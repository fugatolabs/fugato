package fugato

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

func prepareFixture(t *testing.T, manifest string) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, target := filepath.Join(base, "source"), filepath.Join(base, "target")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "prepare@example.invalid"}, {"config", "user.name", "Prepare Test"}} {
		if _, err := gitOutput(t.Context(), source, args...); err != nil {
			t.Fatal(err)
		}
	}
	prepareWrite(t, source, ".fugato.yaml", "version: 1\n"+manifest)
	if _, err := gitOutput(t.Context(), source, "add", ".fugato.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(t.Context(), source, "commit", "-qm", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(t.Context(), source, "worktree", "add", "-qb", "target", target); err != nil {
		t.Fatal(err)
	}
	return source, target
}

func prepareWrite(t *testing.T, root, name, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func prepareStatus(t *testing.T, report control.PrepareReport, name string) string {
	t.Helper()
	for _, entry := range report.Entries {
		if entry.Path == name {
			return entry.Status
		}
	}
	t.Fatalf("entry %q missing from %#v", name, report.Entries)
	return ""
}

func TestPrepareFilesDryRunAndCopy(t *testing.T) {
	source, target := prepareFixture(t, `prepare:
  files:
    - path: .env.local
    - path: secrets
    - path: absent
      required: false
`)
	prepareWrite(t, source, ".env.local", "SECRET=value\n")
	prepareWrite(t, source, "secrets/config.json", "private")
	prepareWrite(t, source, "secrets/node_modules/pkg/token", "excluded")
	report, err := PrepareFiles(t.Context(), target, source, true)
	if err != nil || !report.OK {
		t.Fatalf("dry run: %+v %v", report, err)
	}
	if prepareStatus(t, report, ".env.local") != "would_copy" || prepareStatus(t, report, "secrets/node_modules") != "skipped" {
		t.Fatal(report.Entries)
	}
	if _, err := os.Stat(filepath.Join(target, "secrets")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created directory: %v", err)
	}
	report, err = PrepareFiles(t.Context(), target, source, false)
	if err != nil || !report.OK {
		t.Fatalf("copy: %+v %v", report, err)
	}
	if prepareStatus(t, report, ".env.local") != "copied" || prepareStatus(t, report, "absent") != "missing" {
		t.Fatal(report.Entries)
	}
	data, err := os.ReadFile(filepath.Join(target, "secrets/config.json"))
	if err != nil || string(data) != "private" {
		t.Fatalf("file: %s %v", data, err)
	}
	for name, want := range map[string]os.FileMode{".env.local": 0600, "secrets": 0700} {
		info, err := os.Stat(filepath.Join(target, name))
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("permissions %s: %v %v", name, info, err)
		}
	}
	prepareWrite(t, target, ".env.local", "keep")
	report, err = PrepareFiles(t.Context(), target, source, false)
	if err != nil || prepareStatus(t, report, ".env.local") != "existing" {
		t.Fatalf("rerun: %+v %v", report, err)
	}
	data, _ = os.ReadFile(filepath.Join(target, ".env.local"))
	if string(data) != "keep" {
		t.Fatal("existing file overwritten")
	}
}

func TestPrepareFilesRequiredPreflight(t *testing.T) {
	source, target := prepareFixture(t, `prepare:
  files:
    - path: first
    - path: missing
`)
	prepareWrite(t, source, "first", "must not copy")
	report, err := PrepareFiles(t.Context(), target, source, false)
	if err == nil || report.OK || prepareStatus(t, report, "missing") != "missing" {
		t.Fatalf("required: %+v %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(target, "first")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight copied file: %v", err)
	}
	prepareWrite(t, target, "missing", "already configured")
	report, err = PrepareFiles(t.Context(), target, source, false)
	if err != nil || !report.OK || prepareStatus(t, report, "missing") != "existing" {
		t.Fatalf("existing satisfies required: %+v %v", report, err)
	}
}

