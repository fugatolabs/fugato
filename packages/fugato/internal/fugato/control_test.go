package fugato

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

func TestBoundedWriterKeepsRecentOutput(t *testing.T) {
	w := &boundedWriter{max: 5}
	_, _ = w.Write([]byte("abc"))
	_, _ = w.Write([]byte("def"))
	if got := w.String(); got != "bcdef" || !w.truncated {
		t.Fatalf("got %q, truncated=%v", got, w.truncated)
	}
	_, _ = w.Write([]byte("uvwxyz"))
	if got := w.String(); got != "vwxyz" {
		t.Fatalf("got %q", got)
	}
}

func TestControlServiceLogsSelectsNativeService(t *testing.T) {
	dir := nativeTestProject(t, "version: 1\ncommands:\n  web: {mode: service, host: {argv: [git, status]}}\n  worker: {mode: service, host: {argv: [git, status]}}\n")
	store, err := OpenStore(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	backend := NewEnvironmentBackend(t.TempDir())
	manager := NewManager(store, backend, 46656)
	runtime, err := manager.Create(t.Context(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	web := &nativeLog{text: boundedWriter{max: 64 << 10}}
	worker := &nativeLog{text: boundedWriter{max: 64 << 10}}
	_, _ = web.Write([]byte("web first\nweb last\n"))
	_, _ = worker.Write([]byte("worker secret\n"))
	backend.environments[runtime.ID] = &nativeEnvironment{processes: map[string]*nativeProcess{
		"web": {logs: web}, "worker": {logs: worker},
	}}
	service := ControlService{Manager: manager}
	result, err := service.Logs(t.Context(), control.LogsRequest{RuntimeID: runtime.ID, Service: "web", ServiceKind: "native", Lines: 1})
	if err != nil || result.Service != "web" || result.ServiceKind != "native" || result.Text != "web last\n" || !result.Truncated {
		t.Fatalf("selected service logs = %+v, %v", result, err)
	}
	_, _ = web.Write([]byte(strings.Repeat("x", 80<<10)))
	result, err = service.Logs(t.Context(), control.LogsRequest{RuntimeID: runtime.ID, Service: "web", ServiceKind: "native", Lines: 200})
	if err != nil || len(result.Text) != 64<<10 || !result.Truncated {
		t.Fatalf("retained native service tail lost truncation evidence: bytes=%d truncated=%v err=%v", len(result.Text), result.Truncated, err)
	}
	for _, input := range []control.LogsRequest{
		{RuntimeID: runtime.ID, ServiceKind: "native", Lines: 200},
		{RuntimeID: runtime.ID, Service: "web", ServiceKind: "other", Lines: 200},
	} {
		if _, err := service.Logs(t.Context(), input); err == nil {
			t.Fatalf("accepted invalid service_kind: %+v", input)
		}
	}
	for _, name := range []string{"--invalid", "unknown"} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Logs(t.Context(), control.LogsRequest{RuntimeID: runtime.ID, Service: name, Lines: 200}); err == nil {
				t.Fatal("accepted invalid or unknown log service")
			}
		})
	}
	if len(manager.admission.logs.active) != 0 {
		t.Fatal("service logs leaked admission capacity")
	}
}

