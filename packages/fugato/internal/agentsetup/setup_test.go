package agentsetup

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, agent string) Options {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project with ' quote")
	binDir := filepath.Join(root, "bin with space")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(binDir, "fugato"), "#!/bin/sh\necho daemon-output\n", 0700)
	write(t, filepath.Join(binDir, "fugato-mcp"), "#!/bin/sh\nprintf '%s\\n' \"mcp:$1\" \"$FUGATO_API_URL\" \"$FUGATO_API_TOKEN_FILE\"\n", 0700)
	t.Setenv("FUGATO_STATE_DIR", filepath.Join(root, "private state"))
	t.Setenv("FUGATO_API_PORT", "46655")
	t.Setenv("FUGATO_PROXY_PORT", "46656")
	return Options{Agent: agent, Project: project, BinDir: binDir}
}

func write(t *testing.T, name, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSetupProfiles(t *testing.T) {
	for agent, profile := range profiles {
		t.Run(agent, func(t *testing.T) {
			options := fixture(t, agent)
			configFile := filepath.Join(options.Project, profile.config)
			prior := `{"keep":9007199254740993,"` + profile.key + `":{"other":{"command":"other"}}}`
			if agent == "codex" {
				prior = "# keep my settings\nmodel = \"example\"\n[mcp_servers.other]\ncommand = \"other\"\n"
			}
			write(t, configFile, prior, 0640)
			instructionFile := filepath.Join(options.Project, profile.instructions)
			if agent != "cursor" {
				write(t, instructionFile, "Keep project conventions.\n", 0640)
			}
			options.DryRun = true
			plan, err := Setup(options)
			if err != nil || len(plan.Files) < 6 {
				t.Fatalf("dry run: %+v, %v", plan, err)
			}
			if _, err := os.Stat(filepath.Join(options.Project, ".fugato-agent")); !os.IsNotExist(err) {
				t.Fatalf("dry run created launcher directory: %v", err)
			}
			if got := read(t, configFile); got != prior {
				t.Fatalf("dry run changed config: %s", got)
			}
			options.DryRun = false
			result, err := Setup(options)
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := Setup(options)
			if err != nil || len(repeated.Files) != 0 {
				t.Fatalf("setup was not idempotent: %+v, %v", repeated, err)
			}
			for _, relative := range []string{"SKILL.md", "references/mcp.md", "references/application-compatibility.md", "LICENSE", "NOTICE"} {
				if _, err := os.Stat(filepath.Join(options.Project, profile.skill, "fugato-workflow", relative)); err != nil {
					t.Fatalf("missing bundled %s: %v", relative, err)
				}
			}
			instructions := read(t, instructionFile)
			if !strings.Contains(instructions, profile.skill+"/fugato-workflow/SKILL.md") || (agent != "cursor" && !strings.Contains(instructions, "Keep project conventions.")) {
				t.Fatalf("instructions not preserved or skill not discoverable: %s", instructions)
			}
			config := read(t, configFile)
			if agent == "codex" {
				if !strings.Contains(config, "[mcp_servers.other]") {
					t.Fatalf("lost another server: %s", config)
				}
			} else {
				var parsed map[string]json.RawMessage
				if err := json.Unmarshal([]byte(config), &parsed); err != nil {
					t.Fatal(err)
				}
				if string(parsed["keep"]) != "9007199254740993" || !strings.Contains(string(parsed[profile.key]), `"other"`) {
					t.Fatalf("unrelated config changed: %s", config)
				}
			}
			if info, err := os.Stat(configFile); err != nil || info.Mode().Perm() != 0640 {
				t.Fatalf("existing permissions changed: %v %v", info, err)
			}
			if info, err := os.Stat(result.Launcher); err != nil || info.Mode().Perm()&0077 != 0 {
				t.Fatalf("new launcher is not private: %v %v", info, err)
			}
			output, err := exec.Command("/bin/sh", result.Launcher, "--check").CombinedOutput()
			if err != nil || !strings.HasPrefix(string(output), "mcp:--check\nhttp://127.0.0.1:46655\n") || strings.Contains(string(output), "daemon-output") {
				t.Fatalf("launcher failed or leaked daemon stdout: %s, %v", output, err)
			}
			if !strings.Contains(string(output), filepath.Join(os.Getenv("FUGATO_STATE_DIR"), "token")) {
				t.Fatalf("launcher lost token-file setting: %s", output)
			}
			if _, err := os.Stat(os.Getenv("FUGATO_STATE_DIR")); !os.IsNotExist(err) {
				t.Fatalf("setup started a daemon or created runtime state: %v", err)
			}
		})
	}
}

func TestSetupRejectsConflictsWithoutPartialInstallation(t *testing.T) {
	for _, tt := range []struct {
		name, agent, file, content, want string
	}{
		{"existing MCP server", "cursor", ".cursor/mcp.json", `{"mcpServers":{"fugato":{"command":"mine"}}}`, "existing fugato"},
		{"malformed JSON", "cursor", ".cursor/mcp.json", "{ bad", "expected strict JSON"},
		{"trailing JSON", "cursor", ".cursor/mcp.json", "{} {}", "one strict JSON"},
		{"array config", "cursor", ".cursor/mcp.json", "[]", "expected strict JSON"},
		{"null config", "cursor", ".cursor/mcp.json", "null", "expected an object"},
		{"null servers", "cursor", ".cursor/mcp.json", `{"mcpServers":null}`, "expected an object"},
		{"malformed markers", "codex", "AGENTS.md", end + "\n" + start, "malformed Fugato"},
		{"duplicate markers", "codex", "AGENTS.md", start + start + end, "malformed Fugato"},
		{"unmanaged Cursor rule", "cursor", ".cursor/rules/fugato.mdc", "My rules", "not managed"},
		{"quoted TOML table", "codex", ".codex/config.toml", "[\"mcp_servers\".\"fugato\"]\ncommand = \"mine\"\n", "review existing MCP TOML"},
		{"TOML inline servers", "codex", ".codex/config.toml", "mcp_servers = { other = {} }\n", "review existing MCP TOML"},
		{"incomplete ignore markers", "codex", ".gitignore", ignoreStart + "\n", "malformed Fugato ignore markers"},
		{"edited ignore block", "codex", ".gitignore", ignoreStart + "\n!/.codex/config.toml\n" + ignoreEnd + "\n", "edited Fugato ignore block"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := fixture(t, tt.agent)
			file := filepath.Join(options.Project, tt.file)
			write(t, file, tt.content, 0600)
			if _, err := Setup(options); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("wanted %q, got %v", tt.want, err)
			}
			if got := read(t, file); got != tt.content {
				t.Fatalf("conflicting file changed: %s", got)
			}
			for _, relative := range []string{".fugato-agent", ".agents/skills/fugato-workflow", ".claude/skills/fugato-workflow"} {
				if _, err := os.Stat(filepath.Join(options.Project, relative)); !os.IsNotExist(err) {
					t.Fatalf("partial installation at %s: %v", relative, err)
				}
			}
		})
	}
}