func TestPrepareFilesFilters(t *testing.T) {
	source, target := prepareFixture(t, `prepare:
  files:
    - path: local
  allow: [local/node_modules/selected]
  exclude: ["local/node_modules/selected/private?", "local/**/omit*"]
`)
	for _, name := range []string{"local/node_modules/selected/keep", "local/node_modules/selected/private1", "local/node_modules/other/token", "local/deep/omit.txt", "local/normal", "local/.git/config"} {
		prepareWrite(t, source, name, "value")
	}
	report, err := PrepareFiles(t.Context(), target, source, false)
	if err != nil {
		t.Fatalf("filters: %+v %v", report, err)
	}
	for name, want := range map[string]string{"local/node_modules/selected/keep": "copied", "local/node_modules/selected/private1": "skipped", "local/node_modules/other/token": "skipped", "local/deep/omit.txt": "skipped", "local/.git": "skipped"} {
		if got := prepareStatus(t, report, name); got != want {
			t.Fatalf("%s: %s != %s", name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "local/.git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Git metadata copied")
	}
}

func TestPrepareFilesRejectsSymlinksBeforeWriting(t *testing.T) {
	for _, side := range []string{"source file", "source parent", "target file", "target parent"} {
		t.Run(side, func(t *testing.T) {
			source, target := prepareFixture(t, `prepare:
  files:
    - path: safe
    - path: local/token
`)
			prepareWrite(t, source, "safe", "safe")
			prepareWrite(t, source, "local/token", "secret")
			outside := t.TempDir()
			prepareWrite(t, outside, "token", "outside")
			switch side {
			case "source file":
				if err := os.Remove(filepath.Join(source, "local/token")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "token"), filepath.Join(source, "local/token")); err != nil {
					t.Fatal(err)
				}
			case "source parent":
				if err := os.RemoveAll(filepath.Join(source, "local")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(source, "local")); err != nil {
					t.Fatal(err)
				}
			case "target file":
				if err := os.Mkdir(filepath.Join(target, "local"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "token"), filepath.Join(target, "local/token")); err != nil {
					t.Fatal(err)
				}
			case "target parent":
				if err := os.Symlink(outside, filepath.Join(target, "local")); err != nil {
					t.Fatal(err)
				}
			}
			report, err := PrepareFiles(t.Context(), target, source, false)
			if err == nil || report.OK {
				t.Fatalf("unsafe path accepted: %+v %v", report, err)
			}
			if _, err := os.Stat(filepath.Join(target, "safe")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("preflight wrote safe file")
			}
			data, _ := os.ReadFile(filepath.Join(outside, "token"))
			if string(data) != "outside" {
				t.Fatal("external file changed")
			}
		})
	}
}

