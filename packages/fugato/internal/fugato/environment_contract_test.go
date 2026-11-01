package fugato

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
	"github.com/fugatolabs/fugato/packages/fugato/internal/verify"
)

func TestEnvironmentNamedPreviewAndChecks(t *testing.T) {
	config := fmt.Sprintf(`version: 1
start: {commands: [server]}
commands:
  server:
    mode: service
    host:
      argv: [%q, '-test.run=^TestNativeProcessHelper$']
      env: {TEST_NATIVE_MODE: serve}
  failure:
    mode: task
    host:
      argv: [%q, '-test.run=^TestNativeProcessHelper$']
      env: {TEST_NATIVE_MODE: exit}
  endpoint:
    mode: task
    host:
      argv: [%q, '-test.run=^TestNativeProcessHelper$']
      env: {TEST_NATIVE_MODE: echo, VALUE: '${FUGATO_PORT_site}'}
endpoints:
  site: {command: server, port: 0, protocol: http, browser: true}
checks:
  ready: {http: {endpoint: site, path: /}}
  failure: {command: failure}
`, os.Args[0], os.Args[0], os.Args[0])
	dir := nativeTestProject(t, config)
	ctx := context.Background()
	state := t.TempDir()
	store, err := OpenStore(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := NewEnvironmentBackend(state)
	defer backend.Close(ctx)
	manager := NewManager(store, backend, 46656)
	runtime, err := manager.Up(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("http://site.%s.localhost:46656/", runtime.ID)
	if runtime.PreviewURL != want {
		t.Fatalf("preview=%s want=%s", runtime.PreviewURL, want)
	}
	request := httptest.NewRequest(http.MethodGet, want, nil)
	response := httptest.NewRecorder()
	proxyHandler(manager, 46656).ServeHTTP(response, request)
	if response.Code != 200 || response.Body.String() != "ready" {
		t.Fatalf("proxy=%d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, strings.Replace(want, "site.", "unknown.", 1), nil)
	response = httptest.NewRecorder()
	proxyHandler(manager, 46656).ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatalf("undeclared preview accepted: %d", response.Code)
	}
	check, err := manager.CheckNamed(ctx, dir, "", "ready")
	if err != nil || !check.OK || check.Status != 200 {
		t.Fatalf("check=%+v err=%v", check, err)
	}
	check, err = manager.CheckNamed(ctx, dir, "", "failure")
	if err != nil || check.OK || check.ExitCode != 7 {
		t.Fatalf("failed check=%+v err=%v", check, err)
	}
	command, err := manager.RunNamed(ctx, dir, "", "endpoint")
	bindings := backend.Snapshot(runtime.ID)
	if err != nil || command.Stdout != strconv.Itoa(bindings[0].HostPort) {
		t.Fatalf("cross-command endpoint binding=%+v err=%v", command, err)
	}
	service := ControlService{Manager: manager, Backend: backend, Verifier: verify.Verifier{}}
	result, err := service.VerifyHTTP(ctx, control.VerifyRequest{RuntimeID: runtime.ID, Endpoint: "site"})
	if err != nil || !result.OK {
		t.Fatalf("HTTP verification=%+v err=%v", result, err)
	}
	if _, err := service.VerifyHTTP(ctx, control.VerifyRequest{RuntimeID: runtime.ID, Endpoint: "unknown"}); err == nil {
		t.Fatal("unknown verification endpoint accepted")
	}
	project, err := ResolveProject(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".fugato.yaml"), []byte("malformed: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Down(ctx, runtime.ID); err != nil {
		t.Fatalf("cannot stop owned process after config edit: %v", err)
	}
	if _, err := backend.RunNamed(ctx, project, "endpoint", false, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "unavailable Fugato variable") {
		t.Fatalf("stopped endpoint variable accepted: %v", err)
	}
}

func TestComposeEnvironmentSelectionAndPorts(t *testing.T) {
	p := Project{Config: Config{Version: 1}}
	p.Config.Start.Services = []string{"api", "worker"}
	p.Config.Endpoints = map[string]EndpointConfig{"api": {Service: "api", Port: 8080, Protocol: "http"}}
	model := composeModel{Services: map[string]composeService{"api": {DependsOn: map[string]json.RawMessage{"db": nil}, Ports: []composePort{{Target: 8080}}}, "worker": {}, "db": {Ports: []composePort{{Target: 5432}}}}}
	selected, err := selectedServices(p, model)
	if err != nil || len(selected) != 3 {
		t.Fatalf("selection=%v err=%v", selected, err)
	}
	override, err := composePortOverride(p, model)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(override, "target: 8080") || !strings.Contains(override, "target: 5432") || !strings.Contains(override, "127.0.0.1") {
		t.Fatalf("override=%s", override)
	}
	p.Config.Endpoints = nil
	override, err = composePortOverride(p, model)
	if err != nil || !strings.Contains(override, "target: 5432") {
		t.Fatalf("no browser publication lost: %s %v", override, err)
	}
}