func TestControlRejectsOverloadBeforeResolution(t *testing.T) {
	// A nil store/backend makes any accidental entry into resolution fail;
	// saturated operations must reject before touching either dependency.
	m := NewManager(nil, nil, 46656)
	s := ControlService{Manager: m, PolicyForRuntime: func(string) (control.CommandPolicy, error) {
		t.Fatal("overloaded command reached policy loading")
		return control.CommandPolicy{}, nil
	}}
	for _, tc := range []struct {
		name string
		gate *operationGate
		call func() error
	}{
		{"http verification", m.admission.verification, func() error { _, err := s.VerifyHTTP(context.Background(), control.VerifyRequest{}); return err }},
		{"browser verification", m.admission.verification, func() error { _, err := s.VerifyBrowser(context.Background(), control.VerifyRequest{}); return err }},
		{"command", m.admission.commands, func() error { _, err := s.RunCommand(context.Background(), control.CommandRequest{}); return err }},
		{"get metadata", m.admission.metadata, func() error { _, err := s.Get(context.Background(), "0123456789abcdef"); return err }},
		{"start metadata", m.admission.metadata, func() error { _, err := s.Start(context.Background(), "0123456789abcdef"); return err }},
		{"stop metadata", m.admission.metadata, func() error { _, err := s.Stop(context.Background(), "0123456789abcdef"); return err }},
		{"prepare metadata", m.admission.metadata, func() error {
			_, err := s.Prepare(context.Background(), control.PrepareRequest{Path: "/target", From: "/source"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for range cap(tc.gate.active) {
				release, err := tc.gate.acquire(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(release)
			}
			if err := tc.call(); !errors.Is(err, control.ErrBusy) {
				t.Fatalf("got %v, want busy before resolution", err)
			}
		})
	}
}

func TestControlReturnsVerificationCapacityOnValidationError(t *testing.T) {
	m := NewManager(nil, nil, 46656)
	s := ControlService{Manager: m}
	for range 5 {
		_, err := s.VerifyBrowser(context.Background(), control.VerifyRequest{RuntimeID: "invalid"})
		if err == nil || errors.Is(err, control.ErrBusy) {
			t.Fatalf("capacity leaked after validation failure: %v", err)
		}
	}
}

type blockingCommandBackend struct {
	fakeBackend
	entered chan struct{}
	finish  chan struct{}
}

var errCommandTest = errors.New("test command failed")

func (b *blockingCommandBackend) RunCommand(ctx context.Context, _ Project, _ control.CommandRequest) (control.CommandResult, error) {
	b.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return control.CommandResult{}, ctx.Err()
	case <-b.finish:
		return control.CommandResult{}, errCommandTest
	}
}

func TestControlCommandAdmissionReleasesOnCancellationAndBackendError(t *testing.T) {
	dir := testProject(t)
	cmd := exec.Command("git", "-C", dir, "-c", "user.name=Fugato Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit fixture: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := ResolveProject(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r := Runtime{ID: p.ID, ProjectDir: p.Dir, Worktree: p.Worktree, Repository: p.Repository, ComposeProject: p.ComposeProject, Backend: "compose", Desired: "running", Observed: "running"}
	if err := store.Save(ctx, &r); err != nil {
		t.Fatal(err)
	}
	backend := &blockingCommandBackend{entered: make(chan struct{}, 16), finish: make(chan struct{})}
	s := ControlService{Manager: NewManager(store, backend, 46656), Backend: backend, Policy: control.CommandPolicy{Commands: map[string][][]string{"web": {{"node", "--version"}}}}}
	input := control.CommandRequest{RuntimeID: p.ID, Service: "web", Argv: []string{"node", "--version"}, TimeoutMS: 5000}
	activeCtx, cancelActive := context.WithCancel(ctx)
	defer cancelActive()
	results := make(chan error, 4)
	for range 4 {
		go func() { _, err := s.RunCommand(activeCtx, input); results <- err }()
		select {
		case <-backend.entered:
		case err := <-results:
			t.Fatalf("command failed before reaching backend: %v", err)
		case <-ctx.Done():
			t.Fatal("command did not reach backend")
		}
	}
	if _, err := s.RunCommand(ctx, input); !errors.Is(err, control.ErrBusy) {
		t.Fatalf("fifth command got %v, want overload", err)
	}
	select {
	case <-backend.entered:
		t.Fatal("rejected command reached backend")
	default:
	}
	cancelActive()
	for range 4 {
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled command returned %v", err)
			}
		case <-ctx.Done():
			t.Fatal("cancelled command did not finish")
		}
	}
	close(backend.finish)
	// More than one full budget of failing commands must remain admissible.
	for range 6 {
		if _, err := s.RunCommand(ctx, input); !errors.Is(err, errCommandTest) {
			t.Fatalf("capacity was not released after cancellation/backend failure: %v", err)
		}
	}
}

func TestControlPreparationBeforeRegistrationAndReleasesCapacity(t *testing.T) {
	source := testProject(t)
	config := "version: 1\nprepare:\n  files:\n    - path: .env.local\n"
	if err := os.WriteFile(filepath.Join(source, ".fugato.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	for _, args := range [][]string{
		{"-C", source, "add", ".fugato.yaml"},
		{"-C", source, "-c", "user.name=Fugato Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture"},
		{"-C", source, "worktree", "add", "-q", "-b", "target", target},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	// Nil dependencies prove preparation does not read the registry or backend.
	manager := NewManager(nil, nil, 46656)
	service := ControlService{Manager: manager}
	input := control.PrepareRequest{Path: target, From: source, DryRun: true}
	for range 5 {
		report, err := service.Prepare(context.Background(), input)
		if err == nil || errors.Is(err, control.ErrBusy) || report.SchemaVersion != 1 || report.OK || len(report.Entries) != 1 || report.Entries[0].Status != "missing" {
			t.Fatalf("blocked preparation = %+v, %v", report, err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, ".env.local"), []byte("SECRET_VALUE"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := service.Prepare(context.Background(), input)
	if err != nil || !report.OK || !report.DryRun || report.Entries[0].Status != "would_copy" {
		t.Fatalf("dry run = %+v, %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(target, ".env.local")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote target: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Prepare(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation = %v", err)
	}
	if len(manager.admission.metadata.active) != 0 {
		t.Fatal("preparation leaked metadata admission")
	}
}
