package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

type preparationService struct {
	control.Service
	input   control.PrepareRequest
	calls   int
	blocked bool
}

func (s *preparationService) Prepare(ctx context.Context, input control.PrepareRequest) (control.PrepareReport, error) {
	s.calls++
	s.input = input
	if ctx.Err() != nil {
		return control.PrepareReport{}, ctx.Err()
	}
	report := control.PrepareReport{SchemaVersion: 1, ProjectPath: input.Path, SourcePath: input.From, DryRun: input.DryRun, OK: !s.blocked, Entries: []control.PrepareEntry{{Path: ".env.local", Status: "would_copy"}}}
	if s.blocked {
		report.Entries[0].Status = "missing"
		return report, errors.New("preparation blocked")
	}
	return report, nil
}

func TestPrepareToolBeforeRegistrationPreservesReports(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		service := &preparationService{blocked: blocked}
		input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"prepare_worktree","arguments":{"path":"/target","from":"/source","dry_run":true}}}` + "\n"
		var output bytes.Buffer
		if err := (Server{Service: service}).Run(context.Background(), strings.NewReader(input), &output); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		var response struct {
			Result struct {
				IsError bool                  `json:"isError"`
				Report  control.PrepareReport `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(lines[1]), &response); err != nil {
			t.Fatal(err)
		}
		if service.calls != 1 || !service.input.DryRun || response.Result.IsError != blocked || response.Result.Report.SchemaVersion != 1 || response.Result.Report.OK == blocked {
			t.Fatalf("response=%s calls=%d input=%+v", lines[1], service.calls, service.input)
		}
	}
}

func TestPrepareToolStrictArgumentsAndCancellation(t *testing.T) {
	service := &preparationService{}
	for _, args := range []string{`{"path":"relative","from":"/source"}`, `{"path":"/target"}`, `{"path":"/target","from":"/source","runtime_id":"0123456789abcdef"}`, `{"path":"/target","from":"/source","dry_run":"true"}`} {
		if _, err := (Server{Service: service}).call(context.Background(), "prepare_worktree", json.RawMessage(args)); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
	if service.calls != 0 {
		t.Fatalf("invalid arguments reached service %d times", service.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (Server{Service: service}).call(ctx, "prepare_worktree", json.RawMessage(`{"path":"/target","from":"/source"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
