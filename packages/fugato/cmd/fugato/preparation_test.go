package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

func preparationWorktrees(t *testing.T) (string, string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	for _, args := range [][]string{{"init", "-q", source}, {"-C", source, "config", "user.email", "test@example.invalid"}, {"-C", source, "config", "user.name", "Fugato Test"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	config := []byte("version: 1\nprepare:\n  files:\n    - path: .env.local\n")
	if err := os.WriteFile(filepath.Join(source, ".fugato.yaml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", source, "add", ".fugato.yaml"}, {"-C", source, "commit", "-qm", "fixture"}, {"-C", source, "worktree", "add", "-q", "-b", "target", target}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	return source, target
}

func TestPrepareCLIReportsDryRunAndCopiesWithoutDaemon(t *testing.T) {
	source, target := preparationWorktrees(t)
	secret := "LOCAL_SECRET_VALUE"
	if err := os.WriteFile(filepath.Join(source, ".env.local"), []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("FUGATO_STATE_DIR", state)
	t.Setenv("FUGATO_API_PORT", "invalid")
	for _, dryRun := range []bool{true, false} {
		var output bytes.Buffer
		args := []string{"prepare", "--worktree", target, "--from", source, "--json"}
		if dryRun {
			args = append(args, "--dry-run")
		}
		if err := runTo(args, &output); err != nil {
			t.Fatalf("prepare: %v: %s", err, output.String())
		}
		var report control.PrepareReport
		if err := json.Unmarshal(output.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		want := "copied"
		if dryRun {
			want = "would_copy"
		}
		if !report.OK || report.DryRun != dryRun || len(report.Entries) != 1 || report.Entries[0].Status != want {
			t.Fatalf("report=%+v", report)
		}
		if strings.Contains(output.String(), secret) {
			t.Fatalf("content leaked: %s", output.String())
		}
		data, err := os.ReadFile(filepath.Join(target, ".env.local"))
		if dryRun {
			if !os.IsNotExist(err) {
				t.Fatalf("dry run wrote target: %v", err)
			}
		} else if err != nil || string(data) != secret {
			t.Fatalf("copy=%q err=%v", data, err)
		}
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("prepare created daemon state: %v", err)
	}
}

func TestPrepareCLIBlockedPreservesJSONAndCleanFlagErrors(t *testing.T) {
	source, target := preparationWorktrees(t)
	var output bytes.Buffer
	if err := runTo([]string{"prepare", "--worktree", target, "--from", source, "--json"}, &output); err == nil {
		t.Fatal("missing required file accepted")
	}
	var report control.PrepareReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("invalid report: %v: %s", err, output.String())
	}
	if report.SchemaVersion != 1 || report.OK || len(report.Entries) != 1 || report.Entries[0].Status != "missing" {
		t.Fatalf("report=%+v", report)
	}
	for _, args := range [][]string{{"prepare"}, {"prepare", "--from"}, {"prepare", "--from", source, "--unknown"}, {"prepare", "--from", source, "extra"}, {"prepare", "--dry-run=invalid", "--from", source}} {
		output.Reset()
		if err := runTo(args, &output); err == nil || output.Len() != 0 {
			t.Fatalf("args=%v error=%v output=%s", args, err, output.String())
		}
	}
}

func TestPrepareCLITextContainsOnlyPathAndStatus(t *testing.T) {
	source, target := preparationWorktrees(t)
	if err := os.WriteFile(filepath.Join(source, ".env.local"), []byte("LOCAL_SECRET_VALUE"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	t.Chdir(target)
	if err := runTo([]string{"prepare", "--from", source, "--dry-run"}, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != ".env.local  would_copy\n" {
		t.Fatalf("output=%q", output.String())
	}
}