func TestSetupPreservesEditedManagedFiles(t *testing.T) {
	for _, relative := range []string{".agents/skills/fugato-workflow/SKILL.md", ".fugato-agent/mcp-launch.sh"} {
		t.Run(relative, func(t *testing.T) {
			options := fixture(t, "codex")
			if _, err := Setup(options); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(options.Project, relative)
			write(t, file, "My custom content", 0600)
			if _, err := Setup(options); err == nil || !strings.Contains(err.Error(), "existing file differs") {
				t.Fatalf("expected review of edited file, got %v", err)
			}
			if got := read(t, file); got != "My custom content" {
				t.Fatalf("edited file replaced: %s", got)
			}
		})
	}
}

func TestSetupRejectsSymlinksAndNonregularDestinations(t *testing.T) {
	for _, tt := range []struct{ name, relative string }{
		{"symlink parent", ".agents"},
		{"symlink file", ".cursor/mcp.json"},
		{"symlink ignore file", ".gitignore"},
		{"directory file", ".cursor/mcp.json"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := fixture(t, "cursor")
			file := filepath.Join(options.Project, tt.relative)
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if tt.name == "directory file" {
				if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(t.TempDir(), file); err != nil {
				t.Fatal(err)
			}
			if _, err := Setup(options); err == nil {
				t.Fatal("unsafe destination accepted")
			}
			if _, err := os.Stat(filepath.Join(options.Project, ".fugato-agent")); !os.IsNotExist(err) {
				t.Fatalf("unsafe destination caused partial installation: %v", err)
			}
		})
	}
}

func TestSetupValidatesEnvironment(t *testing.T) {
	for _, tt := range []struct{ name, key, value, want string }{
		{"relative state", "FUGATO_STATE_DIR", "relative", "must be absolute"},
		{"invalid port", "FUGATO_API_PORT", "abc", "between 1 and 65535"},
		{"zero port", "FUGATO_API_PORT", "0", "between 1 and 65535"},
		{"large port", "FUGATO_PROXY_PORT", "65536", "between 1 and 65535"},
		{"same ports", "FUGATO_PROXY_PORT", "046655", "must differ"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := fixture(t, "codex")
			t.Setenv(tt.key, tt.value)
			if _, err := Setup(options); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("wanted %q, got %v", tt.want, err)
			}
		})
	}
}

func TestLauncherStopsIfDaemonCannotStart(t *testing.T) {
	options := fixture(t, "codex")
	write(t, filepath.Join(options.BinDir, "fugato"), "#!/bin/sh\nexit 7\n", 0700)
	result, err := Setup(options)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("/bin/sh", result.Launcher, "--check").CombinedOutput()
	if err == nil || len(output) != 0 {
		t.Fatalf("MCP ran after daemon failure: %s, %v", output, err)
	}
	if status, ok := err.(*exec.ExitError); !ok || status.ExitCode() != 7 {
		t.Fatalf("daemon failure status was lost: %v", err)
	}
}

func TestSetupRequiresBothBinaries(t *testing.T) {
	options := fixture(t, "codex")
	if err := os.Remove(filepath.Join(options.BinDir, "fugato-mcp")); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(options); err == nil || !strings.Contains(err.Error(), "fugato-mcp") {
		t.Fatalf("missing MCP binary accepted: %v", err)
	}
}
