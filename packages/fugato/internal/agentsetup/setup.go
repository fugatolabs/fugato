// Package agentsetup installs project-scoped agent instructions, skills, and MCP settings.
package agentsetup

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

//go:embed all:skill
var skill embed.FS

type profile struct{ skill, instructions, config, key string }

var profiles = map[string]profile{
	"codex":          {".agents/skills", "AGENTS.md", ".codex/config.toml", ""},
	"claude-code":    {".claude/skills", "CLAUDE.md", ".mcp.json", "mcpServers"},
	"cursor":         {".agents/skills", ".cursor/rules/fugato.mdc", ".cursor/mcp.json", "mcpServers"},
	"github-copilot": {".agents/skills", ".github/copilot-instructions.md", ".vscode/mcp.json", "servers"},
	"gemini-cli":     {".agents/skills", "GEMINI.md", ".gemini/settings.json", "mcpServers"},
}

const Agents = "codex, claude-code, cursor, github-copilot, gemini-cli"
const start = "<!-- fugato-workflow:start -->"
const end = "<!-- fugato-workflow:end -->"

// Options describes one project installation. Setup never starts a daemon.
type Options struct {
	Agent, Project, BinDir string
	DryRun                 bool
}

// Result lists the files changed, or planned when DryRun is true.
type Result struct {
	Agent    string   `json:"agent"`
	Project  string   `json:"project"`
	Files    []string `json:"files"`
	Launcher string   `json:"launcher"`
	DryRun   bool     `json:"dry_run"`
}

type change struct {
	name          string
	before, after []byte
	existed       bool
	mode          fs.FileMode
}

