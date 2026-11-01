package fugato

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

func TestCreateNativeRegistersStoppedWithoutStartingWorkloads(t *testing.T) {
	dir := nativeTestProject(t, fmt.Sprintf(`version: 1
start: {commands: [server]}
commands:
  server:
    mode: service
    host:
      argv: [%q, '-test.run=^TestNativeProcessHelper$']
      env: {TEST_NATIVE_MODE: sleep}
`, os.Args[0]))
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
	r, err := manager.Create(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Desired != "stopped" || r.Observed != "stopped" || r.Backend != "native" {
		t.Fatalf("created runtime: %+v", r)
	}
	if backend.environment(r.ID) != nil {
		t.Fatal("creation acquired a native process owner")
	}
	if _, err := os.Stat(filepath.Join(state, "data", r.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("creation allocated workload data: %v", err)
	}
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if backend.environment(r.ID) != nil {
		t.Fatal("reconciliation started the created environment")
	}
	running, err := manager.UpExpected(ctx, dir, r.ID)
	if err != nil || running.Observed != "running" || backend.environment(r.ID) == nil {
		t.Fatalf("explicit start after create: %+v, %v", running, err)
	}
}

func TestCreatePreservesExistingRuntimeStates(t *testing.T) {
	ctx := context.Background()
	dir := testProject(t)
	store, err := OpenStore(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := &fakeBackend{}
	manager := NewManager(store, backend, 46656)
	r, err := manager.Create(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"stopped", "running", "error"} {
		t.Run(state, func(t *testing.T) {
			r.Desired, r.Observed, r.LastError = state, state, "retained evidence"
			if err := store.Save(ctx, &r); err != nil {
				t.Fatal(err)
			}
			got, err := manager.Create(ctx, dir, r.ID)
			if err != nil || !reflect.DeepEqual(got, manager.decorate(r)) {
				t.Fatalf("create changed existing state: %+v, want %+v; %v", got, r, err)
			}
			saved, found, err := store.Get(ctx, r.ID)
			if err != nil || !found || !reflect.DeepEqual(saved, r) {
				t.Fatalf("create rewrote registration: %+v, want %+v; %v", saved, r, err)
			}
		})
	}
	if backend.starts != 0 || backend.runs {
		t.Fatal("create started workloads")
	}
	r.ProjectDir = filepath.Join(dir, "other-project")
	if _, err := store.db.ExecContext(ctx, "UPDATE runtimes SET project_dir = ? WHERE id = ?", r.ProjectDir, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(ctx, dir, r.ID); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("create reused another project's registration: %v", err)
	}
}

func TestCreateHonorsAdmissionCancellationAndIdentity(t *testing.T) {
	ctx := context.Background()
	dir := testProject(t)
	store, err := OpenStore(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store, &fakeBackend{}, 46656)
	if _, err := manager.Create(ctx, dir, "0000000000000000"); err == nil {
		t.Fatal("accepted mismatched expected runtime ID")
	}
	var releases []func()
	for range 4 {
		release, err := manager.admission.lifecycle.acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	_, err = manager.Create(ctx, dir, "")
	for _, release := range releases {
		release()
	}
	if !errors.Is(err, control.ErrBusy) {
		t.Fatalf("create overload: %v", err)
	}
	p, err := ResolveProject(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := manager.lock(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := manager.Create(waitCtx, dir, p.ID); done <- err }()
	deadline := time.After(5 * time.Second)
	for {
		manager.locksMu.Lock()
		waiting := manager.locks[p.ID].refs == 2
		manager.locksMu.Unlock()
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("create returned before reaching runtime gate: %v", err)
		case <-deadline:
			cancel()
			<-done
			t.Fatal("create never waited on runtime gate")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("create did not cancel while waiting for runtime gate: %v", err)
	}
	if items, err := store.List(ctx); err != nil || len(items) != 0 {
		t.Fatalf("failed create registered a runtime: %+v, %v", items, err)
	}
}

func TestCreateHTTPRejectsUnauthorizedAndInvalidRequests(t *testing.T) {
	ctx := context.Background()
	dir := testProject(t)
	state := t.TempDir()
	store, err := OpenStore(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store, &fakeBackend{}, 46656)
	handler := apiHandler(manager, "secret", state, "127.0.0.1:46655")
	for _, tc := range []struct {
		name, token, body string
		status            int
	}{
		{"unauthorized", "", fmt.Sprintf(`{"path":%q}`, dir), http.StatusUnauthorized},
		{"unknown field", "secret", fmt.Sprintf(`{"path":%q,"start":true}`, dir), http.StatusBadRequest},
		{"relative path", "secret", `{"path":"."}`, http.StatusBadRequest},
		{"wrong identity", "secret", fmt.Sprintf(`{"path":%q,"expected_runtime_id":"0000000000000000"}`, dir), http.StatusBadRequest},
		{"create", "secret", fmt.Sprintf(`{"path":%q}`, dir), http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:46655/v1/runtimes/create", strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer "+tc.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
			if tc.status == http.StatusOK {
				var runtime control.Runtime
				if err := json.Unmarshal(response.Body.Bytes(), &runtime); err != nil || runtime.Observed != "stopped" {
					t.Fatalf("create response: %+v, %v", runtime, err)
				}
				var fields map[string]any
				if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
					t.Fatal(err)
				}
				// The editor's strict runtime parser requires string values even
				// when metadata is unavailable, such as HEAD in this unborn repo.
				for _, key := range []string{"id", "worktree", "project_dir", "repository", "commit", "backend", "desired", "observed", "health"} {
					if _, ok := fields[key].(string); !ok {
						t.Errorf("create response field %q must be a string: %s", key, response.Body.String())
					}
				}
				if runtime.Desired != "stopped" || runtime.Health != "unavailable" || runtime.Commit != "" || runtime.MetadataError == "" {
					t.Fatalf("unexpected created runtime metadata: %+v", runtime)
				}
			}
		})
	}
}

func TestCreateValidatesComposeWithoutStartingContainers(t *testing.T) {
	for _, privileged := range []bool{false, true} {
		t.Run(fmt.Sprint(privileged), func(t *testing.T) {
			dir := testProject(t)
			if err := os.WriteFile(filepath.Join(dir, ".fugato.yaml"), []byte("version: 1\nstart: {services: [web]}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {web: {image: busybox}}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			wrapperDir := t.TempDir()
			calls := filepath.Join(wrapperDir, "calls")
			script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
for arg in "$@"; do
  if [ "$arg" = config ]; then
    printf '%%s\n' '{"services":{"web":{"privileged":%t}}}'
    exit 0
  fi
done
exit 97
`, testShellQuote(calls), privileged)
			if err := os.WriteFile(filepath.Join(wrapperDir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx := context.Background()
			store, err := OpenStore(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			manager := NewManager(store, &fakeBackend{}, 46656)
			result, err := manager.Create(ctx, dir, "")
			if privileged && err == nil || !privileged && (err != nil || result.Observed != "stopped") {
				t.Fatalf("privileged=%v result=%+v err=%v", privileged, result, err)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if !strings.Contains(line, " config --format json") {
					t.Fatalf("create executed a workload operation: %s", line)
				}
			}
			items, err := store.List(ctx)
			if err != nil || len(items) == 0 && !privileged || len(items) > 0 && privileged {
				t.Fatalf("unsafe create registration: %+v, %v", items, err)
			}
		})
	}
}

func TestCreateCancelsEffectiveComposeValidationWithoutRegistering(t *testing.T) {
	dir := testProject(t)
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {web: {image: busybox}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wrapperDir := t.TempDir()
	entered := filepath.Join(wrapperDir, "effective-validation")
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = - ]; then
    : > %s
    exec /bin/sleep 30
  fi
done
printf '%%s\n' '{"services":{"web":{}}}'
`, testShellQuote(entered))
	if err := os.WriteFile(filepath.Join(wrapperDir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	store, err := OpenStore(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store, &fakeBackend{}, 46656)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := manager.Create(ctx, dir, ""); done <- err }()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("create returned before effective validation: %v", err)
		case <-deadline:
			cancel()
			<-done
			t.Fatal("effective validation never started")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled validation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("create continued validating after cancellation")
	}
	if items, err := store.List(t.Context()); err != nil || len(items) != 0 {
		t.Fatalf("cancelled validation registered runtime: %+v, %v", items, err)
	}
	releaseCtx, cancelRelease := context.WithTimeout(t.Context(), time.Second)
	defer cancelRelease()
	release, err := manager.acquireWorktreeRegistration(releaseCtx)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
