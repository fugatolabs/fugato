package fugato

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryPath(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"main checkout", "deleted linked worktree", "separate Git directory", "relative checkout configuration", "bare repository", "deleted metadata"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			main := filepath.Join(base, "main checkout")
			common := filepath.Join(main, ".git")
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, output)
				}
			}
			switch name {
			case "separate Git directory", "relative checkout configuration":
				common = filepath.Join(base, "metadata")
				git("init", "-q", "--separate-git-dir", common, main)
				configured := main
				if name == "relative checkout configuration" {
					configured = "../main checkout"
				}
				git("--git-dir", common, "config", "core.worktree", configured)
			case "bare repository":
				common = filepath.Join(base, "bare.git")
				git("init", "-q", "--bare", common)
				main = common
			default:
				git("init", "-q", main)
			}
			if name == "deleted linked worktree" {
				git("-C", main, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
				linked := filepath.Join(base, "feature")
				git("-C", main, "worktree", "add", "--detach", linked)
				// The same common directory is saved for a linked runtime.
				cmd := exec.Command("git", "-C", linked, "rev-parse", "--git-common-dir")
				out, err := cmd.Output()
				if err != nil || strings.TrimSpace(string(out)) != common {
					t.Fatalf("linked identity: %q, %v", out, err)
				}
				if err := os.RemoveAll(linked); err != nil {
					t.Fatal(err)
				}
			}
			if name == "deleted metadata" {
				if err := os.RemoveAll(main); err != nil {
					t.Fatal(err)
				}
			}
			if got := RepositoryPath(t.Context(), common); got != main {
				t.Fatalf("RepositoryPath(%q) = %q, want %q", common, got, main)
			}
		})
	}
}
