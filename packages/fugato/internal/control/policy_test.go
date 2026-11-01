package control

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCommandPolicyIsRuntimeScopedAndDefaultsToDeny(t *testing.T) {
	dir := t.TempDir()
	first := "0123456789abcdef"
	second := "fedcba9876543210"
	policy, err := LoadCommandPolicy(dir, first)
	if err != nil {
		t.Fatal(err)
	}
	request := CommandRequest{RuntimeID: first, Service: "web", Argv: []string{"node", "--version"}, TimeoutMS: 1000}
	if err := policy.Validate(request); err == nil {
		t.Fatal("missing policy allowed command")
	}
	data := []byte(`{"version":1,"runtimes":{"0123456789abcdef":{"web":[["node","--version"]]}}}`)
	path := filepath.Join(dir, "command-policy.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	policy, err = LoadCommandPolicy(dir, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.Validate(request); err != nil {
		t.Fatal(err)
	}
	policy, err = LoadCommandPolicy(dir, second)
	if err != nil {
		t.Fatal(err)
	}
	request.RuntimeID = second
	if err := policy.Validate(request); err == nil {
		t.Fatal("another runtime inherited the policy")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCommandPolicy(dir, first); err == nil {
		t.Fatal("world-readable policy accepted")
	}
}
