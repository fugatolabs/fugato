package fugato

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func cleanupFixture(t *testing.T) Runtime {
	t.Helper()
	p, err := ResolveProject(context.Background(), testProject(t))
	if err != nil {
		t.Fatal(err)
	}
	return Runtime{ID: p.ID, ProjectDir: p.Dir, Worktree: p.Worktree, Repository: p.Repository, ComposeProject: p.ComposeProject, Backend: "environment", Desired: "running", Observed: "running"}
}

func TestDestroyDockerOwnershipAndDataPolicy(t *testing.T) {
	for _, volumes := range []bool{false, true} {
		t.Run(map[bool]string{false: "retain data", true: "delete data"}[volumes], func(t *testing.T) {
			r := cleanupFixture(t)
			calls := []string{}
			run := cleanupDockerFixture(r, &calls, "")
			if err := destroyDockerResources(context.Background(), r, volumes, run); err != nil {
				t.Fatal(err)
			}
			var removed []string
			for _, call := range calls {
				if strings.Contains(call, " rm ") {
					removed = append(removed, call)
				}
				if strings.Contains(call, "prune") || strings.Contains(call, "image") {
					t.Fatal(call)
				}
			}
			want := []string{"container rm --force container1", "network rm network1"}
			if volumes {
				want = append(want, "volume rm "+r.ComposeProject+"_db")
			}
			if !reflect.DeepEqual(removed, want) {
				t.Fatalf("removed %v, want %v", removed, want)
			}
			if !volumes {
				for _, call := range calls {
					if strings.HasPrefix(call, "volume ") {
						t.Fatal(call)
					}
				}
			}
		})
	}
}

func cleanupDockerFixture(r Runtime, calls *[]string, wrongKind string) dockerCleanupCommand {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		*calls = append(*calls, strings.Join(args, " "))
		kind, command := args[0], args[1]
		if command == "ls" {
			switch kind {
			case "container":
				return []byte("container1"), nil
			case "network":
				return []byte("network1"), nil
			case "volume":
				return []byte(r.ComposeProject + "_db"), nil
			}
		}
		if command == "inspect" {
			labels := map[string]string{"com.docker.compose.project": r.ComposeProject, "com.docker.compose.service": "web", "com.docker.compose.project.working_dir": r.ProjectDir, "com.docker.compose.network": "default", "com.docker.compose.volume": "db"}
			if kind == wrongKind {
				labels["com.docker.compose.project"] = "fugato-foreign"
			}
			item := map[string]any{"Id": args[2], "Labels": labels, "Config": map[string]any{"Labels": labels}}
			if kind == "network" {
				item["Name"] = r.ComposeProject + "_default"
			}
			if kind == "volume" {
				item["Name"] = args[2]
			}
			return json.Marshal([]any{item})
		}
		if command == "rm" {
			return nil, nil
		}
		return nil, errors.New("unexpected Docker command")
	}
}

func TestDestroyValidatesWholeDockerSelectionBeforeRemoval(t *testing.T) {
	for _, kind := range []string{"container", "network", "volume"} {
		t.Run(kind, func(t *testing.T) {
			r := cleanupFixture(t)
			var calls []string
			err := destroyDockerResources(context.Background(), r, true, cleanupDockerFixture(r, &calls, kind))
			if err == nil || !strings.Contains(err.Error(), "ownership mismatch") {
				t.Fatalf("error = %v", err)
			}
			for _, call := range calls {
				if strings.Contains(call, " rm ") {
					t.Fatalf("mutated before validating ownership: %s", call)
				}
			}
		})
	}
}

func TestDestroyRejectsContainerWorkingDirectoryMismatch(t *testing.T) {
	r := cleanupFixture(t)
	var calls []string
	fixture := cleanupDockerFixture(r, &calls, "")
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		output, err := fixture(ctx, args...)
		if len(args) > 1 && args[0] == "container" && args[1] == "inspect" {
			output = []byte(strings.ReplaceAll(string(output), r.ProjectDir, "/another/project"))
		}
		return output, err
	}
	if err := destroyDockerResources(context.Background(), r, false, run); err == nil {
		t.Fatal("accepted foreign project directory")
	}
	for _, call := range calls {
		if strings.Contains(call, " rm ") {
			t.Fatal(call)
		}
	}
}

