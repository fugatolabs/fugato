package fugato

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ProjectDiscovery reports configuration and candidates without executing project commands.
type ProjectDiscovery struct {
	Worktree  string              `json:"worktree"`
	Projects  []DiscoveredProject `json:"projects"`
	Truncated bool                `json:"truncated"`
}

type DiscoveredProject struct {
	ID           string               `json:"id,omitempty"`
	ProjectDir   string               `json:"project_dir"`
	ConfigFile   string               `json:"config_file,omitempty"`
	ComposeFiles []string             `json:"compose_files"`
	Kind         string               `json:"kind"`
	Configured   bool                 `json:"configured"`
	Error        string               `json:"error,omitempty"`
	Commands     []DiscoveredCommand  `json:"commands"`
	Checks       []string             `json:"checks"`
	CheckDetails []DiscoveredCheck    `json:"check_details,omitempty"`
	Endpoints    []DiscoveredEndpoint `json:"endpoints"`
}

type DiscoveredCommand struct {
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// DiscoveredCheck reports execution prerequisites declared by a check and its
// referenced command.
type DiscoveredCheck struct {
	Name            string `json:"name"`
	RequiresRunning bool   `json:"requires_running"`
	Reason          string `json:"reason,omitempty"`
}

func discoverCheck(cfg Config, name string) DiscoveredCheck {
	detail := DiscoveredCheck{Name: name}
	check := cfg.Checks[name]
	if check.HTTP != nil {
		detail.Reason = "Requires a running endpoint"
	} else if command := cfg.Commands[check.Command]; command.Exec != nil {
		detail.Reason = "Executes inside a running Compose service"
	} else if command.Host != nil {
		usesPort := func(value string) bool {
			for _, variable := range fugatoVariable.FindAllStringSubmatch(value, -1) {
				if strings.HasPrefix(variable[1], "FUGATO_PORT_") {
					return true
				}
			}
			return false
		}
		for _, value := range command.Host.Argv {
			if usesPort(value) {
				detail.Reason = "Uses runtime endpoint port bindings"
				break
			}
		}
		for _, value := range command.Host.Env {
			if usesPort(value) {
				detail.Reason = "Uses runtime endpoint port bindings"
				break
			}
		}
	}
	detail.RequiresRunning = detail.Reason != ""
	return detail
}

type DiscoveredEndpoint struct {
	Name string `json:"name"`
	EndpointConfig
}

// DiscoverProjects searches at most 10,000 entries and 100 projects within five seconds.
// It does not invoke Docker, start a daemon, or interpret package scripts as authorization.
func DiscoverProjects(ctx context.Context, input string) (ProjectDiscovery, error) {
	if input == "" {
		input = "."
	}
	dir, err := filepath.Abs(input)
	if err != nil {
		return ProjectDiscovery{}, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return ProjectDiscovery{}, fmt.Errorf("resolve discovery path: %w", err)
	}
	scanCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	root, err := gitOutput(scanCtx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ProjectDiscovery{}, fmt.Errorf("not inside a Git worktree: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return ProjectDiscovery{}, err
	}
	report := ProjectDiscovery{Worktree: root, Projects: []DiscoveredProject{}}
	excluded, err := discoveryExclusions(scanCtx, root)
	if err != nil {
		return ProjectDiscovery{}, err
	}
	seen := map[string]bool{}
	entries := 0
	err = walkDiscovery(root, func(path string, entry fs.DirEntry, walkErr error) error {
		entries++
		if entries > 10000 || len(report.Projects) >= 100 || scanCtx.Err() != nil {
			report.Truncated = true
			return fs.SkipAll
		}
		if walkErr != nil {
			report.Truncated = true
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if excluded[path] {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != root && skipDiscoveryDirectory(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !discoveryMarker(name) {
			return nil
		}
		projectDir := filepath.Dir(path)
		if seen[projectDir] {
			return nil
		}
		seen[projectDir] = true
		candidate := DiscoveredProject{
			ProjectDir: projectDir, ComposeFiles: []string{}, Kind: "manifest", Commands: []DiscoveredCommand{},
			Checks: []string{}, Endpoints: []DiscoveredEndpoint{},
		}
		configFile := filepath.Join(projectDir, ".fugato.yaml")
		if info, err := os.Lstat(configFile); err == nil && info.Mode()&os.ModeSymlink == 0 {
			candidate.ConfigFile = configFile
			candidate.Configured = true
		}
		composeFiles := defaultComposeFiles(projectDir)
		if candidate.Configured || len(composeFiles) > 0 {
			candidate.Kind = "compose"
			candidate.ComposeFiles = append(candidate.ComposeFiles, composeFiles...)
			p, err := DiscoverProject(scanCtx, projectDir, "")
			if err != nil {
				candidate.Error = err.Error()
			} else {
				candidate.ID = p.ID
				candidate.ComposeFiles = append([]string{}, p.ComposeFiles...)
				candidate.Kind = "native"
				if len(p.ComposeFiles) > 0 {
					candidate.Kind = "compose"
				}
				for commandName, command := range p.Config.Commands {
					candidate.Commands = append(candidate.Commands, DiscoveredCommand{Name: commandName, Mode: command.Mode})
					if command.Host != nil && len(p.ComposeFiles) > 0 {
						candidate.Kind = "mixed"
					}
				}
				slices.SortFunc(candidate.Commands, func(a, b DiscoveredCommand) int { return compareNames(a.Name, b.Name) })
				for checkName := range p.Config.Checks {
					candidate.Checks = append(candidate.Checks, checkName)
				}
				slices.Sort(candidate.Checks)
				for _, checkName := range candidate.Checks {
					candidate.CheckDetails = append(candidate.CheckDetails, discoverCheck(p.Config, checkName))
				}
				for endpointName, endpoint := range p.Config.Endpoints {
					candidate.Endpoints = append(candidate.Endpoints, DiscoveredEndpoint{Name: endpointName, EndpointConfig: endpoint})
				}
				slices.SortFunc(candidate.Endpoints, func(a, b DiscoveredEndpoint) int { return compareNames(a.Name, b.Name) })
			}
		}
		report.Projects = append(report.Projects, candidate)
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return ProjectDiscovery{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectDiscovery{}, err
	}
	if err := scanCtx.Err(); errors.Is(err, context.DeadlineExceeded) {
		report.Truncated = true
	}
	slices.SortFunc(report.Projects, func(a, b DiscoveredProject) int { return compareNames(a.ProjectDir, b.ProjectDir) })
	return report, nil
}

func compareNames(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func skipDiscoveryDirectory(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "dist", "build", "out", "target", "coverage",
		".next", ".nuxt", ".venv", "venv", "__pycache__", ".fugato", ".vscode-test", ".vscode-test-web",
		".cache", ".turbo", ".yarn", ".pnpm-store", ".gradle", ".pytest_cache", ".mypy_cache", ".ruff_cache":
		return true
	}
	return false
}

// Git applies nested .gitignore files, repository excludes, and global excludes.
// --directory collapses ignored trees so downloaded dependencies never need to
// be enumerated by our scanner. Reuse the metadata output cap and scan deadline.
func discoveryExclusions(ctx context.Context, root string) (map[string]bool, error) {
	var output cappedGitOutput
	// Reading the index must not run a repository-configured fsmonitor hook.
	cmd := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	cmd.Dir = root
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("read project discovery exclusions: %w", err)
	}
	excluded := map[string]bool{}
	for name := range strings.SplitSeq(output.String(), "\x00") {
		if name == "" {
			continue
		}
		excluded[filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(name, "/")))] = true
	}
	return excluded, nil
}

func discoveryMarker(name string) bool {
	switch name {
	case ".fugato.yaml", "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml", "package.json":
		return true
	}
	return false
}

// Read directories in bounded batches; filepath.WalkDir sorts and buffers every
// entry before its callback can enforce our entry limit.
func walkDiscovery(root string, visit fs.WalkDirFunc) error {
	info, err := os.Lstat(root)
	if err != nil {
		return visit(root, nil, err)
	}
	entry := fs.FileInfoToDirEntry(info)
	if err := visit(root, entry, nil); err != nil {
		return err
	}
	if !entry.IsDir() {
		return nil
	}
	dir, err := os.Open(root)
	if err != nil {
		return visit(root, entry, err)
	}
	defer dir.Close()
	for {
		entries, readErr := dir.ReadDir(128)
		for _, child := range entries {
			childPath := filepath.Join(root, child.Name())
			var visitErr error
			if child.IsDir() {
				visitErr = walkDiscovery(childPath, visit)
			} else {
				visitErr = visit(childPath, child, nil)
			}
			if visitErr != nil && !errors.Is(visitErr, fs.SkipDir) {
				return visitErr
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return visit(root, entry, readErr)
		}
	}
}
