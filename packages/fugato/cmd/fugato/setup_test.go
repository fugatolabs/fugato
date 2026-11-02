package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupCLI(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	project := filepath.Join(root, "project")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fugato", "fugato-mcp"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FUGATO_STATE_DIR", filepath.Join(root, "state"))
	var output bytes.Buffer
	args := []string{"setup", "--agent", "codex", "--project", project, "--bin-dir", binDir, "--json", "--dry-run"}
	if err := runTo(args, &output); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Agent  string   `json:"agent"`
		Files  []string `json:"files"`
		DryRun bool     `json:"dry_run"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("setup did not produce one JSON report: %s, %v", output.String(), err)
	}
	if report.Agent != "codex" || !report.DryRun || len(report.Files) == 0 {
		t.Fatalf("invalid report: %+v", report)
	}
	if entries, err := os.ReadDir(project); err != nil || len(entries) != 0 {
		t.Fatalf("dry run changed project: %v, %v", entries, err)
	}
	output.Reset()
	args = args[:len(args)-1]
	if err := runTo(args, &output); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || report.DryRun {
		t.Fatalf("invalid applied report: %s, %v", output.String(), err)
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatalf("setup started daemon or touched runtime state: %v", err)
	}
}

func TestSetupCLIHelpAndValidation(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"missing agent", []string{"setup"}, "choose --agent"},
		{"invalid agent", []string{"setup", "--agent", "unknown"}, "choose --agent"},
		{"positional argument", []string{"setup", "extra"}, "unexpected arguments"},
		{"unknown flag", []string{"setup", "--unknown"}, "flag provided but not defined"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := runTo(tt.args, &output); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("wanted %q, got %v", tt.want, err)
			}
			if output.Len() != 0 {
				t.Fatalf("error wrote stdout: %s", output.String())
			}
		})
	}
	var output bytes.Buffer
	if err := runTo([]string{"setup", "--help"}, &output); err != nil || !strings.Contains(output.String(), "does not require Node.js") {
		t.Fatalf("setup help: %s, %v", output.String(), err)
	}
}
