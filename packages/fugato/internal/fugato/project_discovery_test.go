package fugato

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverProjectsReportsExplicitCheckPrerequisites(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		name := "native"
		if mixed {
			name = "mixed"
		}
		t.Run(name, func(t *testing.T) {
			config := `version: 1
commands:
  server: {mode: service, host: {argv: [touch, SHOULD_NOT_EXIST]}}
  independent: {mode: task, host: {argv: [echo, '${FUGATO_RUNTIME_ID}', '${FUGATO_DATA_DIR}']}}
  port-arg: {mode: task, host: {argv: [echo, '${FUGATO_PORT_site}']}}
  port-env: {mode: task, host: {argv: [echo, ok], env: {PORT: '${FUGATO_PORT_site}'}}}
`
			if mixed {
				config += "  container-exec: {mode: task, exec: {service: web, argv: [echo, ok]}}\n  container-run: {mode: task, run: {service: web, argv: [echo, ok]}}\n"
			}
			config += `endpoints:
  site: {command: server, port: 0, protocol: http}
checks:
  arg-port: {command: port-arg}
  env-port: {command: port-env}
  http: {http: {endpoint: site, path: /}}
  independent: {command: independent}
`
			if mixed {
				config += "  run: {command: container-run}\n  service-exec: {command: container-exec}\n"
			}
			dir := nativeTestProject(t, config)
			if mixed {
				if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {web: {image: test}}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			report, err := DiscoverProjects(t.Context(), dir)
			if err != nil || len(report.Projects) != 1 || report.Projects[0].Error != "" {
				t.Fatalf("discovery: %+v, %v", report, err)
			}
			candidate := report.Projects[0]
			want := []DiscoveredCheck{
				{Name: "arg-port", RequiresRunning: true, Reason: "Uses runtime endpoint port bindings"},
				{Name: "env-port", RequiresRunning: true, Reason: "Uses runtime endpoint port bindings"},
				{Name: "http", RequiresRunning: true, Reason: "Requires a running endpoint"},
				{Name: "independent"},
			}
			if mixed {
				want = append(want, DiscoveredCheck{Name: "run"}, DiscoveredCheck{Name: "service-exec", RequiresRunning: true, Reason: "Executes inside a running Compose service"})
			}
			if !reflect.DeepEqual(candidate.CheckDetails, want) {
				t.Fatalf("check prerequisites: %+v, want %+v", candidate.CheckDetails, want)
			}
			if len(candidate.Checks) != len(want) {
				t.Fatalf("legacy check names changed: %+v", candidate.Checks)
			}
			for i, check := range want {
				if candidate.Checks[i] != check.Name {
					t.Fatalf("legacy check names/order changed: %+v", candidate.Checks)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "SHOULD_NOT_EXIST")); !os.IsNotExist(err) {
				t.Fatalf("discovery executed project command: %v", err)
			}
		})
	}
}

func TestDiscoverProjectsReadOnlyCandidates(t *testing.T) {
	dir := nativeTestProject(t, "version: 1\ncommands: {build: {mode: task, host: {argv: [touch, SHOULD_NOT_EXIST]}}}\n")
	for path, content := range map[string]string{
		"apps/api/.fugato.yaml":                           "version: 1\ncommands: {broken: {mode: invalid}}\n",
		"apps/ui/package.json":                            `{"scripts":{"dev":"touch SHOULD_NOT_EXIST"}}`,
		"node_modules/dependency/.fugato.yaml":            "version: 1\n",
		"dist/.fugato.yaml":                               "version: 1\n",
		".vscode-test/extensions/sample/package.json":     "{}",
		".vscode-test-web/extensions/sample/package.json": "{}",
		".cache/download/package.json":                    "{}",
	} {
		target := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "apps"), filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	report, err := DiscoverProjects(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.Truncated || len(report.Projects) != 3 {
		t.Fatalf("unexpected discovery: %+v", report)
	}
	if report.Projects[0].Kind != "native" || len(report.Projects[0].Commands) != 1 {
		t.Fatalf("missing native config: %+v", report)
	}
	if !report.Projects[1].Configured || report.Projects[1].Error == "" {
		t.Fatalf("malformed config not visible: %+v", report)
	}
	if report.Projects[2].Kind != "manifest" || report.Projects[2].Configured {
		t.Fatalf("manifest became configured: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(dir, "SHOULD_NOT_EXIST")); !os.IsNotExist(err) {
		t.Fatal("discovery executed a command")
	}
}

func TestDiscoverProjectsRespectsGitExclusions(t *testing.T) {
	dir := nativeTestProject(t, "version: 1\n")
	files := map[string]string{
		".gitignore":                        "downloaded/\napps/*\n!apps/kept/\n",
		".git/info/exclude":                 "local-cache/\n",
		"downloaded/extension/package.json": "{}",
		"local-cache/package.json":          "{}",
		"apps/ignored/package.json":         "{}",
		"apps/kept/package.json":            "{}",
		"nested/.gitignore":                 "generated/\n",
		"nested/generated/package.json":     "{}",
		"nested/project/package.json":       "{}",
		"tracked/package.json":              "{}",
	}
	for name, content := range files {
		target := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", "add", "tracked/package.json")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("track project: %v: %s", err, output)
	}
	f, err := os.OpenFile(filepath.Join(dir, ".gitignore"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("tracked/\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := DiscoverProjects(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".", "apps/kept", "nested/project", "tracked"}
	if report.Truncated || len(report.Projects) != len(want) {
		t.Fatalf("unexpected discovery: %+v", report)
	}
	for i, name := range want {
		if report.Projects[i].ProjectDir != filepath.Join(dir, name) {
			t.Fatalf("project %d = %q, want %q", i, report.Projects[i].ProjectDir, filepath.Join(dir, name))
		}
	}
}

func TestDiscoverProjectsDoesNotInvokeFilesystemMonitor(t *testing.T) {
	dir := nativeTestProject(t, "version: 1\n")
	marker := filepath.Join(t.TempDir(), "monitor-ran")
	monitor := filepath.Join(t.TempDir(), "fsmonitor")
	script := "#!/bin/sh\n: > " + testShellQuote(marker) + "\nprintf 'token\\000'\n"
	if err := os.WriteFile(monitor, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", ".fugato.yaml"}, {"config", "core.fsmonitor", monitor}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	report, err := DiscoverProjects(t.Context(), dir)
	if err != nil || len(report.Projects) != 1 {
		t.Fatalf("discovery: %+v, %v", report, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("read-only discovery executed a configured filesystem monitor: %v", err)
	}
}

func TestDiscoverProjectsReportsProjectLimit(t *testing.T) {
	dir := nativeTestProject(t, "version: 1\n")
	for i := range 105 {
		path := filepath.Join(dir, string(rune('a'+i)))
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "package.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := DiscoverProjects(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Truncated || len(report.Projects) != 100 {
		t.Fatalf("expected bounded discovery, got %d, truncated=%v", len(report.Projects), report.Truncated)
	}
}

func TestDiscoverWorktreesWithoutManager(t *testing.T) {
	dir := nativeTestProject(t, "version: 1\n")
	result, err := DiscoverWorktrees(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Worktrees) != 1 || result.Worktrees[0].Path != dir {
		t.Fatalf("unexpected worktrees: %+v", result)
	}
}
