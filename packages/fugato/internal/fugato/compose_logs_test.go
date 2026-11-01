package fugato

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func composeLogsFixture(t *testing.T, privileged bool) (Project, string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "logs-args")
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    config) printf '%%s\n' '{"services":{"web":{"privileged":%t}}}'; exit 0 ;;
    logs) printf '%%s\n' "$@" > %s; printf 'compose-web\n'; exit 0 ;;
    up|run|start|exec|down) exit 97 ;;
  esac
done
exit 98
`, privileged, testShellQuote(argsFile))
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	p := Project{ID: "0123456789abcdef", Dir: dir, ComposeFile: filepath.Join(dir, "compose.yaml"),
		ComposeProject: "fugato-0123456789abcdef", Config: Config{Version: 1, Start: StartConfig{Services: []string{"web"}}}}
	return p, argsFile
}

func TestComposeServiceLogsValidateAndScopeSelection(t *testing.T) {
	p, argsFile := composeLogsFixture(t, false)
	backend := ComposeBackend{}
	for _, tc := range []struct {
		service, kind string
	}{
		{"--follow", ""}, {"unknown", "compose"}, {"web", "native"}, {"web", "invalid"},
	} {
		if err := backend.LogsService(t.Context(), p, tc.service, tc.kind, io.Discard); err == nil {
			t.Fatalf("accepted invalid selector %+v", tc)
		}
		if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
			t.Fatalf("invalid selection reached logs subprocess: %v", err)
		}
	}
	var output strings.Builder
	if err := backend.LogsService(t.Context(), p, "web", "compose", &output); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(args), "logs\n--no-color\n--tail\n200\n--\nweb\n") || output.String() != "compose-web\n" {
		t.Fatalf("wrong scoped logs: args=%q output=%q", args, output.String())
	}
}

func TestComposeServiceLogsPreserveEffectiveModelValidation(t *testing.T) {
	p, argsFile := composeLogsFixture(t, true)
	err := (ComposeBackend{}).LogsService(t.Context(), p, "web", "compose", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "privilege settings") {
		t.Fatalf("unsafe model was accepted: %v", err)
	}
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Fatalf("unsafe model reached logs subprocess: %v", err)
	}
}

func TestMixedServiceLogsDisambiguateNativeAndComposeNames(t *testing.T) {
	p, _ := composeLogsFixture(t, false)
	p.Config.Commands = map[string]CommandConfig{"web": {Mode: "service", Host: &HostCommand{Argv: []string{"git", "status"}}}}
	logs := &nativeLog{text: boundedWriter{max: 64 << 10}}
	_, _ = logs.Write([]byte("native-web\n"))
	backend := NewEnvironmentBackend(t.TempDir())
	backend.environments[p.ID] = &nativeEnvironment{processes: map[string]*nativeProcess{"web": {logs: logs}}}
	if err := backend.LogsService(t.Context(), p, "web", "", io.Discard); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous service selection = %v", err)
	}
	for _, kind := range []string{"native", "compose"} {
		var output strings.Builder
		if err := backend.LogsService(t.Context(), p, "web", kind, &output); err != nil {
			t.Fatal(err)
		}
		if output.String() != kind+"-web\n" {
			t.Fatalf("%s service output = %q", kind, output.String())
		}
	}
}
