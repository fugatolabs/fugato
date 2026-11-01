package fugato

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

type fakeBackend struct {
	endpoint string
	mu       sync.Mutex
	starts   int
	runs     bool
}

func (f *fakeBackend) Start(context.Context, Project) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.runs = true
	return nil
}

func (f *fakeBackend) Inspect(context.Context, Project) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs, nil
}

func (f *fakeBackend) Stop(context.Context, Project) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = false
	return nil
}

func (*fakeBackend) Logs(context.Context, Project, bool, io.Writer) error { return nil }

func testProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(dir, ".fugato.yaml"), []byte("version: 1\ncommands: {noop: {mode: task, host: {argv: [git, status]}}}\n"), 0600); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestConcurrentUpIsIdempotentAndDownKeepsRegistry(t *testing.T) {
	dir := testProject(t)
	store, err := OpenStore(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := &fakeBackend{}
	manager := NewManager(store, backend, 46656)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var result Runtime
			var err error
			for {
				result, err = manager.Up(ctx, dir)
				if !errors.Is(err, control.ErrBusy) {
					break
				}
				select {
				case <-ctx.Done():
					err = ctx.Err()
				case <-time.After(time.Millisecond):
					continue
				}
				break
			}
			if err == nil && result.PreviewURL == "" {
				err = fmt.Errorf("missing preview URL")
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if backend.starts != 1 {
		t.Fatalf("wanted one backend start, got %d", backend.starts)
	}
	items, err := manager.List(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("wanted one registry entry: %v, %v", items, err)
	}
	stopped, err := manager.Down(context.Background(), items[0].ID)
	if err != nil || stopped.Observed != "stopped" {
		t.Fatalf("down: %+v, %v", stopped, err)
	}
	if _, err := manager.Down(context.Background(), items[0].ID); err != nil {
		t.Fatalf("repeated down: %v", err)
	}
}

// The registry file is checked separately from the lifecycle test so the
// latter can focus on concurrent operations.
func TestStoreSurvivesRestart(t *testing.T) {
	stateDir := t.TempDir()
	store, err := OpenStore(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	r := Runtime{ID: "1234567890abcdef", ProjectDir: "/tmp/example", Worktree: "/tmp/example", Repository: "/tmp/repo", ComposeProject: "fugato-1234567890abcdef", Backend: "compose", Desired: "stopped", Observed: "stopped"}
	if err := store.Save(context.Background(), &r); err != nil {
		t.Fatal(err)
	}
	if r.UpdatedAt == "" {
		t.Fatal("Save did not record a timestamp")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, found, err := store.Get(context.Background(), r.ID)
	if err != nil || !found || got.Desired != "stopped" {
		t.Fatalf("registry restart lost runtime: %+v, %v", got, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, got.UpdatedAt); err != nil {
		t.Fatalf("invalid timestamp: %v", err)
	}
}

func TestStoreRejectsNewerSchema(t *testing.T) {
	stateDir := t.TempDir()
	store, err := OpenStore(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(context.Background(), stateDir); err == nil {
		t.Fatal("newer registry schema was accepted")
	}
}

func (f *fakeBackend) Snapshot(string) []PortBinding {
	host := "127.0.0.1"
	port := 12345
	if f.endpoint != "" {
		host, number, _ := net.SplitHostPort(f.endpoint)
		_ = host
		port, _ = strconv.Atoi(number)
	}
	return []PortBinding{{Name: "site", Service: "web", HostIP: host, HostPort: port, ContainerPort: 3000, Protocol: "http", Browser: true, URL: fmt.Sprintf("http://%s:%d/", host, port)}}
}
func (f *fakeBackend) Ports(context.Context, Project) ([]PortBinding, error) {
	return f.Snapshot(""), nil
}

func TestUnsupportedRegistryRetainsData(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), "ALTER TABLE runtimes ADD COLUMN endpoint TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenStore(t.Context(), dir); err == nil {
		reopened.Close()
		t.Fatal("obsolete registry accepted")
	} else if !strings.Contains(err.Error(), "fresh FUGATO_STATE_DIR") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "registry.sqlite")); err != nil {
		t.Fatal("obsolete registry was deleted")
	}
}
