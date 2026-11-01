package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fugatolabs/fugato/packages/fugato/internal/fugato"
)

func TestPrintRuntimesGroupsRepositories(t *testing.T) {
	t.Parallel()
	// Missing checkout metadata must not prevent displaying or sorting rows.
	base := t.TempDir()
	repoA, repoB := filepath.Join(base, "a"), filepath.Join(base, "b")
	items := []fugato.Runtime{
		{ID: "b", Repository: filepath.Join(repoB, ".git"), ProjectDir: "/feature/a"},
		{ID: "a2", Repository: filepath.Join(repoA, ".git"), ProjectDir: "/feature/z"},
		{ID: "a1", Repository: filepath.Join(repoA, ".git"), ProjectDir: "/feature/a"},
	}
	var output bytes.Buffer
	if err := printRuntimes(t.Context(), &output, items); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 || !strings.Contains(lines[0], "REPOSITORY") || !strings.Contains(lines[0], "WORKTREE") {
		t.Fatalf("unexpected table: %s", output.String())
	}
	for i, want := range []struct{ id, repository, project string }{
		{"a1", repoA, "/feature/a"}, {"a2", repoA, "/feature/z"}, {"b", repoB, "/feature/a"},
	} {
		row := lines[i+1]
		if strings.Fields(row)[0] != want.id || !strings.Contains(row, want.repository) || !strings.Contains(row, want.project) {
			t.Fatalf("unexpected row %d: %s", i, row)
		}
	}
	if items[0].ID != "b" {
		t.Fatal("printing changed the caller's runtime order")
	}
}

func TestPrintRuntimesEmptyAndCanceled(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	if err := printRuntimes(t.Context(), &output, nil); err != nil || !strings.Contains(output.String(), "REPOSITORY") {
		t.Fatalf("empty listing: %q, %v", output.String(), err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output.Reset()
	if err := printRuntimes(ctx, &output, nil); !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatalf("canceled listing: %q, %v", output.String(), err)
	}
}
