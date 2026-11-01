package fugato

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config describes a versioned Fugato environment.
type Config struct {
	Version   int                       `yaml:"version"`
	Prepare   PrepareConfig             `yaml:"prepare,omitempty"`
	Start     StartConfig               `yaml:"start,omitempty"`
	Endpoints map[string]EndpointConfig `yaml:"endpoints,omitempty"`
	Commands  map[string]CommandConfig  `yaml:"commands,omitempty"`
	Checks    map[string]CheckConfig    `yaml:"checks,omitempty"`
	Compose   string                    `yaml:"compose,omitempty"`
	Browser   struct {
		Service string `yaml:"service"`
		Profile string `yaml:"profile,omitempty"`
	} `yaml:"browser,omitempty"`
}

type Project struct {
	ID             string
	Dir            string
	Worktree       string
	Repository     string
	ComposeFile    string
	ComposeFiles   []string
	ComposeProject string
	ConfigFile     string
	OverrideYAML   string
	Config         Config
}

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func ResolveProject(ctx context.Context, input string) (Project, error) {
	p, err := DiscoverProject(ctx, input, "")
	if err != nil {
		return Project{}, err
	}
	return CompleteProject(ctx, p)
}

// DiscoverProject finds the nearest project without requiring a Fugato config.
// composeHint is used by init for nonstandard Compose filenames.
func DiscoverProject(ctx context.Context, input, composeHint string) (Project, error) {
	return discoverProject(ctx, input, composeHint, false)
}

func discoverProject(ctx context.Context, input, composeHint string, allowEmpty bool) (Project, error) {
	return discoverProjectInputs(ctx, input, composeHint, allowEmpty, false)
}

