package fugato

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSuggestsHostPlacementForMisplacedCommandFields(t *testing.T) {
	for _, field := range []string{"cwd", "argv", "env"} {
		t.Run(field, func(t *testing.T) {
			value := "."
			if field == "argv" {
				value = "[echo, ok]"
			} else if field == "env" {
				value = "{PORT: '3000'}"
			}
			data := []byte("version: 1\ncommands:\n  web:\n    mode: service\n    " + field + ": " + value + "\n")
			_, err := parseProjectConfig(data)
			if err == nil || !strings.Contains(err.Error(), "commands.web.host."+field) {
				t.Fatalf("expected actionable host placement error, got %v", err)
			}
		})
	}
}

func TestConfigValidNativeEnvironment(t *testing.T) {
	data := `version: 1
start:
  commands: [build, web]
commands:
  build:
    mode: task
    host:
      argv: [go, build, ./...]
  web:
    mode: service
    host:
      argv: [node, server.js]
      env:
        PORT: '${FUGATO_PORT_site}'
        DATA_DIR: '${FUGATO_DATA_DIR}'
endpoints:
  site:
    command: web
    port: 0
    protocol: http
    browser: true
checks:
  ready:
    http:
      endpoint: site
      path: /health
  build:
    command: build
`
	cfg, err := parseProjectConfig([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Commands) != 2 || cfg.Endpoints["site"].Command != "web" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestConfigRejectsInvalidContracts(t *testing.T) {
	tests := []struct{ name, config string }{
		{name: "omitted native port", config: "version: 1\ncommands: {web: {mode: service, host: {argv: [node]}}}\nendpoints: {site: {command: web, protocol: http}}"},
		{name: "removed web block", config: "version: 1\nweb: {}"},
		{name: "null block", config: "version: 1\ncommands: null"},
		{name: "empty browser", config: "version: 1\nbrowser: {}"},

		{name: "unknown", config: "version: 1\nunknown: true"},
		{name: "duplicate", config: "version: 1\ncommands: {}\ncommands: {}"},
		{name: "removed web selector", config: "version: 1\nweb: {service: web}"},
		{name: "missing command", config: "version: 1\nstart: {commands: [missing]}"},
		{name: "duplicate start", config: "version: 1\nstart: {services: [web, web]}"},
		{name: "interactive startup", config: "version: 1\nstart: {commands: [shell]}\ncommands: {shell: {mode: interactive, host: {argv: [sh]}}}"},
		{name: "two executors", config: "version: 1\ncommands: {test: {mode: task, host: {argv: [go]}, run: {service: web, argv: [go]}}}"},
		{name: "container persistent command", config: "version: 1\ncommands: {web: {mode: service, run: {service: web, argv: [go]}}}"},
		{name: "missing argv", config: "version: 1\ncommands: {test: {mode: task, host: {}}}"},
		{name: "unknown variable", config: "version: 1\ncommands: {test: {mode: task, host: {argv: [echo, '${HOME}']}}}"},
		{name: "unknown endpoint variable", config: "version: 1\ncommands: {test: {mode: task, host: {argv: [echo, '${FUGATO_PORT_missing}']}}}"},
		{name: "reserved env", config: "version: 1\ncommands: {test: {mode: task, host: {argv: [echo], env: {FUGATO_DATA_DIR: /tmp}}}}"},
		{name: "browser TCP", config: "version: 1\nendpoints: {api: {service: api, port: 80, protocol: tcp, browser: true}}"},
		{name: "unknown endpoint command", config: "version: 1\nendpoints: {api: {command: api, port: 0, protocol: http}}"},
		{name: "zero container port", config: "version: 1\nendpoints: {api: {service: api, port: 0, protocol: http}}"},
		{name: "check unknown task", config: "version: 1\nchecks: {test: {command: test}}"},
		{name: "absolute cwd", config: "version: 1\ncommands: {test: {mode: task, host: {cwd: /tmp, argv: [echo]}}}"},
		{name: "unsupported version", config: "version: 2\ncommands: {}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseProjectConfig([]byte(tt.config)); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestResolveNativeProjectWithoutDocker(t *testing.T) {
	dir := nativeTestProject(t, "version: 1\ncommands: {test: {mode: task, host: {argv: [git, status]}}}\n")
	p, err := ResolveProject(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Config.Version != 1 || len(p.ComposeFiles) != 0 || len(p.Config.Endpoints) != 0 {
		t.Fatalf("native project gained Compose/browser settings: %+v", p)
	}
	report := Doctor(context.Background(), dir)
	if !report.OK() {
		t.Fatalf("native doctor failed: %+v", report.Findings)
	}
	for _, finding := range report.Findings {
		if strings.Contains(finding.Code, "DOCKER") {
			t.Fatalf("native doctor checked Docker: %+v", finding)
		}
	}
}

func TestHostCwdCannotEscapeWorktree(t *testing.T) {
	dir := nativeTestProject(t, "version: 1\ncommands: {test: {mode: task, host: {cwd: outside, argv: [echo]}}}\n")
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverProject(context.Background(), dir, ""); err == nil {
		t.Fatal("accepted host cwd symlink outside worktree")
	}
}

func TestNativeProjectRejectsComposeReferencesWithoutCompose(t *testing.T) {
	dir := nativeTestProject(t, "version: 1\nstart: {services: [api]}\n")
	if _, err := ResolveProject(context.Background(), dir); err == nil {
		t.Fatal("accepted missing Compose file")
	}
}

func nativeTestProject(t *testing.T, config string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(context.Background(), dir, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".fugato.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestConfigAliasesAndMergedFields(t *testing.T) {
	tests := []struct {
		name, config string
		valid        bool
	}{
		{name: "aliased endpoint", valid: true, config: `version: 1
endpoints:
  first: &endpoint {service: api, port: 8080, protocol: http}
  second: *endpoint
`},
		{name: "merged endpoint", valid: true, config: `version: 1
endpoints:
  first: &endpoint {service: api, port: 8080, protocol: http}
  second: {<<: *endpoint, browser: true}
`},
		{name: "merged root with omitted port", config: `<<:
  version: 1
  commands: {web: {mode: service, host: {argv: [node]}}}
  endpoints: {site: {command: web, protocol: http}}
`},
		{name: "merged removed fields", config: `version: 1
<<: {web: {}}
`},
		{name: "recursive alias", config: `version: 1
commands:
  task: &task {mode: task, host: {argv: [echo]}, <<: *task}
`},
		{name: "aliased null", config: `version: 1
commands:
  task: {mode: task, host: {argv: [echo], env: {A: &nothing null, B: *nothing}}}
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseProjectConfig([]byte(tt.config))
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
		})
	}
}

func TestConfigHostVariablesCanReferenceOtherEndpoints(t *testing.T) {
	data := `version: 1
start:
  services: [api]
  commands: [frontend]
commands:
  frontend:
    mode: service
    host:
      argv: [node, server.js]
      env:
        PORT: '${FUGATO_PORT_site}'
        API_URL: 'http://127.0.0.1:${FUGATO_PORT_api}'
  test:
    mode: task
    host:
      argv: [curl, 'http://127.0.0.1:${FUGATO_PORT_site}']
      env:
        API_URL: 'http://127.0.0.1:${FUGATO_PORT_api}'
endpoints:
  site: {command: frontend, port: 0, protocol: http, browser: true}
  api: {service: api, port: 8080, protocol: http}
`
	if _, err := parseProjectConfig([]byte(data)); err != nil {
		t.Fatal(err)
	}
	invalid := strings.ReplaceAll(data, "${FUGATO_PORT_api}", "${FUGATO_PORT_missing}")
	if _, err := parseProjectConfig([]byte(invalid)); err == nil {
		t.Fatal("unknown endpoint port variable accepted")
	}
}
