package fugato

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeProcessHelper(t *testing.T) {
	mode := os.Getenv("TEST_NATIVE_MODE")
	if mode == "" {
		return
	}
	switch mode {
	case "exit":
		fmt.Print("task output")
		os.Exit(7)
	case "echo":
		fmt.Print(os.Getenv("VALUE"))
		os.Exit(0)
	case "sleep":
		for {
			time.Sleep(time.Hour)
		}
	case "service-log":
		fmt.Println(os.Getenv("VALUE"))
		for {
			time.Sleep(time.Hour)
		}
	case "serve":
		listener, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("FUGATO_PORT_site"))
		if err != nil {
			os.Exit(2)
		}
		_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ready") }))
		os.Exit(0)
	case "fork":
		child := exec.Command(os.Args[0], "-test.run=^TestNativeProcessHelper$")
		child.Env = append(os.Environ(), "TEST_NATIVE_MODE=serve")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("CHILD_PID"), []byte(fmt.Sprint(child.Process.Pid)), 0600); err != nil {
			os.Exit(3)
		}
		time.Sleep(200 * time.Millisecond)
		os.Exit(0)
	}
	os.Exit(0)
}
func nativeRuntimeProject(t *testing.T, mode string) Project {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := Project{ID: "aaaaaaaaaaaaaaaa", Dir: dir, Worktree: dir, Config: Config{Version: 1}}
	p.Config.Commands = map[string]CommandConfig{"server": {Mode: "service", Host: &HostCommand{Argv: []string{os.Args[0], "-test.run=^TestNativeProcessHelper$"}, Env: map[string]string{"TEST_NATIVE_MODE": mode}}}}
	p.Config.Start.Commands = []string{"server"}
	p.Config.Endpoints = map[string]EndpointConfig{"site": {Command: "server", Protocol: "http", Port: 0, Browser: true}}
	return p
}
func TestNativeRuntimeOwnsPortsAndShutdown(t *testing.T) {
	p := nativeRuntimeProject(t, "serve")
	b := NewEnvironmentBackend(t.TempDir())
	defer b.Close(context.Background())
	if err := b.Start(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	ports, err := b.Ports(context.Background(), p)
	if err != nil || len(ports) != 1 {
		t.Fatalf("ports %+v: %v", ports, err)
	}
	response, err := http.Get(ports[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if running, err := b.Inspect(context.Background(), p); err != nil || !running {
		t.Fatalf("running=%v err=%v", running, err)
	}
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if running, _ := b.Inspect(context.Background(), p); running {
		t.Fatal("stopped child is reported running")
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", ports[0].HostPort), 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("native endpoint survives shutdown")
	}
	// A new daemon has no authority over persisted PIDs and reports no native owner.
	restarted := NewEnvironmentBackend(t.TempDir())
	if running, err := restarted.Inspect(context.Background(), p); err != nil || running {
		t.Fatalf("restart claims process: running=%v err=%v", running, err)
	}
}
func TestNativeLeaderExitCleansDescendants(t *testing.T) {
	p := nativeRuntimeProject(t, "fork")
	p.Config.Commands["server"].Host.Env["CHILD_PID"] = filepath.Join(p.Dir, "child.pid")
	b := NewEnvironmentBackend(t.TempDir())
	defer b.Close(context.Background())
	if err := b.Start(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	ports := b.Snapshot(p.ID)
	process := b.environment(p.ID).processes["server"]
	select {
	case <-process.done:
	case <-time.After(5 * time.Second):
		t.Fatal("leader was not reaped")
	}
	if _, err := os.Stat(filepath.Join(p.Dir, "child.pid")); err != nil {
		t.Fatal("helper never forked")
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", ports[0].HostPort), 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("orphan descendant survived leader exit")
	}
}
func TestNativeTaskExitCancellationAndLiteralArguments(t *testing.T) {
	p := nativeRuntimeProject(t, "serve")
	p.Config.Start.Commands = nil
	b := NewEnvironmentBackend(t.TempDir())
	p.Config.Commands["task"] = CommandConfig{Mode: "task", Host: &HostCommand{Argv: []string{os.Args[0], "-test.run=^TestNativeProcessHelper$"}, Env: map[string]string{"TEST_NATIVE_MODE": "exit"}}}
	result, err := b.RunNamed(context.Background(), p, "task", false, nil, nil, nil)
	if err != nil || result.ExitCode != 7 || result.Stdout != "task output" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	p.Config.Commands["task"].Host.Env = map[string]string{"TEST_NATIVE_MODE": "echo", "VALUE": "literal $HOME $1 $$ ${FUGATO_RUNTIME_ID}"}
	result, err = b.RunNamed(context.Background(), p, "task", false, nil, nil, nil)
	if err != nil || result.Stdout != "literal $HOME $1 $$ "+p.ID {
		t.Fatalf("literal expansion: %+v %v", result, err)
	}
	p.Config.Commands["task"].Host.Env = map[string]string{"TEST_NATIVE_MODE": "sleep"}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = b.RunNamed(ctx, p, "task", false, nil, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled task error=%v", err)
	}
}
func TestNativeStartupFailureRollsBackAndRetainsData(t *testing.T) {
	p := nativeRuntimeProject(t, "serve")
	p.Config.Start.Commands = append(p.Config.Start.Commands, "fail")
	p.Config.Commands["fail"] = CommandConfig{Mode: "task", Host: &HostCommand{Argv: []string{os.Args[0], "-test.run=^TestNativeProcessHelper$"}, Env: map[string]string{"TEST_NATIVE_MODE": "exit"}}}
	dir := t.TempDir()
	b := NewEnvironmentBackend(dir)
	defer b.Close(context.Background())
	if err := b.Start(context.Background(), p); err == nil || !strings.Contains(err.Error(), "status 7") {
		t.Fatalf("failure=%v", err)
	}
	if running, _ := b.Inspect(context.Background(), p); running {
		t.Fatal("failed startup left process running")
	}
	if _, err := os.Stat(filepath.Join(dir, "data", p.ID)); err != nil {
		t.Fatal("startup rollback removed data directory")
	}
}
func TestNativeLogTailBounded(t *testing.T) {
	log := &nativeLog{text: boundedWriter{max: 16}}
	_, _ = io.WriteString(log, "first log line\nsecond log line\n")
	value, _ := log.snapshot()
	if len(value) != 16 || !strings.HasSuffix(value, "second log line\n") {
		t.Fatalf("tail=%q", value)
	}
}

func TestNativeServiceLogsAreIsolatedAndRetainedAfterStop(t *testing.T) {
	p := nativeRuntimeProject(t, "service-log")
	p.Config.Endpoints = map[string]EndpointConfig{}
	p.Config.Commands["server"].Host.Env["VALUE"] = "server-only"
	p.Config.Commands["worker"] = CommandConfig{Mode: "service", Host: &HostCommand{
		Argv: []string{os.Args[0], "-test.run=^TestNativeProcessHelper$"},
		Env:  map[string]string{"TEST_NATIVE_MODE": "service-log", "VALUE": "worker-only"},
	}}
	p.Config.Start.Commands = []string{"server", "worker"}
	b := NewEnvironmentBackend(t.TempDir())
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	if err := b.Start(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if err := b.Stop(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"server", "worker"} {
		t.Run(name, func(t *testing.T) {
			var output strings.Builder
			if err := b.LogsService(t.Context(), p, name, "", &output); err != nil {
				t.Fatal(err)
			}
			if output.String() != name+"-only\n" {
				t.Fatalf("service log = %q", output.String())
			}
		})
	}
	var combined strings.Builder
	if err := b.Logs(t.Context(), p, false, &combined); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(combined.String(), "server-only") || !strings.Contains(combined.String(), "worker-only") {
		t.Fatalf("aggregate log = %q", combined.String())
	}
	if err := b.LogsService(t.Context(), p, "--invalid", "", io.Discard); err == nil {
		t.Fatal("accepted invalid service name")
	}
	if err := b.LogsService(t.Context(), p, "unknown", "", io.Discard); err == nil {
		t.Fatal("accepted unknown native service")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := b.LogsService(ctx, p, "server", "", io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled service log = %v", err)
	}
}

func TestHostExecutableUsesConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := hostExecutable("tool", dir, dir)
	if err != nil || got != path {
		t.Fatalf("lookup=%q err=%v", got, err)
	}
	if _, err := hostExecutable("tool", dir, ""); err == nil {
		t.Fatal("empty PATH searched implicit working directory")
	}
	got, err = hostExecutable("./tool", dir, "")
	if err != nil || got != path {
		t.Fatalf("explicit relative executable=%q err=%v", got, err)
	}
}
