package fugato

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The real Git smudge filter executes this test binary. Holding a file lock lets
// the parent prove that cancellation killed the child, even before it is reaped.
func TestWorktreeCheckoutFilterProcess(t *testing.T) {
	lockPath := os.Getenv("FUGATO_TEST_FILTER_LOCK")
	if lockPath == "" {
		t.Skip("checkout filter helper")
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath+".started", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Second)
}

func TestWorktreeCreationCancellationStopsCheckoutFilter(t *testing.T) {
	manager, dir := worktreeFixture(t)
	for name, body := range map[string]string{".gitattributes": "*.txt filter=fugato-test\n", "sample.txt": "fixture\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "filter fixture"}} {
		if _, err := worktreeGit(t.Context(), dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	filter := "exec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=^TestWorktreeCheckoutFilterProcess$"
	if _, err := worktreeGit(t.Context(), dir, "config", "filter.fugato-test.smudge", filter); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(t.TempDir(), "filter.lock")
	t.Setenv("FUGATO_TEST_FILTER_LOCK", lockPath)
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	finished := make(chan struct{})
	target := filepath.Join(t.TempDir(), "checkout")
	go func() {
		defer close(finished)
		_, err := manager.CreateWorktree(ctx, createWorktreeRequest{Path: dir, Branch: "cancel-filter", TargetPath: target, NewBranch: true})
		done <- err
	}()
	defer func() {
		cancel()
		// A failed implementation must not strand the test's child or goroutine.
		// Signal only while that child still owns our unique lock, avoiding PID reuse.
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			if data, err := os.ReadFile(lockPath + ".started"); err == nil {
				if pid, err := strconv.Atoi(string(data)); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
		<-finished
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if data, err := os.ReadFile(lockPath + ".started"); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil && pid > 0 {
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("checkout stopped before filter entered: %v", err)
		case <-deadline.C:
			t.Fatal("checkout filter did not enter")
		case <-tick.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("checkout filter kept cancelled Git operation alive")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("checkout filter still owns its lock after cancellation: %v", err)
	}
	if len(manager.admission.metadata.active) != 0 {
		t.Fatal("cancelled worktree operation retained admission")
	}
}
