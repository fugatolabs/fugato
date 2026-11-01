package fugato

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveProjectRejectsUnsafeConfigPaths(t *testing.T) {
	dir := testProject(t)
	if _, err := ResolveProject(context.Background(), dir); err != nil {
		t.Fatalf("valid project: %v", err)
	}
	config := filepath.Join(dir, ".fugato.yaml")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	unsafe := []byte("version: 1\nstart: {services: [web]}\nendpoints: {web: {service: web, port: 3000, protocol: http}}\nchecks: {ready: {http: {endpoint: web, path: //outside.example/health}}}\n")
	if err := os.WriteFile(config, unsafe, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveProject(context.Background(), dir); err == nil {
		t.Fatal("unsafe health URL was accepted")
	}
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, append(append([]byte{}, data...), []byte("---\nversion: 1\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveProject(context.Background(), dir); err == nil {
		t.Fatal("multiple YAML documents were accepted")
	}
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(outside, []byte("services: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "compose.yaml")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveProject(context.Background(), dir); err == nil {
		t.Fatal("Compose symlink outside project was accepted")
	}
}

func TestDiscoverProjectWithoutConfigKeepsIDWhenConfigIsAdded(t *testing.T) {
	root := t.TempDir()
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "apps", "web", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "apps", "web", "compose.yaml"), []byte("services:\n  web:\n    image: caddy:2-alpine\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(context.Background(), root, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "apps", "web", "nested")
	first, err := DiscoverProject(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Dir != filepath.Join(root, "apps", "web") || first.ConfigFile != "" {
		t.Fatalf("wrong no-config discovery: %+v", first)
	}
	if err := os.WriteFile(filepath.Join(first.Dir, ".fugato.yaml"), []byte("version: 1\nstart: {services: [web]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := DiscoverProject(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.ComposeProject != first.ComposeProject || second.ConfigFile == "" {
		t.Fatalf("config changed runtime identity: before=%+v after=%+v", first, second)
	}
}
