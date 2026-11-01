package fugato

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
	"github.com/fugatolabs/fugato/packages/fugato/internal/verify"
)

func TestBrowserValidatesCurrentComposeBeforeContainerRun(t *testing.T) {
	for _, tc := range []struct {
		name, browser, message string
	}{
		{"privileged", `{"profiles":["test"],"privileged":true}`, "privilege settings"},
		{"outside bind mount", `{"profiles":["test"],"volumes":[{"type":"bind","source":"/"}]}`, "mounts outside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := testProject(t)
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			config := "version: 1\ncompose: compose.yaml\nstart: {services: [web]}\nendpoints: {site: {service: web, port: 3000, protocol: http, browser: true}}\nbrowser:\n  service: browser\n  profile: test\n"
			if err := os.WriteFile(filepath.Join(dir, ".fugato.yaml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			// Discover the identity without invoking Docker; the runner must read
			// and validate the current model, even for an existing runtime.
			p, err := DiscoverProject(context.Background(), dir, "")
			if err != nil {
				t.Fatal(err)
			}
			fakeDir := t.TempDir()
			modelPath := filepath.Join(fakeDir, "model.json")
			marker := filepath.Join(fakeDir, "container-run")
			model := `{"services":{"web":{"ports":[{"host_ip":"127.0.0.1","target":3000,"protocol":"tcp"}]},"browser":` + tc.browser + `}}`
			if !json.Valid([]byte(model)) {
				t.Fatal("invalid test model")
			}
			if err := os.WriteFile(modelPath, []byte(model), 0600); err != nil {
				t.Fatal(err)
			}
			// This fake only serves configuration and records an attempted run;
			// no Docker daemon or real container is involved.
			script := "#!/bin/sh\nfor arg in \"$@\"; do\n case \"$arg\" in\n config) cat '" + modelPath + "'; exit 0 ;;\n run) touch '" + marker + "'; exit 1 ;;\n esac\ndone\nexit 1\n"
			if err := os.WriteFile(filepath.Join(fakeDir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err = (ComposeBrowserRunner{}).Run(ctx, verify.Target{ProjectDir: dir, Runtime: control.Runtime{ID: p.ID}}, "http://web:3000")
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("got %v, want unsafe Compose rejection containing %q", err, tc.message)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("unsafe model reached container run: %v", err)
			}
		})
	}
}

func TestBrowserRequiresDeadlineBeforeResolvingProject(t *testing.T) {
	_, err := (ComposeBrowserRunner{}).Run(context.Background(), verify.Target{ProjectDir: "/does-not-exist"}, "http://web:3000")
	if err == nil || !strings.Contains(err.Error(), "requires a deadline") {
		t.Fatalf("got %v, want deadline rejection before project work", err)
	}
}
