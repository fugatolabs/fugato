package fugato

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestComposeStartTearsDownPartialLaunch(t *testing.T) {
	realDocker, err := exec.LookPath("docker")
	if err != nil || exec.Command("docker", "compose", "version").Run() != nil {
		t.Skip("Docker Compose CLI is unavailable")
	}
	for _, tc := range []struct {
		name      string
		mode      string
		wantError string
		wantCalls string
	}{
		{name: "Compose up fails", mode: "up", wantError: "start Compose project", wantCalls: "up\ndown\n"},
		{name: "startup does not infer HTTP health", mode: "health", wantCalls: "up\n"},
		{name: "cancelled start still tears down", mode: "cancel", wantError: "start Compose project", wantCalls: "up\ndown\n"},
		{name: "teardown failure is reported", mode: "cleanup", wantError: "clean up failed Compose start", wantCalls: "up\ndown\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if output, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v: %s", err, output)
			}
			compose := "services:\n  web:\n    image: busybox:latest\n    ports: ['127.0.0.1::3000']\n"
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0600); err != nil {
				t.Fatal(err)
			}
			wrapperDir := t.TempDir()
			calls := filepath.Join(wrapperDir, "calls")
			wrapper := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    up)
      printf 'up\n' >> %s
      if [ %s = health ]; then exit 0; fi
      if [ %s = cancel ]; then sleep 10; exit 0; fi
      exit 23 ;;
    port)
      printf 'port\n' >> %s
      printf '127.0.0.1:1\n'
      exit 0 ;;
    down)
      printf 'down\n' >> %s
      if [ %s = cleanup ]; then exit 24; fi
      exit 0 ;;
  esac
done
exec %s "$@"
`, testShellQuote(calls), testShellQuote(tc.mode), testShellQuote(tc.mode), testShellQuote(calls), testShellQuote(calls), testShellQuote(tc.mode), testShellQuote(realDocker))
			if err := os.WriteFile(filepath.Join(wrapperDir, "docker"), []byte(wrapper), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			project, err := ResolveProject(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.mode == "cancel" {
				ctx, cancel := context.WithCancel(context.Background())
				result := make(chan error, 1)
				go func() {
					startErr := (ComposeBackend{}).Start(ctx, project)
					result <- startErr
				}()
				deadline := time.After(5 * time.Second)
				for {
					data, _ := os.ReadFile(calls)
					if strings.Contains(string(data), "up\n") {
						break
					}
					select {
					case <-deadline:
						cancel()
						t.Fatal("Compose up was not invoked")
					case <-time.After(10 * time.Millisecond):
					}
				}
				cancel()
				select {
				case err = <-result:
				case <-time.After(5 * time.Second):
					t.Fatal("cancelled start did not return after cleanup")
				}
			} else {
				err = (ComposeBackend{}).Start(context.Background(), project)
			}
			if (tc.wantError == "" && err != nil) || (tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError))) {
				t.Fatalf("want error containing %q, got %v", tc.wantError, err)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.wantCalls {
				t.Fatalf("Compose calls: got %q, want %q", data, tc.wantCalls)
			}
		})
	}
}

func testShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
