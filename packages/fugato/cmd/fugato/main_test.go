package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/fugato"
)

func TestRunHelpAndUnknownCommandDoNotStartDaemon(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
		err  bool
	}{
		{name: "root help", args: []string{"--help"}, want: "Commands:"},
		{name: "help command", args: []string{"help", "doctor"}, want: "--json"},
		{name: "command help", args: []string{"doctor", "--help"}, want: "--json"},
		{name: "daemon help", args: []string{"daemon", "--help"}, want: "foreground"},
		{name: "unknown", args: []string{"missing"}, err: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stateDir := filepath.Join(t.TempDir(), "state")
			t.Setenv("FUGATO_STATE_DIR", stateDir)
			var output bytes.Buffer
			err := runTo(tt.args, &output)
			if (err != nil) != tt.err {
				t.Fatalf("runTo(%v) error = %v, wantErr = %v", tt.args, err, tt.err)
			}
			if !tt.err && !strings.Contains(output.String(), tt.want) {
				t.Fatalf("output %q does not contain %q", output.String(), tt.want)
			}
			if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
				t.Fatalf("help or unknown command created daemon state directory: %v", err)
			}
		})
	}
}

func TestRunDoctorJSONProducesReportOnBlockingError(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Setenv("FUGATO_STATE_DIR", stateDir)
	var output bytes.Buffer
	err := runTo([]string{"doctor", "--json", "--worktree", filepath.Join(t.TempDir(), "missing")}, &output)
	if err == nil {
		t.Fatal("expected doctor failure")
	}
	var report doctorJSONReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("doctor stdout is not valid JSON: %v: %s", err, output.String())
	}
	if report.SchemaVersion != 1 || report.OK || report.Project != nil || len(report.Findings) != 1 || report.Findings[0].Code != "PROJECT_DISCOVERY" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("doctor started daemon: %v", err)
	}
}

func TestPrintRuntimesShowsLifecycleAndBackend(t *testing.T) {
	var output bytes.Buffer
	items := []fugato.Runtime{{ID: "aabbccddeeff0011", Desired: "running", Observed: "stopped", Backend: "compose", ProjectDir: "/tmp/worktree"}}
	if err := printRuntimes(t.Context(), &output, items); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"DESIRED", "OBSERVED", "BACKEND", "running", "stopped", "compose", "/tmp/worktree"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("missing %q from ls output: %s", value, output.String())
		}
	}
}

func TestLogClientRequiresSuccessfulTrailer(t *testing.T) {
	for _, tt := range []struct {
		name, status string
		wantError    bool
	}{
		{"success", "ok", false}, {"late failure", "error", true}, {"truncated protocol", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.status != "" {
					w.Header().Set("Trailer", fugato.LogStatusTrailer)
				}
				_, _ = io.WriteString(w, "line\n")
				if tt.status != "" {
					w.Header().Set(fugato.LogStatusTrailer, tt.status)
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "token"), []byte("token"), 0600); err != nil {
				t.Fatal(err)
			}
			client := localClient{settings: fugato.Settings{StateDir: dir, APIAddr: strings.TrimPrefix(server.URL, "http://")}}
			var output bytes.Buffer
			err := client.stream(t.Context(), "/logs", &output)
			if (err != nil) != tt.wantError {
				t.Fatalf("stream: %v", err)
			}
			if output.String() != "line\n" {
				t.Fatalf("output %q", output.String())
			}
		})
	}
}
func TestUnaryClientHonorsEarlierDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("token"), 0600); err != nil {
		t.Fatal(err)
	}
	client := localClient{settings: fugato.Settings{StateDir: dir, APIAddr: strings.TrimPrefix(server.URL, "http://")}}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	var output any
	if err := client.request(ctx, "GET", "/", nil, &output); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request: %v", err)
	}
}