// preparing permits missing runtime inputs that the explicit preparation plan
// may create. Ordinary discovery and all runtime validation remain strict.
func discoverProjectInputs(ctx context.Context, input, composeHint string, allowEmpty, preparing bool) (Project, error) {
	if input == "" {
		input = "."
	}
	dir, err := filepath.Abs(input)
	if err != nil {
		return Project{}, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return Project{}, fmt.Errorf("resolve worktree path: %w", err)
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return Project{}, fmt.Errorf("worktree path is not a directory: %s", dir)
	}
	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	root, err := gitOutput(gitCtx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Project{}, fmt.Errorf("not inside a Git worktree: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Project{}, err
	}
	common, err := gitOutput(gitCtx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return Project{}, err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	common = filepath.Clean(common)
	if data, readErr := os.ReadFile(common); readErr == nil && strings.HasPrefix(string(data), "gitdir: ") {
		common = filepath.Join(filepath.Dir(common), strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir: ")))
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return Project{}, fmt.Errorf("resolve Git repository identity: %w", err)
	}
	configDir := dir
	var configPath string
	for {
		candidate := filepath.Join(configDir, ".fugato.yaml")
		if _, statErr := os.Lstat(candidate); statErr == nil {
			configPath = candidate
			break
		}
		if composeHint != "" || len(defaultComposeFiles(configDir)) > 0 {
			break
		}
		if configDir == root {
			if allowEmpty {
				configDir = dir
				break
			}
			return Project{}, fmt.Errorf("no .fugato.yaml or Compose file between %s and Git worktree root %s", dir, root)
		}
		parent := filepath.Dir(configDir)
		if parent == configDir || !within(root, parent) {
			return Project{}, errors.New("project path escaped Git worktree")
		}
		configDir = parent
	}
	cfg := Config{Version: 1}
	if configPath != "" {
		configPath, err = filepath.EvalSymlinks(configPath)
		if err != nil || !within(configDir, configPath) {
			return Project{}, errors.New(".fugato.yaml must be a file within the project directory")
		}
		configInfo, statErr := os.Stat(configPath)
		if statErr != nil || !configInfo.Mode().IsRegular() || configInfo.Size() > 64<<10 {
			return Project{}, errors.New(".fugato.yaml must be a regular file no larger than 64 KiB")
		}
		data, readErr := os.ReadFile(configPath)
		if readErr != nil {
			return Project{}, readErr
		}
		cfg, err = parseProjectConfig(data)
		if err != nil {
			return Project{}, err
		}
	}
	if composeHint != "" {
		cfg.Compose = composeHint
	}
	composeFiles := defaultComposeFiles(configDir)
	if cfg.Compose != "" {
		if filepath.IsAbs(cfg.Compose) {
			return Project{}, errors.New("compose path must be relative to the project directory")
		}
		composeFiles = []string{filepath.Join(configDir, cfg.Compose)}
	}

	for i, file := range composeFiles {
		if preparing {
			if err := validatePreparationRuntimePath(root, file, false); err != nil {
				return Project{}, err
			}
			continue
		}
		resolved, resolveErr := filepath.EvalSymlinks(file)
		if resolveErr != nil || !within(root, resolved) {
			return Project{}, fmt.Errorf("Compose file must exist within the Git worktree: %s", file)
		}
		info, statErr := os.Stat(resolved)
		if statErr != nil || !info.Mode().IsRegular() {
			return Project{}, fmt.Errorf("Compose file is not a regular file: %s", file)
		}
		composeFiles[i] = resolved
	}
	if preparing {
		for _, command := range cfg.Commands {
			if command.Host != nil {
				if err := validatePreparationRuntimePath(root, filepath.Join(configDir, command.Host.Cwd), true); err != nil {
					return Project{}, err
				}
			}
		}
	} else if err := validateHostDirectories(cfg, configDir, root); err != nil {
		return Project{}, err
	}
	composeFile := ""
	if len(composeFiles) > 0 {
		composeFile = composeFiles[0]
	}
	sum := sha256.Sum256([]byte(common + "\x00" + configDir))
	id := hex.EncodeToString(sum[:8])
	return Project{ID: id, Dir: configDir, Worktree: root, Repository: common, ComposeFile: composeFile,
		ComposeFiles: composeFiles, ComposeProject: "fugato-" + id, ConfigFile: configPath, Config: cfg}, nil
}

func defaultComposeFiles(dir string) []string {
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		file := filepath.Join(dir, name)
		if _, err := os.Lstat(file); err == nil {
			files := []string{file}
			overrides := []string{"compose.override.yaml", "compose.override.yml"}
			if strings.HasPrefix(name, "docker-compose.") {
				overrides = []string{"docker-compose.override.yaml", "docker-compose.override.yml"}
			}
			for _, override := range overrides {
				if _, err := os.Lstat(filepath.Join(dir, override)); err == nil {
					return append(files, filepath.Join(dir, override))
				}
			}
			return files
		}
	}
	return nil
}

func parseProjectConfig(data []byte) (Config, error) {
	if len(data) > 64<<10 {
		return Config{}, errors.New(".fugato.yaml must be no larger than 64 KiB")
	}
	if err := misplacedCommandField(data); err != nil {
		return Config{}, err
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("invalid .fugato.yaml: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, errors.New(".fugato.yaml must contain exactly one YAML document")
	}
	if cfg.Version != 1 {
		return Config{}, errors.New(".fugato.yaml requires version: 1")
	}
	if err := validateConfigFields(data); err != nil {
		return Config{}, err
	}
	return cfg, validateConfig(cfg)
}

func misplacedCommandField(data []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil || len(document.Content) == 0 {
		return nil // The normal decoder reports syntax errors.
	}
	commands := yamlMappingValue(document.Content[0], "commands")
	if commands == nil || commands.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(commands.Content); i += 2 {
		name, command := commands.Content[i].Value, commands.Content[i+1]
		if command.Kind != yaml.MappingNode {
			continue
		}
		for _, field := range []string{"cwd", "argv", "env"} {
			if yamlMappingValue(command, field) != nil {
				return fmt.Errorf("commands.%s.%s belongs under commands.%s.host.%s", name, field, name, field)
			}
		}
	}
	return nil
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func validHealthPath(value string) bool {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || len(value) > 2048 || strings.ContainsAny(value, "\\\r\n#") {
		return false
	}
	u, err := url.ParseRequestURI(value)
	return err == nil && !u.IsAbs() && u.Host == "" && u.RawQuery == "" && path.Clean(u.Path) == u.Path
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