func TestPrepareFilesRepositoryAndSourceSelection(t *testing.T) {
	source, target := prepareFixture(t, "prepare:\n  files: [{path: token}]\n")
	prepareWrite(t, source, "token", "source")
	other := nativeTestProject(t, "version: 1\n")
	for _, from := range []string{"", other, filepath.Join(source, "absent")} {
		report, err := PrepareFiles(t.Context(), target, from, false)
		if err == nil || report.OK {
			t.Fatalf("source %q accepted: %+v %v", from, report, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if report, err := PrepareFiles(ctx, target, source, false); !errors.Is(err, context.Canceled) || report.OK {
		t.Fatalf("cancel: %+v %v", report, err)
	}
}

func TestPrepareFilesRequiredExcludedRoot(t *testing.T) {
	source, target := prepareFixture(t, "prepare:\n  files: [{path: node_modules}]\n")
	prepareWrite(t, source, "node_modules/token", "secret")
	report, err := PrepareFiles(t.Context(), target, source, false)
	if err == nil || report.OK || prepareStatus(t, report, "node_modules") != "blocked" {
		t.Fatalf("excluded required: %+v %v", report, err)
	}
}

// Err interposes synchronously at copy phase boundaries, so these tests prove
// ordering without timing assumptions, goroutines, or global test hooks.
type prepareInterposedContext struct {
	context.Context
	checks    int
	interpose func(int)
}

func (c *prepareInterposedContext) Err() error {
	c.checks++
	c.interpose(c.checks)
	return c.Context.Err()
}

func TestPreparePublishInterleavings(t *testing.T) {
	for _, scenario := range []string{"destination appears", "source replaced", "target parent replaced", "target root replaced", "cancel during copy"} {
		t.Run(scenario, func(t *testing.T) {
			source, target := t.TempDir(), t.TempDir()
			prepareWrite(t, source, "local/token", "original")
			if err := os.Mkdir(filepath.Join(target, "local"), 0700); err != nil {
				t.Fatal(err)
			}
			src, err := os.OpenRoot(source)
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			dst, err := os.OpenRoot(target)
			if err != nil {
				t.Fatal(err)
			}
			defer dst.Close()
			info, err := prepareLstat(src, "local/token")
			if err != nil {
				t.Fatal(err)
			}
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			outside := t.TempDir()
			ctx := &prepareInterposedContext{Context: base, interpose: func(check int) {
				if scenario == "cancel during copy" && check == 2 {
					cancel()
				}
				if check != 4 {
					return
				}
				switch scenario {
				case "destination appears":
					prepareWrite(t, target, "local/token", "preserve")
				case "source replaced":
					if err := os.Rename(filepath.Join(source, "local/token"), filepath.Join(source, "old")); err != nil {
						t.Fatal(err)
					}
					prepareWrite(t, source, "local/token", "changed!")
				case "target root replaced":
					if err := os.Rename(target, filepath.Join(outside, "moved")); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(target, 0700); err != nil {
						t.Fatal(err)
					}
				case "target parent replaced":
					if err := os.Rename(filepath.Join(target, "local"), filepath.Join(target, "old")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, filepath.Join(target, "local")); err != nil {
						t.Fatal(err)
					}
				}
			}}
			status, err := preparePublish(ctx, src, dst, prepareItem{name: "local/token", info: info})
			if scenario == "destination appears" {
				if err != nil || status != "existing" {
					t.Fatalf("publish: %s %v", status, err)
				}
				data, _ := os.ReadFile(filepath.Join(target, "local/token"))
				if string(data) != "preserve" {
					t.Fatal("clobbered existing destination")
				}
			} else if err == nil {
				t.Fatal("interleaving did not fail")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 && scenario != "target root replaced" {
				t.Fatal("wrote outside target")
			}
			if scenario == "target root replaced" {
				if _, err := os.Stat(filepath.Join(outside, "moved/local/token")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("published after target root moved")
				}
			}
			if scenario != "target parent replaced" && scenario != "target root replaced" {
				entries, _ := os.ReadDir(filepath.Join(target, "local"))
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".fugato-prepare-") {
						t.Fatal("temporary file leaked")
					}
				}
			}
		})
	}
}

func TestPrepareFilesExistingFileDirectoryConflict(t *testing.T) {
	source, target := prepareFixture(t, "prepare:\n  files: [{path: local}]\n")
	prepareWrite(t, source, "local/token", "source")
	prepareWrite(t, target, "local", "preserve")
	report, err := PrepareFiles(t.Context(), target, source, false)
	if err == nil || report.OK || prepareStatus(t, report, "local") != "blocked" {
		t.Fatalf("conflict: %+v %v", report, err)
	}
	data, _ := os.ReadFile(filepath.Join(target, "local"))
	if string(data) != "preserve" {
		t.Fatal("existing file changed")
	}
}

func TestPrepareFilesExecutableOwnerOnly(t *testing.T) {
	source, target := prepareFixture(t, "prepare:\n  files: [{path: run-local}]\n")
	prepareWrite(t, source, "run-local", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(source, "run-local"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareFiles(t.Context(), target, source, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(target, "run-local"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("permissions: %v %v", info, err)
	}
}

func TestPrepareFilesSizeLimitsPreflight(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files int
		size  int64
	}{
		{"one oversized file", 1, prepareMaxFileBytes + 1},
		{"oversized aggregate", 5, prepareMaxFileBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, target := prepareFixture(t, "prepare:\n  files: [{path: local}]\n")
			for i := 0; i < tc.files; i++ {
				name := filepath.Join(source, "local", string(rune('a'+i)))
				if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
					t.Fatal(err)
				}
				f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.Truncate(tc.size); err != nil {
					f.Close()
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			report, err := PrepareFiles(t.Context(), target, source, false)
			if err == nil || report.OK {
				t.Fatalf("limit not enforced: %+v %v", report, err)
			}
			if _, err := os.Stat(filepath.Join(target, "local")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("limit failure created target directory")
			}
		})
	}
}

func TestPrepareFilesBootstrapsHostWorkingDirectory(t *testing.T) {
	source, target := prepareFixture(t, `prepare:
  files: [{path: local}]
commands:
  worker:
    mode: task
    host:
      cwd: local
      argv: ["worker"]
`)
	prepareWrite(t, source, "local/settings", "private")
	if _, err := DiscoverProject(t.Context(), target, ""); err == nil {
		t.Fatal("ordinary discovery accepted missing host.cwd")
	}
	report, err := PrepareFiles(t.Context(), target, source, true)
	if err != nil || !report.OK {
		t.Fatalf("bootstrap dry-run: %+v %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(target, "local")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run created host working directory")
	}
	report, err = PrepareFiles(t.Context(), target, source, false)
	if err != nil || !report.OK {
		t.Fatalf("bootstrap: %+v %v", report, err)
	}
	if _, err := DiscoverProject(t.Context(), target, ""); err != nil {
		t.Fatalf("prepared host.cwd still invalid: %v", err)
	}
}

func TestPrepareFilesBootstrapsExplicitComposeFile(t *testing.T) {
	source, target := prepareFixture(t, `compose: local/compose.yaml
prepare:
  files: [{path: local/compose.yaml}]
`)
	prepareWrite(t, source, "local/compose.yaml", "services: {}\n")
	if _, err := DiscoverProject(t.Context(), target, ""); err == nil {
		t.Fatal("ordinary discovery accepted missing Compose file")
	}
	report, err := PrepareFiles(t.Context(), target, source, true)
	if err != nil || !report.OK {
		t.Fatalf("bootstrap dry-run: %+v %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(target, "local")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run created Compose parent")
	}
	report, err = PrepareFiles(t.Context(), target, source, false)
	if err != nil || !report.OK {
		t.Fatalf("bootstrap: %+v %v", report, err)
	}
	if _, err := DiscoverProject(t.Context(), target, ""); err != nil {
		t.Fatalf("prepared Compose file still invalid: %v", err)
	}
}

func TestPrepareFilesBootstrapPreservesRuntimePathBoundary(t *testing.T) {
	for _, kind := range []string{"compose", "host.cwd"} {
		t.Run(kind, func(t *testing.T) {
			manifest := "compose: local/compose.yaml\nprepare:\n  files: [{path: token}]\n"
			if kind == "host.cwd" {
				manifest = "prepare:\n  files: [{path: token}]\ncommands:\n  worker:\n    mode: task\n    host: {cwd: local, argv: [worker]}\n"
			}
			source, target := prepareFixture(t, manifest)
			prepareWrite(t, source, "token", "private")
			if err := os.Symlink(t.TempDir(), filepath.Join(target, "local")); err != nil {
				t.Fatal(err)
			}
			report, err := PrepareFiles(t.Context(), target, source, false)
			if err == nil || report.OK {
				t.Fatalf("escaping runtime input accepted: %+v %v", report, err)
			}
			if _, err := os.Stat(filepath.Join(target, "token")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("boundary failure copied file")
			}
		})
	}
}

func TestPrepareFilesReportBudgetPreventsCopy(t *testing.T) {
	source, target := prepareFixture(t, "prepare:\n  files: [{path: local}]\n")
	// HTML-sensitive characters exercise the encoder's six-byte escapes. Raw
	// path lengths fit comfortably below the budget; encoded JSON does not.
	folder := "local/" + strings.Repeat("<&", 110)
	for i := 0; i < 1700; i++ {
		prepareWrite(t, source, fmt.Sprintf("%s/%04d", folder, i), "LOCAL_SECRET_SENTINEL_a91bd3")
	}
	report, err := PrepareFiles(t.Context(), target, source, false)
	if err == nil || report.OK || !report.Truncated || !strings.Contains(report.Error, "2 MiB") {
		t.Fatalf("report budget: ok=%v truncated=%v error=%q err=%v", report.OK, report.Truncated, report.Error, err)
	}
	encoded, err := json.Marshal(report)
	if err != nil || len(encoded) > prepareMaxReportBytes {
		t.Fatalf("oversized failure report: bytes=%d err=%v", len(encoded), err)
	}
	if strings.Contains(string(encoded), "LOCAL_SECRET_SENTINEL_a91bd3") {
		t.Fatal("report exposed file contents")
	}
	if _, err := os.Stat(filepath.Join(target, "local")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("report limit failure copied files or directories")
	}
}
