package fugato

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// PrepareConfig names the only local files eligible for preparation.
type PrepareConfig struct {
	Files   []PrepareFile `yaml:"files,omitempty"`
	Exclude []string      `yaml:"exclude,omitempty"`
	Allow   []string      `yaml:"allow,omitempty"`
}

type PrepareFile struct {
	Path     string `yaml:"path"`
	Required *bool  `yaml:"required,omitempty"`
}

func (f PrepareFile) required() bool { return f.Required == nil || *f.Required }

func validatePreparePath(name string) error {
	if name == "" || name == "." || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") || strings.ContainsAny(name, "*?[]{}") {
		return errors.New("prepare path must be a clean relative file or directory path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || strings.EqualFold(part, ".git") {
			return errors.New("prepare path must not traverse parents or Git metadata")
		}
	}
	return nil
}

func validatePrepareConfig(cfg PrepareConfig) error {
	if len(cfg.Files) > prepareMaxEntries {
		return errors.New("prepare.files exceeds 10000 entries")
	}
	seen := map[string]bool{}
	for _, file := range cfg.Files {
		if err := validatePreparePath(file.Path); err != nil {
			return fmt.Errorf("prepare.files: %w", err)
		}
		if seen[file.Path] {
			return fmt.Errorf("duplicate prepare.files path %q", file.Path)
		}
		seen[file.Path] = true
	}
	for _, patterns := range [][]string{cfg.Exclude, cfg.Allow} {
		for _, pattern := range patterns {
			if _, err := preparePattern(pattern); err != nil {
				return err
			}
		}
	}
	return nil
}

// Patterns are anchored slash paths; matching a directory also matches its children.
// ** occupies a whole component and matches zero or more components.
func preparePattern(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" || pattern == "." || path.IsAbs(pattern) || path.Clean(pattern) != pattern || strings.ContainsAny(pattern, "\\\x00\r\n[]{}") {
		return nil, errors.New("invalid prepare glob pattern")
	}
	rawParts := strings.Split(pattern, "/")
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		if part == "**" && len(parts) > 0 && parts[len(parts)-1] == "**" {
			continue
		}
		parts = append(parts, part)
	}
	trailingRecursive := len(parts) > 1 && parts[len(parts)-1] == "**"
	if trailingRecursive {
		parts = parts[:len(parts)-1]
	}
	var b strings.Builder
	b.WriteString("^")
	for i, part := range parts {
		if part == ".." || part == "." || strings.Contains(part, "**") && part != "**" {
			return nil, errors.New("invalid prepare glob pattern")
		}
		if part == "**" {
			if i == len(parts)-1 {
				b.WriteString(".*")
			} else {
				b.WriteString("(?:[^/]+/)*")
			}
			continue
		}
		for _, c := range part {
			switch c {
			case '*':
				b.WriteString("[^/]*")
			case '?':
				b.WriteString("[^/]")
			default:
				b.WriteString(regexp.QuoteMeta(string(c)))
			}
		}
		if i < len(parts)-1 {
			b.WriteString("/")
		}
	}
	if trailingRecursive {
		b.WriteString("(?:/.*)?")
	}
	b.WriteString("(?:/.*)?$")
	return regexp.Compile(b.String())
}
