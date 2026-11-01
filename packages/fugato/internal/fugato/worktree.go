package fugato

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type createWorktreeRequest struct {
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	TargetPath string `json:"target_path"`
	NewBranch  bool   `json:"new_branch,omitempty"`
	StartPoint string `json:"start_point,omitempty"`
}

type removeWorktreeRequest struct {
	Path     string `json:"path"`
	Worktree string `json:"worktree"`
}

// Registration and worktree removal share this short gate: no registered
// environment may appear between removal's registry check and Git mutation.
// Only new registration uses it, after acquiring the runtime gate. Release
// before starting workloads; runtime gates continue to own that work.
func (m *Manager) acquireWorktreeRegistration(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.locksMu.Lock()
	if m.registration == nil {
		m.registration = make(chan struct{}, 1)
	}
	gate := m.registration
	m.locksMu.Unlock()
	select {
	case gate <- struct{}{}:
		var once sync.Once
		release := func() { once.Do(func() { <-gate }) }
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// The caller holds the runtime gate for project.ID and has observed no saved
// record. Removal may have won before this gate was acquired, so resolve again
// before registration rather than saving an identity for a deleted checkout.
func (m *Manager) acquireProjectRegistration(ctx context.Context, path string, project Project) (Project, func(), error) {
	release, err := m.acquireWorktreeRegistration(ctx)
	if err != nil {
		return Project{}, nil, err
	}
	resolved, err := resolveExpectedProject(ctx, path, project.ID)
	if err != nil {
		release()
		return Project{}, nil, err
	}
	if resolved.Dir != project.Dir || resolved.Repository != project.Repository || resolved.Worktree != project.Worktree {
		release()
		return Project{}, nil, errors.New("project identity changed before registration")
	}
	return resolved, release, nil
}

// worktreeGit bounds both process lifetime (by its caller) and output, and never
// evaluates a shell or repository checkout hooks during an editor mutation.
func worktreeGit(ctx context.Context, dir string, args ...string) (string, error) {
	argv := append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = dir
	// Git may launch checkout filters that inherit its output pipes. Own that
	// process group during the command so cancellation also stops those children.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr cappedGitOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("Git worktree operation: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (m *Manager) CreateWorktree(ctx context.Context, input createWorktreeRequest) (repositoryWorktrees, error) {
	release, err := m.admission.metadata.acquire(ctx)
	if err != nil {
		return repositoryWorktrees{}, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir, err := absoluteDirectory(input.Path)
	if err != nil {
		return repositoryWorktrees{}, err
	}
	if _, err := worktreeGit(ctx, dir, "check-ref-format", "--branch", input.Branch); err != nil {
		return repositoryWorktrees{}, fmt.Errorf("invalid branch name: %w", err)
	}
	if input.StartPoint != "" && !input.NewBranch {
		return repositoryWorktrees{}, errors.New("start_point is only valid when creating a new branch")
	}
	if !filepath.IsAbs(input.TargetPath) {
		return repositoryWorktrees{}, errors.New("target_path must be absolute")
	}
	parent, err := absoluteDirectory(filepath.Dir(filepath.Clean(input.TargetPath)))
	if err != nil {
		return repositoryWorktrees{}, fmt.Errorf("target parent directory: %w", err)
	}
	target := filepath.Join(parent, filepath.Base(filepath.Clean(input.TargetPath)))
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return repositoryWorktrees{}, errors.New("target_path must not already exist")
	}
	listed, err := DiscoverWorktrees(ctx, dir)
	if err != nil {
		return repositoryWorktrees{}, err
	}
	if target == listed.Repository || strings.HasPrefix(target, listed.Repository+string(filepath.Separator)) {
		return repositoryWorktrees{}, errors.New("target_path must be outside Git metadata")
	}
	args := []string{"worktree", "add"}
	ref := "refs/heads/" + input.Branch
	if input.NewBranch {
		start := input.StartPoint
		if start == "" {
			start = "HEAD"
		}
		// Resolve before passing to worktree add so option-like refs cannot be
		// interpreted as flags and the chosen starting commit stays stable.
		ref, err = worktreeGit(ctx, dir, "rev-parse", "--verify", "--end-of-options", start+"^{commit}")
		if err != nil {
			return repositoryWorktrees{}, fmt.Errorf("resolve start_point: %w", err)
		}
		ref = strings.TrimSpace(ref)
		args = append(args, "-b", input.Branch)
	} else if _, err := worktreeGit(ctx, dir, "show-ref", "--verify", "--", ref); err != nil {
		return repositoryWorktrees{}, fmt.Errorf("existing local branch required: %w", err)
	} else {
		// Git attaches HEAD only for a branch's short name; a full ref would
		// unexpectedly create a detached checkout.
		ref = input.Branch
	}
	args = append(args, "--", target, ref)
	if _, err := worktreeGit(ctx, dir, args...); err != nil {
		return repositoryWorktrees{}, err
	}
	return DiscoverWorktrees(ctx, dir)
}

func (m *Manager) RemoveWorktree(ctx context.Context, input removeWorktreeRequest) error {
	release, err := m.admission.metadata.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	registrationDone, err := m.acquireWorktreeRegistration(ctx)
	if err != nil {
		return err
	}
	defer registrationDone()
	dir, err := absoluteDirectory(input.Path)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(input.Worktree) {
		return errors.New("worktree must be absolute")
	}
	target := filepath.Clean(input.Worktree)
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listed, err := DiscoverWorktrees(ctx, dir)
	if err != nil {
		return err
	}
	var selected *discoveredWorktree
	for i := range listed.Worktrees {
		if listed.Worktrees[i].Path != target {
			continue
		}
		if i == 0 {
			return errors.New("cannot remove the primary worktree")
		}
		selected = &listed.Worktrees[i]
		break
	}
	if selected == nil {
		return errors.New("worktree is not registered in this repository")
	}
	runtimes, err := m.store.List(ctx)
	if err != nil {
		return err
	}
	for _, runtime := range runtimes {
		if runtime.Worktree == target {
			return errors.New("destroy the worktree's registered environments before removing it")
		}
	}
	if selected.Missing {
		// Git also marks a registration prunable when only its .git marker
		// disappeared. Remaining files cannot be checked for local changes.
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("worktree folder still exists but its Git metadata is missing or invalid: %s; preserve or move the folder before removing its worktree registration", target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect unavailable worktree folder: %w", err)
		}
	} else {
		status, err := worktreeGit(ctx, target, "status", "--porcelain=v1", "--untracked-files=all", "--ignored")
		if err != nil {
			return err
		}
		if status != "" {
			return worktreeRemovalFilesError(status)
		}
	}
	// No force flag: Git retains its own dirty, locked, and submodule checks.
	_, err = worktreeGit(ctx, dir, "worktree", "remove", "--", target)
	return err
}

func worktreeRemovalFilesError(status string) error {
	var message strings.Builder
	message.WriteString("cannot remove a worktree with uncommitted, untracked, or ignored files\n\nLocal files:\n")
	lines := strings.Split(strings.TrimSuffix(status, "\n"), "\n")
	for i, line := range lines {
		if i == 8 {
			message.WriteString("- Additional entries omitted; review the folder for the full list.\n")
			break
		}
		if len(line) < 4 {
			continue
		}
		kind := "Changed"
		switch line[:2] {
		case "??":
			kind = "Untracked"
		case "!!":
			kind = "Ignored"
		}
		name := []rune(line[3:]) // Porcelain quotes control characters in filenames.
		if len(name) > 240 {
			name = append(name[:240], '…')
		}
		fmt.Fprintf(&message, "- %s: %s\n", kind, string(name))
	}
	message.WriteString("\nCommit or stash tracked changes. Move untracked or ignored files you want to keep outside this worktree, or delete them yourself if no longer needed. Then try removal again. Ignored files may contain local configuration or secrets.")
	return errors.New(message.String())
}