// Setup validates the entire installation before writing any files. It preserves
// existing settings and refuses to replace user-edited skills or launchers.
func Setup(options Options) (Result, error) {
	var result Result
	p, ok := profiles[options.Agent]
	if !ok {
		return result, fmt.Errorf("choose --agent from: %s", Agents)
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return result, errors.New("Fugato supports macOS and Linux")
	}
	project := options.Project
	if project == "" {
		project = "."
	}
	project, err := filepath.Abs(project)
	if err != nil {
		return result, err
	}
	project, err = filepath.EvalSymlinks(project)
	if err != nil {
		return result, err
	}
	root, err := os.OpenRoot(project)
	if err != nil {
		return result, fmt.Errorf("open project: %w", err)
	}
	defer root.Close()
	cli, mcp, err := binaries(options.BinDir)
	if err != nil {
		return result, err
	}
	state := os.Getenv("FUGATO_STATE_DIR")
	if state == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			return result, err
		}
		state = filepath.Join(config, "fugato")
	}
	if !filepath.IsAbs(state) {
		return result, errors.New("Fugato state directory must be absolute")
	}
	api, err := port("FUGATO_API_PORT", "46655")
	if err != nil {
		return result, err
	}
	proxy, err := port("FUGATO_PROXY_PORT", "46656")
	if err != nil {
		return result, err
	}
	if api == proxy {
		return result, errors.New("Fugato API and preview ports must differ")
	}
	launcher := filepath.Join(project, ".fugato-agent/mcp-launch.sh")
	plan := []change{}
	add := func(name string, after []byte, preserve bool) error {
		before, existed, mode, err := readRegular(root, name)
		if err != nil {
			return err
		}
		if existed && bytes.Equal(before, after) {
			return nil
		}
		if existed && !preserve {
			return fmt.Errorf("existing file differs; review it before reinstalling: %s", name)
		}
		plan = append(plan, change{name, before, after, existed, mode})
		return nil
	}
	if err := fs.WalkDir(skill, "skill", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := skill.ReadFile(name)
		if err != nil {
			return err
		}
		return add(filepath.Join(p.skill, "fugato-workflow", strings.TrimPrefix(name, "skill/")), data, false)
	}); err != nil {
		return result, err
	}
	launcherText := "#!/bin/sh\nset -eu\n" +
		"export FUGATO_STATE_DIR=" + quote(state) + "\n" +
		"export FUGATO_API_PORT=" + quote(api) + "\n" +
		"export FUGATO_PROXY_PORT=" + quote(proxy) + "\n" +
		"export FUGATO_API_URL=" + quote("http://127.0.0.1:"+api) + "\n" +
		"export FUGATO_API_TOKEN_FILE=" + quote(filepath.Join(state, "token")) + "\n" +
		quote(cli) + " ls >/dev/null\nexec " + quote(mcp) + " \"$@\"\n"
	if err := add(".fugato-agent/mcp-launch.sh", []byte(launcherText), false); err != nil {
		return result, err
	}
	before, _, _, err := readRegular(root, p.instructions)
	if err != nil {
		return result, err
	}
	instructions, err := instructions(string(before), options.Agent, p.skill)
	if err != nil {
		return result, err
	}
	if err := add(p.instructions, []byte(instructions), true); err != nil {
		return result, err
	}
	before, _, _, err = readRegular(root, p.config)
	if err != nil {
		return result, err
	}
	config, err := configuration(before, options.Agent, p.key, launcher)
	if err != nil {
		return result, fmt.Errorf("%s: %w", p.config, err)
	}
	if err := add(p.config, config, true); err != nil {
		return result, err
	}
	before, _, _, err = readRegular(root, ".gitignore")
	if err != nil {
		return result, err
	}
	ignore, err := localIgnore(string(before), p.config)
	if err != nil {
		return result, err
	}
	previous := len(plan)
	if err := add(".gitignore", []byte(ignore), true); err != nil {
		return result, err
	}
	if len(plan) > previous {
		// Install exclusions before creating local settings, after every target
		// has passed validation. A later I/O failure must not expose new files.
		plan = append([]change{plan[len(plan)-1]}, plan[:len(plan)-1]...)
	}
	result = Result{options.Agent, project, []string{}, launcher, options.DryRun}
	for _, item := range plan {
		result.Files = append(result.Files, filepath.ToSlash(item.name))
	}
	if !options.DryRun {
		for _, item := range plan {
			if err := apply(root, item); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func port(key, fallback string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		value = fallback
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return "", fmt.Errorf("%s must be between 1 and 65535", key)
		}
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("%s must be between 1 and 65535", key)
	}
	return strconv.Itoa(n), nil
}

func executable(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("install executable %s first, or supply --bin-dir", name)
	}
	return absolute, nil
}

func binaries(binDir string) (string, string, error) {
	if binDir != "" {
		cli, err := executable(filepath.Join(binDir, "fugato"))
		if err != nil {
			return "", "", err
		}
		mcp, err := executable(filepath.Join(binDir, "fugato-mcp"))
		return cli, mcp, err
	}
	cli, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	cli, err = filepath.EvalSymlinks(cli)
	if err != nil {
		return "", "", err
	}
	if mcp, err := executable(filepath.Join(filepath.Dir(cli), "fugato-mcp")); err == nil {
		return cli, mcp, nil
	}
	mcp, err := exec.LookPath("fugato-mcp")
	if err != nil {
		return "", "", errors.New("install fugato-mcp first, or supply --bin-dir")
	}
	mcp, err = executable(mcp)
	return cli, mcp, err
}

