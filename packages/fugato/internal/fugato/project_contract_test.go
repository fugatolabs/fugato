package fugato

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// These fixtures render Compose configuration without starting containers.
func TestProjectContractComposeFixtures(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("Docker Compose CLI is unavailable")
	}
	for _, tc := range []struct {
		name, compose, config, wantError string
		services                         []string
		ports                            []int
	}{
		{name: "published service", compose: "services:\n  web:\n    image: busybox:latest\n    ports: ['3000:3000']\n", services: []string{"web"}, ports: []int{3000}},
		{name: "exposed service has no browser inference", compose: "services:\n  web:\n    image: busybox:latest\n    expose: ['3000']\n", services: []string{"web"}},
		{name: "multiple services", compose: "services:\n  web:\n    image: busybox:latest\n    ports: ['3000:3000']\n  db:\n    image: busybox:latest\n    ports: ['5432:5432']\n", services: []string{"db", "web"}, ports: []int{3000}},
		{name: "profiled service excluded", compose: "services:\n  web:\n    image: busybox:latest\n  browser:\n    image: busybox:latest\n    profiles: [test]\n", services: []string{"web"}},
		{name: "worker without ports", compose: "services:\n  worker:\n    image: busybox:latest\n", services: []string{"worker"}},
		{name: "explicit empty start", compose: "services:\n  web:\n    image: busybox:latest\n", config: "version: 1\nstart: {}\n", services: []string{}},
		{name: "explicit root with dependencies", compose: "services:\n  web:\n    image: busybox:latest\n    ports: ['3000:3000']\n    depends_on: [db]\n  db:\n    image: busybox:latest\n", config: "version: 1\nstart: {services: [web]}\n", services: []string{"web"}, ports: []int{3000}},
		{name: "host networking rejected", compose: "services:\n  web:\n    image: busybox:latest\n    network_mode: host\n", wantError: "network_mode"},
		{name: "external mutable volume rejected", compose: "services:\n  web:\n    image: busybox:latest\n    volumes: ['shared:/data']\nvolumes:\n  shared:\n    external: true\n", wantError: "external or has a shared explicit name"},
		{name: "duplicate target rejected", compose: "services:\n  web:\n    image: busybox:latest\n    ports: ['3000:3000', '3001:3000']\n", wantError: "more than once"},
		{name: "matching range remapped", compose: "services:\n  web:\n    image: busybox:latest\n    ports: ['3000-3001:3000-3001']\n", services: []string{"web"}, ports: []int{3000, 3001}},
		{name: "UDP publication remapped", compose: "services:\n  web:\n    image: busybox:latest\n    ports: ['3000:3000', '5353:5353/udp']\n", services: []string{"web"}, ports: []int{3000, 5353}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := gitOutput(context.Background(), dir, "init", "-q"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(tc.compose), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.config != "" {
				if err := os.WriteFile(filepath.Join(dir, ".fugato.yaml"), []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			project, err := ResolveProject(context.Background(), dir)
			if err == nil {
				project, err = (ComposeBackend{}).Prepare(context.Background(), project)
			}
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("wanted %q, got %v", tc.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(project.Config.Start.Services, tc.services) || len(project.Config.Endpoints) != 0 {
				t.Fatalf("unexpected startup or browser inference: %+v", project.Config)
			}
			output, err := composeOutput(context.Background(), project, "--profile", "*", "config", "--format", "json")
			if err != nil {
				t.Fatal(err)
			}
			var effective composeModel
			if err := json.Unmarshal(output, &effective); err != nil {
				t.Fatal(err)
			}
			for _, target := range tc.ports {
				found := false
				for _, port := range effective.Services["web"].Ports {
					if port.Target != target {
						continue
					}
					found = true
					if port.HostIP != "127.0.0.1" || len(port.Published) > 0 && string(port.Published) != `""` {
						t.Fatalf("port not dynamic loopback: %+v", port)
					}
				}
				if !found {
					t.Fatalf("missing port %d", target)
				}
			}
		})
	}
}

func TestProjectContractInitAlwaysWritesSelectedRoots(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("Docker Compose CLI is unavailable")
	}
	for _, tc := range []struct {
		name     string
		choices  InitChoices
		services []string
	}{
		{name: "selected roots", choices: InitChoices{Services: []string{"web"}}, services: []string{"web"}},
		{name: "all default roots", choices: InitChoices{}, services: []string{"db", "web"}},
		{name: "explicit empty roots", choices: InitChoices{Services: []string{}}, services: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := gitOutput(context.Background(), dir, "init", "-q"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    image: busybox:latest\n  db:\n    image: busybox:latest\n"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := DiscoverProject(context.Background(), dir, "")
			if err != nil {
				t.Fatal(err)
			}
			project, wrote, err := Init(context.Background(), dir, tc.choices)
			if err != nil || !wrote {
				t.Fatalf("init wrote=%v error=%v", wrote, err)
			}
			data, err := os.ReadFile(project.ConfigFile)
			if err != nil {
				t.Fatal(err)
			}
			config, err := parseProjectConfig(data)
			if err != nil {
				t.Fatal(err)
			}
			if project.ID != before.ID || !slices.Equal(config.Start.Services, tc.services) || len(config.Endpoints) != 0 {
				t.Fatalf("unexpected initialized config: %+v", config)
			}
			resolved, err := ResolveProject(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(resolved.Config.Start.Services, tc.services) {
				t.Fatalf("explicit configuration changed startup roots: %+v", resolved.Config.Start)
			}
		})
	}
}

func TestInitNativeProjectScaffold(t *testing.T) {
	dir := t.TempDir()
	if _, err := gitOutput(context.Background(), dir, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	project, wrote, err := Init(context.Background(), dir, InitChoices{})
	if err != nil || !wrote {
		t.Fatalf("init wrote=%v error=%v", wrote, err)
	}
	data, err := os.ReadFile(project.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "version: 1\n") || !strings.Contains(string(data), "fugato example native") || !strings.Contains(string(data), "#   commands: [web]") {
		t.Fatalf("unexpected native scaffold: %s", data)
	}
	if config, err := parseProjectConfig(data); err != nil || len(config.Start.Commands) != 0 {
		t.Fatalf("native scaffold must have no selected startup work: %+v %v", config, err)
	}
	if _, _, err := Init(context.Background(), dir, InitChoices{}); err == nil {
		t.Fatal("init overwrote existing configuration")
	}
}

func TestProjectContractSiblingMonorepoPaths(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("Docker Compose CLI is unavailable")
	}
	root := t.TempDir()
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	projectDir := filepath.Join(root, "apps", "web")
	sourceDir := filepath.Join(root, "apps", "src")
	for _, dir := range []string{projectDir, sourceDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "Dockerfile"), []byte("FROM busybox:latest\n"), 0600); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  web:\n    build: ../src\n    expose: ['3000']\n    volumes: ['../src:/app/src']\n"
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yaml"), []byte(compose), 0600); err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(context.Background(), projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if project.Worktree != root || project.Dir != projectDir {
		t.Fatalf("wrong monorepo scope: %+v", project)
	}
	if _, err := (ComposeBackend{}).Prepare(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yaml"), []byte(fmt.Sprintf("services:\n  web:\n    image: busybox:latest\n    expose: ['3000']\n    volumes: [%q]\n", outside+":/app/src")), 0600); err != nil {
		t.Fatal(err)
	}
	project, err = ResolveProject(context.Background(), projectDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (ComposeBackend{}).Prepare(context.Background(), project)
	if err == nil || !strings.Contains(err.Error(), "outside the project directory") {
		t.Fatalf("outside-worktree bind accepted: %v", err)
	}
}
