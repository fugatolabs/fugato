package fugato

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

type reconcileBackend struct {
	mu        sync.Mutex
	running   map[string]bool
	blockedID string
	entered   chan struct{}
	release   chan struct{}
}

func (b *reconcileBackend) Start(_ context.Context, p Project) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running[p.ID] = true
	return nil
}
func (b *reconcileBackend) Stop(_ context.Context, p Project) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running[p.ID] = false
	return nil
}
func (b *reconcileBackend) Inspect(ctx context.Context, p Project) (bool, error) {
	if p.ID == b.blockedID {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running[p.ID], nil
}
func (*reconcileBackend) Logs(context.Context, Project, bool, io.Writer) error { return nil }

func stoppedRuntime(p Project) Runtime {
	return Runtime{ID: p.ID, ProjectDir: p.Dir, Worktree: p.Worktree, Repository: p.Repository,
		ComposeProject: p.ComposeProject, Backend: "compose", Desired: "stopped", Observed: "stopped"}
}

func TestReconcileReloadsDesiredStateAfterConcurrentUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p1, err := ResolveProject(ctx, testProject(t))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := ResolveProject(ctx, testProject(t))
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r1, r2 := stoppedRuntime(p1), stoppedRuntime(p2)
	if err := store.Save(ctx, &r2); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, &r1); err != nil {
		t.Fatal(err)
	}
	// Make ordering independent of wall-clock precision: p1 must be visited first.
	if _, err := store.db.ExecContext(ctx, "UPDATE runtimes SET updated_at = ? WHERE id = ?", "9999", p1.ID); err != nil {
		t.Fatal(err)
	}
	backend := &reconcileBackend{running: map[string]bool{}, blockedID: p1.ID, entered: make(chan struct{}), release: make(chan struct{})}
	manager := NewManager(store, backend, 46656)
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); done <- manager.Reconcile(ctx) }()
	// Even a failing assertion cancels and joins the owned worker before closing DB.
	defer func() { cancel(); <-finished }()
	select {
	case <-backend.entered:
	case <-ctx.Done():
		t.Fatal("reconciliation did not reach first runtime")
	}
	// Reconciliation has already read both records, but has not locked p2.
	if _, err := manager.Up(ctx, p2.Dir); err != nil {
		t.Fatal(err)
	}
	close(backend.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("reconciliation failed to finish")
	}
	got, _, err := manager.Get(ctx, p2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Desired != "running" || got.Observed != "running" {
		t.Fatalf("reconciliation undid successful up: %+v", got)
	}
}

func TestRuntimeGateCancellationAndReclamation(t *testing.T) {
	manager := NewManager(nil, nil, 0)
	const id = "0123456789abcdef"
	unlock, err := manager.lock(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := manager.Down(ctx, id); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("wanted deadline error, got %v", err)
		}
	case <-time.After(time.Second):
		unlock()
		<-done
		t.Fatal("cancelled operation waited for runtime lock")
	}
	manager.locksMu.Lock()
	refs := manager.locks[id].refs
	manager.locksMu.Unlock()
	if refs != 1 {
		t.Errorf("cancelled waiter retained reference: %d", refs)
	}
	unlock()
	if len(manager.locks) != 0 {
		t.Fatal("released gate retained in map")
	}
	// Unknown but valid IDs must not accumulate entries either.
	for i := range 100 {
		release, err := manager.lock(context.Background(), fmt.Sprintf("%016x", i))
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if _, err := manager.lock(context.Background(), "invalid"); err == nil {
		t.Fatal("invalid runtime ID accepted")
	}
	if len(manager.locks) != 0 {
		t.Fatal("gate map grew after operations completed")
	}
}

func TestStoreOperationsHonorCancellationWhileConnectionBusy(t *testing.T) {
	store, err := OpenStore(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	held, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	manager := NewManager(store, nil, 0)
	for _, test := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"save", func(ctx context.Context) error { return store.Save(ctx, &Runtime{}) }},
		{"get", func(ctx context.Context) error { _, _, err := manager.Get(ctx, "0123456789abcdef"); return err }},
		{"list", func(ctx context.Context) error { _, err := manager.List(ctx); return err }},
		{"reconcile", manager.Reconcile},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- test.run(ctx) }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("wanted deadline error, got %v", err)
				}
			case <-time.After(time.Second):
				held.Close()
				<-done
				t.Fatal("operation waited for database connection after cancellation")
			}
		})
	}
}

type cancelledStartBackend struct {
	fakeBackend
	entered chan struct{}
}

func (b *cancelledStartBackend) Start(ctx context.Context, _ Project) error {
	close(b.entered)
	<-ctx.Done()
	return ctx.Err()
}

func TestCancelledStartRecordsFailureWithBoundedCleanup(t *testing.T) {
	store, err := OpenStore(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := testProject(t)
	backend := &cancelledStartBackend{entered: make(chan struct{})}
	manager := NewManager(store, backend, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type outcome struct {
		runtime Runtime
		err     error
	}
	done := make(chan outcome, 1)
	go func() { r, err := manager.Up(ctx, dir); done <- outcome{r, err} }()
	select {
	case <-backend.entered:
	case <-ctx.Done():
		<-done
		t.Fatal("start never reached backend")
	}
	cancel()
	result := <-done
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("wanted cancellation error, got %v", result.err)
	}
	got, found, err := manager.Get(context.Background(), result.runtime.ID)
	if err != nil || !found || got.Observed != "error" || got.LastError == "" {
		t.Fatalf("cancelled start lost persisted failure: %+v, found=%v, err=%v", got, found, err)
	}
}

func TestFailurePersistenceHasIndependentDeadline(t *testing.T) {
	store, err := OpenStore(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	held, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := NewManager(store, nil, 0)
	done := make(chan error, 1)
	go func() { done <- manager.recordFailure(ctx, &Runtime{}) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cleanup did not use independent bounded context: %v", err)
		}
	case <-time.After(3 * time.Second):
		held.Close()
		<-done
		t.Fatal("failure persistence ignored its cleanup deadline")
	}
}
