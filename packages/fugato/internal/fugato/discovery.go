package fugato

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxListedWorktrees = 200

type discoveredWorktree struct {
	Path    string `json:"path"`
	Branch  string `json:"branch,omitempty"`
	Missing bool   `json:"missing"`
}

type repositoryWorktrees struct {
	Repository string               `json:"repository"`
	Worktrees  []discoveredWorktree `json:"worktrees"`
	Truncated  bool                 `json:"truncated"`
}

type resolvedProject struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Worktree   string `json:"worktree"`
	ProjectDir string `json:"project_dir"`
	Registered bool   `json:"registered"`
}

func absoluteDirectory(input string) (string, error) {
	if !filepath.IsAbs(input) {
		return "", errors.New("path must be absolute")
	}
	dir, err := filepath.EvalSymlinks(input)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", errors.New("path must name a directory")
	}
	return dir, nil
}

func (m *Manager) ResolveProjectMetadata(ctx context.Context, input string) (resolvedProject, error) {
	release, err := m.admission.metadata.acquire(ctx)
	if err != nil {
		return resolvedProject{}, err
	}
	defer release()
	if _, err := absoluteDirectory(input); err != nil {
		return resolvedProject{}, err
	}
	p, err := DiscoverProject(ctx, input, "")
	if err != nil {
		return resolvedProject{}, err
	}
	_, found, err := m.store.Get(ctx, p.ID)
	if err != nil {
		return resolvedProject{}, err
	}
	return resolvedProject{ID: p.ID, Repository: p.Repository, Worktree: p.Worktree, ProjectDir: p.Dir, Registered: found}, nil
}

func (m *Manager) RepositoryWorktrees(ctx context.Context, input string) (repositoryWorktrees, error) {
	release, err := m.admission.metadata.acquire(ctx)
	if err != nil {
		return repositoryWorktrees{}, err
	}
	defer release()
	return DiscoverWorktrees(ctx, input)
}

// DiscoverWorktrees lists Git worktrees without requiring or starting the daemon.
func DiscoverWorktrees(ctx context.Context, input string) (repositoryWorktrees, error) {
	dir, err := absoluteDirectory(input)
	if err != nil {
		return repositoryWorktrees{}, err
	}
	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	common, err := gitOutput(gitCtx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return repositoryWorktrees{}, fmt.Errorf("resolve Git repository identity: %w", err)
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return repositoryWorktrees{}, err
	}
	var out cappedGitOutput
	cmd := exec.CommandContext(gitCtx, "git", "worktree", "list", "--porcelain")
	cmd.Dir = dir
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return repositoryWorktrees{}, fmt.Errorf("list Git worktrees: %w", err)
	}
	result, err := parseWorktreeList(common, out.String())
	if err != nil {
		return repositoryWorktrees{}, err
	}
	return result, nil
}

func parseWorktreeList(common, output string) (repositoryWorktrees, error) {
	result := repositoryWorktrees{Repository: common, Worktrees: []discoveredWorktree{}}
	for _, record := range strings.Split(strings.TrimSpace(output), "\n\n") {
		if record == "" {
			continue
		}
		var item discoveredWorktree
		for _, line := range strings.Split(record, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				item.Path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "branch refs/heads/"):
				item.Branch = strings.TrimPrefix(line, "branch refs/heads/")
			case strings.HasPrefix(line, "prunable "):
				item.Missing = true
			}
		}
		if !filepath.IsAbs(item.Path) {
			return repositoryWorktrees{}, errors.New("Git returned an invalid worktree path")
		}
		item.Path = filepath.Clean(item.Path)
		if resolved, err := filepath.EvalSymlinks(item.Path); err == nil {
			item.Path = resolved
		} else {
			item.Missing = true
		}
		if len(result.Worktrees) == maxListedWorktrees {
			result.Truncated = true
			break
		}
		result.Worktrees = append(result.Worktrees, item)
	}
	return result, nil
}

type cappedGitOutput struct{ bytes.Buffer }

func (w *cappedGitOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > 1<<20 {
		return 0, errors.New("Git worktree list exceeds 1 MiB")
	}
	return w.Buffer.Write(p)
}
