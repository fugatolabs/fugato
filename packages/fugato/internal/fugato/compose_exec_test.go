package fugato

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

func TestComposeExecUsesSelectedServiceAndTimeout(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\ncase \"$*\" in\n  *config*) printf '%s\\n' '{\"services\":{\"web\":{\"ports\":[{\"host_ip\":\"127.0.0.1\",\"target\":3000,\"protocol\":\"tcp\"}]}}}' ;;\n  *) printf '%s\\n' \"$@\" > \"" + argsFile + "\"; printf 'v20\\n' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	p := Project{ID: "0123456789abcdef", Dir: dir, ComposeFile: filepath.Join(dir, "compose.yaml"), ComposeProject: "fugato-0123456789abcdef"}
	p.Config.Start.Services = []string{"web"}
	p.Config.Endpoints = map[string]EndpointConfig{"site": {Service: "web", Port: 3000, Protocol: "http", Browser: true}}
	input := control.CommandRequest{RuntimeID: p.ID, Service: "web", Argv: []string{"node", "--version"}, TimeoutMS: 1200}
	result, err := (ComposeBackend{}).RunCommand(context.Background(), p, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Stdout != "v20\n" {
		t.Fatalf("unexpected result: %+v", result)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "exec\n-T\nweb\ntimeout\n-s\nKILL\n2s\nnode\n--version\n") {
		t.Fatalf("wrong Docker args: %s", args)
	}
	input.Service = "db"
	if _, err := (ComposeBackend{}).RunCommand(context.Background(), p, input); err == nil {
		t.Fatal("database service accepted")
	}
}
