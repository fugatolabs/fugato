package fugato

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// This backend blocks restoration at the same boundary as a Compose inspection
// after daemon restart: persisted state exists before live endpoints are known.
type restoringBackend struct {
	fakeBackend
	restoreMu sync.Mutex
	ready     bool
	entered   chan struct{}
	release   chan struct{}
}

func (b *restoringBackend) SnapshotReady(string) bool {
	b.restoreMu.Lock()
	defer b.restoreMu.Unlock()
	return b.ready
}
func (b *restoringBackend) Snapshot(id string) []PortBinding {
	if !b.SnapshotReady(id) {
		return nil
	}
	return b.fakeBackend.Snapshot(id)
}
func (b *restoringBackend) Inspect(ctx context.Context, _ Project) (bool, error) {
	close(b.entered)
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-b.release:
	}
	b.restoreMu.Lock()
	b.ready = true
	b.restoreMu.Unlock()
	return true, nil
}
func TestRestartDoesNotReportRunningBeforeNamedRoutesAreRestored(t *testing.T) {
	ctx := t.Context()
	dir := testProject(t)
	project, err := ResolveProject(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := Runtime{ID: project.ID, ProjectDir: project.Dir, Worktree: project.Worktree, Repository: project.Repository, ComposeProject: project.ComposeProject, Backend: "environment", Desired: "running", Observed: "running"}
	if err := store.Save(ctx, &record); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "restored") }))
	defer upstream.Close()
	backend := &restoringBackend{fakeBackend: fakeBackend{endpoint: strings.TrimPrefix(upstream.URL, "http://")}, entered: make(chan struct{}), release: make(chan struct{})}
	manager := NewManager(store, backend, 46656)
	before, found, err := manager.Get(ctx, project.ID)
	if err != nil || !found || before.Observed != "unknown" || before.PreviewURL != "" {
		t.Fatalf("unreconciled record claims availability: %+v %v", before, err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.Reconcile(ctx) }()
	<-backend.entered
	items, err := manager.List(ctx)
	if err != nil || len(items) != 1 || items[0].Observed == "running" {
		t.Fatalf("blocked reconciliation exposes running: %+v %v", items, err)
	}
	close(backend.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after, _, err := manager.Get(ctx, project.ID)
	if err != nil || after.Observed != "running" || after.PreviewURL == "" {
		t.Fatalf("restored metadata unavailable: %+v %v", after, err)
	}
	response := httptest.NewRecorder()
	proxyHandler(manager, 46656).ServeHTTP(response, httptest.NewRequest(http.MethodGet, after.PreviewURL, nil))
	if response.Code != 200 || response.Body.String() != "restored" {
		t.Fatalf("running route unavailable: %d %s", response.Code, response.Body.String())
	}
}
func TestZeroEndpointSnapshotCanBeReady(t *testing.T) {
	backend := NewEnvironmentBackend(t.TempDir())
	backend.environments["aaaaaaaaaaaaaaaa"] = &nativeEnvironment{}
	if !backend.SnapshotReady("aaaaaaaaaaaaaaaa") {
		t.Fatal("zero-endpoint environment cannot finish reconciliation")
	}
	if backend.SnapshotReady("bbbbbbbbbbbbbbbb") {
		t.Fatal("unobserved environment is ready")
	}
}