func instructions(prior, agent, skillDir string) (string, error) {
	block := start + "\nFor tasks that configure, run, inspect, verify, or stop this project's local environments, read " + skillDir + "/fugato-workflow/SKILL.md and follow its referenced guidance. Use the Fugato MCP server for supported runtime inspection, lifecycle, logs, and verification; use the CLI for configuration, preparation, and named tasks/checks. Match the exact project path and runtime before acting. Respect the user's requested scope and the agent's permissions. Report checks actually run and any failures.\n" + end
	if strings.Contains(prior, start) || strings.Contains(prior, end) {
		if strings.Count(prior, start) != 1 || strings.Count(prior, end) != 1 || strings.Index(prior, end) < strings.Index(prior, start) {
			return "", errors.New("malformed Fugato instruction markers")
		}
		return prior[:strings.Index(prior, start)] + block + prior[strings.Index(prior, end)+len(end):], nil
	}
	if agent == "cursor" && prior != "" {
		return "", errors.New("existing fugato.mdc is not managed by this installer")
	}
	prefix := ""
	if prior != "" {
		prefix = strings.TrimRight(prior, " \t\r\n") + "\n\n"
	} else if agent == "cursor" {
		prefix = "---\nalwaysApply: true\n---\n\n"
	}
	return prefix + block + "\n", nil
}

var ambiguousTOML = regexp.MustCompile(`(?m)\[\s*["']?mcp_servers["']?\s*\]|^\s*["']?mcp_servers["']?\s*=`)

func configuration(prior []byte, agent, key, launcher string) ([]byte, error) {
	if agent == "codex" {
		encoded, err := json.Marshal(launcher)
		if err != nil {
			return nil, err
		}
		table := "\n[mcp_servers.fugato]\ncommand = \"/bin/sh\"\nargs = [" + string(encoded) + "]\n"
		text := string(prior)
		if strings.HasSuffix(text, table) {
			return prior, nil
		}
		if strings.Contains(text, "mcp_servers") && (strings.Contains(text, "fugato") || ambiguousTOML.MatchString(text)) {
			return nil, errors.New("review existing MCP TOML before adding Fugato")
		}
		return []byte(text + table), nil
	}
	config := map[string]any{}
	if len(prior) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(prior))
		decoder.UseNumber()
		if err := decoder.Decode(&config); err != nil {
			return nil, errors.New("expected strict JSON; preserve and convert comments/trailing commas before setup")
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, errors.New("expected one strict JSON object")
		}
	}
	if config == nil {
		return nil, errors.New("invalid MCP configuration: expected an object")
	}
	servers := map[string]any{}
	if existing, ok := config[key]; ok {
		var valid bool
		servers, valid = existing.(map[string]any)
		if !valid || servers == nil {
			return nil, errors.New("invalid MCP server settings: expected an object")
		}
	}
	server := map[string]any{"command": "/bin/sh", "args": []any{launcher}}
	if agent != "gemini-cli" {
		server["type"] = "stdio"
	}
	if existing, ok := servers["fugato"]; ok && !reflect.DeepEqual(existing, server) {
		return nil, errors.New("existing fugato MCP server differs")
	}
	servers["fugato"] = server
	config[key] = servers
	data, err := json.MarshalIndent(config, "", "  ")
	return append(data, '\n'), err
}

func safeTarget(root *os.Root, name string) error {
	for current := name; current != "."; current = filepath.Dir(current) {
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink: %s", current)
		}
	}
	return nil
}

func readRegular(root *os.Root, name string) ([]byte, bool, fs.FileMode, error) {
	if err := safeTarget(root, name); err != nil {
		return nil, false, 0, err
	}
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, 0600, nil
	}
	if err != nil {
		return nil, false, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, 0, fmt.Errorf("expected a regular file: %s", name)
	}
	data, err := root.ReadFile(name)
	return data, true, info.Mode().Perm(), err
}

func apply(root *os.Root, item change) error {
	data, existed, _, err := readRegular(root, item.name)
	if err != nil {
		return err
	}
	if existed != item.existed || !bytes.Equal(data, item.before) {
		return fmt.Errorf("file changed during setup: %s", item.name)
	}
	if err := root.MkdirAll(filepath.Dir(item.name), 0700); err != nil {
		return err
	}
	temporary := fmt.Sprintf("%s.fugato-%d.tmp", item.name, os.Getpid())
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, item.mode)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err := file.Write(item.after); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return root.Rename(temporary, item.name)
}
