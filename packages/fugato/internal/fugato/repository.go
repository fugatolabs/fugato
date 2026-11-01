package fugato

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RepositoryPath resolves a saved Git common directory to its main checkout for
// display, independently of the runtime's worktree or the caller's directory.
// If Git metadata is unavailable, ordinary .git directories still identify the
// main checkout; other layouts retain the saved Git directory as a fallback.
// This display path must not be used as a replacement for repository identity.
func RepositoryPath(ctx context.Context, common string) string {
	if common == "" {
		return ""
	}
	fallback := common
	if filepath.Base(common) == ".git" {
		fallback = filepath.Dir(common)
	}
	if !filepath.IsAbs(common) {
		return fallback
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Submodules and separate Git directories can place metadata away from the
	// checkout. core.worktree, when set, is relative to the Git directory.
	var configured cappedGitOutput
	config := exec.CommandContext(ctx, "git", "--git-dir", common, "config", "--null", "--path", "--get", "core.worktree")
	config.Stdout = &configured
	if err := config.Run(); err == nil {
		root := strings.TrimSuffix(configured.String(), "\x00")
		if root != "" {
			if !filepath.IsAbs(root) {
				root = filepath.Join(common, root)
			}
			return filepath.Clean(root)
		}
	}
	var output cappedGitOutput
	cmd := exec.CommandContext(ctx, "git", "--git-dir", common, "worktree", "list", "--porcelain", "-z")
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return fallback
	}
	first, _, _ := strings.Cut(output.String(), "\x00")
	if root, ok := strings.CutPrefix(first, "worktree "); ok && filepath.IsAbs(root) {
		return root
	}
	return fallback
}