func TestDestroyNativeMissingWorktreeAndDataBoundary(t *testing.T) {
	for _, volumes := range []bool{false, true} {
		t.Run(map[bool]string{false: "retain", true: "delete"}[volumes], func(t *testing.T) {
			ctx := context.Background()
			state := t.TempDir()
			store, err := OpenStore(ctx, state)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			backend := NewEnvironmentBackend(state)
			manager := NewManager(store, backend, 46656)
			dir := testProject(t)
			runtime, err := manager.Up(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.Backend != "native" {
				t.Fatalf("native marker: %s", runtime.Backend)
			}
			// A new daemon has no in-memory owner; saved type must avoid Docker.
			backend = NewEnvironmentBackend(state)
			manager = NewManager(store, backend, 46656)
			t.Setenv("PATH", t.TempDir())
			data := filepath.Join(state, "data", runtime.ID)
			if err := os.MkdirAll(data, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(data, "db"), []byte("saved"), 0600); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(state, "data", "another-runtime")
			if err := os.MkdirAll(other, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			result, err := manager.Destroy(ctx, runtime.ID, volumes)
			if err != nil {
				t.Fatal(err)
			}
			if result.ID != runtime.ID || result.Volumes != volumes {
				t.Fatalf("result: %+v", result)
			}
			if _, found, err := store.Get(ctx, runtime.ID); err != nil || found {
				t.Fatalf("registry retained: %v %v", found, err)
			}
			if _, found := backend.ownedProject(runtime.ID); found {
				t.Fatal("owner retained")
			}
			_, err = os.Stat(data)
			if volumes && !os.IsNotExist(err) || !volumes && err != nil {
				t.Fatalf("data policy: %v", err)
			}
			if _, err := os.Stat(other); err != nil {
				t.Fatalf("other runtime touched: %v", err)
			}
		})
	}
}

func TestRemoveRuntimeDataRejectsSymlinks(t *testing.T) {
	for _, target := range []string{"state", "data", "runtime", "child"} {
		t.Run(target, func(t *testing.T) {
			state := t.TempDir()
			outside := t.TempDir()
			marker := filepath.Join(outside, "keep")
			if err := os.WriteFile(marker, []byte("precious"), 0600); err != nil {
				t.Fatal(err)
			}
			id := "0123456789abcdef"
			data := filepath.Join(state, "data", id)
			if err := os.MkdirAll(data, 0700); err != nil {
				t.Fatal(err)
			}
			link := state
			switch target {
			case "data":
				link = filepath.Join(state, "data")
			case "runtime":
				link = data
			case "child":
				link = filepath.Join(data, "link")
			}
			if err := os.RemoveAll(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			err := removeRuntimeData(context.Background(), state, id)
			if target != "child" && err == nil {
				t.Fatal("accepted symlink")
			}
			if target == "child" && err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("escaped data root: %v", err)
			}
		})
	}
}

type failingDestroyBackend struct {
	fakeBackend
	called bool
}

func (b *failingDestroyBackend) Destroy(context.Context, Runtime, bool) error {
	b.called = true
	return errors.New("cleanup failed")
}

func TestDestroyFailureKeepsStoppedDesiredRecord(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r := cleanupFixture(t)
	if err := store.Save(ctx, &r); err != nil {
		t.Fatal(err)
	}
	backend := &failingDestroyBackend{}
	manager := NewManager(store, backend, 46656)
	if _, err := manager.Destroy(ctx, r.ID, true); err == nil {
		t.Fatal("missing cleanup error")
	}
	saved, found, err := store.Get(ctx, r.ID)
	if err != nil || !found || saved.Desired != "stopped" || saved.Observed != "error" || saved.LastError != "cleanup failed" {
		t.Fatalf("saved = %+v, %v %v", saved, found, err)
	}
}

func TestDestroyRefusesInconsistentSavedIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r := cleanupFixture(t)
	r.ComposeProject = "another-project"
	if err := store.Save(ctx, &r); err != nil {
		t.Fatal(err)
	}
	backend := &failingDestroyBackend{}
	manager := NewManager(store, backend, 46656)
	if _, err := manager.Destroy(ctx, r.ID, true); err == nil {
		t.Fatal("accepted corrupt identity")
	}
	if backend.called {
		t.Fatal("called backend with corrupt identity")
	}
}

func TestDestroyCancelledWaiterDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r := cleanupFixture(t)
	if err := store.Save(ctx, &r); err != nil {
		t.Fatal(err)
	}
	backend := &failingDestroyBackend{}
	manager := NewManager(store, backend, 46656)
	unlock, err := manager.lock(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := manager.Destroy(cancelled, r.ID, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("error: %v", err)
	}
	if backend.called {
		t.Fatal("cancelled cleanup ran")
	}
	saved, _, _ := store.Get(ctx, r.ID)
	if saved.Desired != "running" {
		t.Fatalf("changed desired state: %+v", saved)
	}
}

func TestDestroyEvictsOwnerBindingsAndReports(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	store, err := OpenStore(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := NewEnvironmentBackend(state)
	manager := NewManager(store, backend, 46656)
	r, err := manager.Up(ctx, testProject(t))
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.environments[r.ID].bindings = []PortBinding{{Name: "web", HostPort: 9999}}
	backend.mu.Unlock()
	manager.reports.entries[r.ID] = &reportEntry{bytes: 12}
	manager.reports.bytes = 12
	if _, err := manager.Destroy(ctx, r.ID, false); err != nil {
		t.Fatal(err)
	}
	if backend.SnapshotReady(r.ID) || len(backend.Snapshot(r.ID)) != 0 {
		t.Fatal("stale endpoint snapshot retained")
	}
	if len(manager.reports.entries) != 0 || manager.reports.bytes != 0 {
		t.Fatal("verification reports retained")
	}
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(ctx, r.ID); err != nil || found {
		t.Fatalf("reconciliation resurrected runtime: %v %v", found, err)
	}
}

func TestUpPreservesComposeCleanupHistory(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r := cleanupFixture(t)
	r.Desired, r.Observed = "stopped", "stopped"
	if err := store.Save(ctx, &r); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, &fakeBackend{}, 46656)
	restarted, err := manager.Up(ctx, r.ProjectDir)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Backend != "environment" {
		t.Fatalf("lost Compose resource history: %s", restarted.Backend)
	}
}
