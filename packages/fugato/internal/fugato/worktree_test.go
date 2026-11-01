package fugato

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

func worktreeFixture(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := testProject(t)
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewManager(store, &fakeBackend{}, 46656), dir
}

func addTestWorktree(t *testing.T, manager *Manager, dir, branch string) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "linked")
	listed, err := manager.CreateWorktree(t.Context(), createWorktreeRequest{Path: dir, Branch: branch, TargetPath: target, NewBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range listed.Worktrees {
		if item.Branch == branch {
			return item.Path
		}
	}
	t.Fatalf("created branch absent: %+v", listed)
	return ""
}

func TestWorktreeCreateRemoveHTTP(t *testing.T) {
	manager, dir := worktreeFixture(t)
	handler := apiHandler(manager, "test-token", t.TempDir(), "127.0.0.1:46655")
	target := filepath.Join(t.TempDir(), "feature")
	created := extensionRequest(t, handler, http.MethodPost, "/v1/repositories/worktrees/create", createWorktreeRequest{Path: dir, Branch: "feature", TargetPath: target, NewBranch: true, StartPoint: "HEAD"})
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"branch":"feature"`) {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	removed := extensionRequest(t, handler, http.MethodPost, "/v1/repositories/worktrees/remove", removeWorktreeRequest{Path: dir, Worktree: target})
	if removed.Code != http.StatusOK || !strings.Contains(removed.Body.String(), `"removed":true`) {
		t.Fatalf("remove: %d %s", removed.Code, removed.Body.String())
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree directory retained: %v", err)
	}
	// Removing a worktree retains its branch for later use.
	listed, err := manager.CreateWorktree(t.Context(), createWorktreeRequest{Path: dir, Branch: "feature", TargetPath: target})
	if err != nil || len(listed.Worktrees) != 2 || listed.Worktrees[1].Branch != "feature" {
		t.Fatalf("existing branch: %+v, %v", listed, err)
	}
	if _, err := manager.CreateWorktree(t.Context(), createWorktreeRequest{Path: dir, Branch: "feature", TargetPath: filepath.Join(t.TempDir(), "duplicate")}); err == nil {
		t.Fatal("branch already checked out accepted")
	}
}

func TestWorktreeRemovalProtectsFilesAndEnvironments(t *testing.T) {
	for _, kind := range []string{"tracked", "untracked", "ignored", "registered", "primary", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			manager, dir := worktreeFixture(t)
			target := addTestWorktree(t, manager, dir, "feature")
			switch kind {
			case "tracked":
				if err := os.WriteFile(filepath.Join(target, ".fugato.yaml"), []byte("modified"), 0600); err != nil {
					t.Fatal(err)
				}
			case "untracked":
				if err := os.WriteFile(filepath.Join(target, "local-data"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "ignored":
				if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("local-data\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "local-data"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "registered":
				p, err := DiscoverProject(t.Context(), target, "")
				if err != nil {
					t.Fatal(err)
				}
				r := Runtime{ID: p.ID, Worktree: p.Worktree, ProjectDir: p.Dir, Repository: p.Repository, Desired: "stopped", Observed: "stopped"}
				if err := manager.store.Save(t.Context(), &r); err != nil {
					t.Fatal(err)
				}
			case "primary":
				target = dir
			case "foreign":
				target = testProject(t)
			}
			err := manager.RemoveWorktree(t.Context(), removeWorktreeRequest{Path: dir, Worktree: target})
			if err == nil {
				t.Fatal("unsafe removal accepted")
			}
			if expected := map[string]string{"tracked": "Changed: .fugato.yaml", "untracked": "Untracked: local-data", "ignored": "Ignored: local-data"}[kind]; expected != "" {
				if !strings.Contains(err.Error(), expected) || !strings.Contains(err.Error(), "Commit or stash") || !strings.Contains(err.Error(), "outside this worktree") {
					t.Fatalf("missing file and preservation guidance: %v", err)
				}
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("protected files lost: %v", err)
			}
		})
	}
}

func TestWorktreeRemovalReportsBoundedFileList(t *testing.T) {
	manager, dir := worktreeFixture(t)
	target := addTestWorktree(t, manager, dir, "feature")
	for i := range 12 {
		if err := os.WriteFile(filepath.Join(target, fmt.Sprintf("local %02d\nfile", i)), []byte("private file contents"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	handler := apiHandler(manager, "test-token", t.TempDir(), "127.0.0.1:46655")
	response := extensionRequest(t, handler, http.MethodPost, "/v1/repositories/worktrees/remove", removeWorktreeRequest{Path: dir, Worktree: target})
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code < 400 || strings.Count(body.Error, "Untracked:") != 8 || !strings.Contains(body.Error, "Additional entries omitted") {
		t.Fatalf("unexpected refusal: %d %s", response.Code, body.Error)
	}
	if !strings.Contains(body.Error, `"local 00\nfile"`) || strings.Contains(body.Error, "private file contents") {
		t.Fatalf("filenames must stay quoted and contents private: %s", body.Error)
	}
	if data, err := os.ReadFile(filepath.Join(target, "local 11\nfile")); err != nil || string(data) != "private file contents" {
		t.Fatalf("local file changed: %q, %v", data, err)
	}
}

func TestWorktreeRemovalOfUnavailableCheckout(t *testing.T) {
	for _, kind := range []string{"missing folder", "missing Git marker"} {
		t.Run(kind, func(t *testing.T) {
			manager, dir := worktreeFixture(t)
			target := addTestWorktree(t, manager, dir, "feature")
			if kind == "missing folder" {
				if err := os.RemoveAll(target); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(filepath.Join(target, ".git")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "local-data"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := manager.RemoveWorktree(t.Context(), removeWorktreeRequest{Path: dir, Worktree: target})
			if kind == "missing Git marker" {
				if err == nil || !strings.Contains(err.Error(), "folder still exists") || !strings.Contains(err.Error(), "move") {
					t.Fatalf("expected actionable missing-metadata error, got %v", err)
				}
				contents, err := os.ReadFile(filepath.Join(target, "local-data"))
				if err != nil || string(contents) != "keep" {
					t.Fatalf("remaining files were changed: %q, %v", contents, err)
				}
				listed, err := DiscoverWorktrees(t.Context(), dir)
				if err != nil || len(listed.Worktrees) != 2 {
					t.Fatalf("registration was changed: %+v, %v", listed, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				listed, err := DiscoverWorktrees(t.Context(), dir)
				if err != nil || len(listed.Worktrees) != 1 {
					t.Fatalf("missing checkout registration retained: %+v, %v", listed, err)
				}
			}
			if _, err := worktreeGit(t.Context(), dir, "show-ref", "--verify", "refs/heads/feature"); err != nil {
				t.Fatalf("branch was removed: %v", err)
			}
		})
	}
}

func TestWorktreeMutationRejectsInvalidInputsAndOverload(t *testing.T) {
	manager, dir := worktreeFixture(t)
	for _, request := range []createWorktreeRequest{
		{Path: ".", Branch: "feature", TargetPath: filepath.Join(t.TempDir(), "new"), NewBranch: true},
		{Path: dir, Branch: "--orphan", TargetPath: filepath.Join(t.TempDir(), "new"), NewBranch: true},
		{Path: dir, Branch: "feature", TargetPath: "relative", NewBranch: true},
		{Path: dir, Branch: "missing", TargetPath: filepath.Join(t.TempDir(), "new")},
		{Path: dir, Branch: "feature", TargetPath: filepath.Join(t.TempDir(), "new"), NewBranch: true, StartPoint: "--all"},
		{Path: dir, Branch: "feature", TargetPath: dir, NewBranch: true},
	} {
		if _, err := manager.CreateWorktree(t.Context(), request); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
	for range cap(manager.admission.metadata.active) {
		release, err := manager.admission.metadata.acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
	if _, err := manager.CreateWorktree(t.Context(), createWorktreeRequest{}); !errors.Is(err, control.ErrBusy) {
		t.Fatalf("create overload: %v", err)
	}
	if err := manager.RemoveWorktree(t.Context(), removeWorktreeRequest{}); !errors.Is(err, control.ErrBusy) {
		t.Fatalf("remove overload: %v", err)
	}
}

func TestWorktreeCreationSkipsCheckoutHooks(t *testing.T) {
	manager, dir := worktreeFixture(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := "#!/bin/sh\nprintf ran > '" + strings.ReplaceAll(marker, "'", "'\\''") + "'\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "post-checkout"), []byte(hook), 0700); err != nil {
		t.Fatal(err)
	}
	addTestWorktree(t, manager, dir, "feature")
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout hook executed: %v", err)
	}
}

func TestWorktreeOperationsSkipFSMonitor(t *testing.T) {
	manager, dir := worktreeFixture(t)
	marker := filepath.Join(t.TempDir(), "monitor-ran")
	monitor := filepath.Join(t.TempDir(), "fsmonitor")
	command := "#!/bin/sh\nprintf ran > '" + strings.ReplaceAll(marker, "'", "'\\''") + "'\n"
	if err := os.WriteFile(monitor, []byte(command), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := worktreeGit(t.Context(), dir, "config", "core.fsmonitor", monitor); err != nil {
		t.Fatal(err)
	}
	target := addTestWorktree(t, manager, dir, "feature")
	if err := manager.RemoveWorktree(t.Context(), removeWorktreeRequest{Path: dir, Worktree: target}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git fsmonitor executed: %v", err)
	}
}

func TestWorktreeRemovalBeforeFirstRegistration(t *testing.T) {
	for _, operation := range []string{"up", "create"} {
		t.Run(operation, func(t *testing.T) {
			manager, dir := worktreeFixture(t)
			target := addTestWorktree(t, manager, dir, "feature")
			p, err := DiscoverProject(t.Context(), target, "")
			if err != nil {
				t.Fatal(err)
			}
			unlock, err := manager.lock(t.Context(), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if unlock != nil {
					unlock()
				}
			}()
			done := make(chan error, 1)
			go func() {
				var err error
				if operation == "up" {
					_, err = manager.Up(t.Context(), target)
				} else {
					_, err = manager.Create(t.Context(), target, "")
				}
				done <- err
			}()
			// Initial identity resolution has finished, but this operation has
			// not acquired its runtime gate or registered the environment yet.
			waitWorktreeCondition(t, func() bool {
				manager.locksMu.Lock()
				defer manager.locksMu.Unlock()
				return manager.locks[p.ID].refs == 2
			})
			manager.locksMu.Lock()
			registrationHeld := len(manager.registration) != 0
			manager.locksMu.Unlock()
			if registrationHeld {
				t.Fatal("runtime waiter holds registration and blocks unrelated worktrees")
			}
			if err := manager.RemoveWorktree(t.Context(), removeWorktreeRequest{Path: dir, Worktree: target}); err != nil {
				t.Fatal(err)
			}
			unlock()
			unlock = nil
			if err := <-done; err == nil {
				t.Fatal("registered a checkout removed after initial identity resolution")
			}
			items, err := manager.store.List(t.Context())
			if err != nil || len(items) != 0 {
				t.Fatalf("deleted checkout registration: %+v, %v", items, err)
			}
		})
	}
}

func TestWorktreeRuntimeWaiterDoesNotBlockUnrelatedRegistration(t *testing.T) {
	for _, operation := range []string{"up", "create"} {
		t.Run(operation, func(t *testing.T) {
			manager, dir := worktreeFixture(t)
			runtime, err := manager.Create(t.Context(), dir, "")
			if err != nil {
				t.Fatal(err)
			}
			unlock, err := manager.lock(t.Context(), runtime.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if unlock != nil {
					unlock()
				}
			}()
			done := make(chan error, 1)
			go func() {
				var err error
				if operation == "up" {
					_, err = manager.Up(t.Context(), dir)
				} else {
					_, err = manager.Create(t.Context(), dir, "")
				}
				done <- err
			}()
			waitWorktreeCondition(t, func() bool {
				manager.locksMu.Lock()
				defer manager.locksMu.Unlock()
				return manager.locks[runtime.ID].refs == 2
			})
			if len(manager.registration) != 0 {
				t.Fatal("existing runtime waiter holds shared registration gate")
			}
			if _, err := manager.Create(t.Context(), testProject(t), ""); err != nil {
				t.Fatalf("unrelated registration: %v", err)
			}
			unlock()
			unlock = nil
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorktreeRegistrationWaiterCancellation(t *testing.T) {
	manager, dir := worktreeFixture(t)
	release, err := manager.acquireWorktreeRegistration(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := manager.Up(ctx, dir); done <- err }()
	waitWorktreeCondition(t, func() bool { return len(manager.admission.lifecycle.active) == 1 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter cancellation: %v", err)
	}
	release()
	items, err := manager.store.List(t.Context())
	if err != nil || len(items) != 0 {
		t.Fatalf("cancelled registration persisted: %+v, %v", items, err)
	}
}

func waitWorktreeCondition(t *testing.T, condition func() bool) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !condition() {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("worktree operation did not reach synchronization barrier")
		}
	}
}
