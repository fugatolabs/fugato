package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

type fakeService struct{ control.Service }

func (fakeService) List(context.Context) ([]control.Runtime, error) {
	return []control.Runtime{{ID: "0123456789abcdef", Commit: "deadbeef", Worktree: "/tmp/worktree"}}, nil
}

func TestProtocolHandshakeListAndCall(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_runtimes","arguments":{}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := (Server{Service: fakeService{}}).Run(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d responses: %s", len(lines), output.String())
	}
	var list struct {
		Result struct {
			Tools []tool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Result.Tools) != 10 {
		t.Fatalf("got %d tools", len(list.Result.Tools))
	}
	var call struct {
		Result struct {
			StructuredContent struct {
				Runtimes []control.Runtime `json:"runtimes"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &call); err != nil {
		t.Fatal(err)
	}
	if call.Result.IsError || len(call.Result.StructuredContent.Runtimes) != 1 || call.Result.StructuredContent.Runtimes[0].Commit != "deadbeef" {
		t.Fatalf("unexpected tool result: %s", lines[2])
	}
}

func TestToolCallRequiresRuntimeID(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"stop_runtime","arguments":{}}}` + "\n"
	var output bytes.Buffer
	if err := (Server{Service: fakeService{}}).Run(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"isError":true`) {
		t.Fatal(output.String())
	}
}

type endpointService struct {
	control.Service
	requests []control.VerifyRequest
}

func (s *endpointService) VerifyHTTP(_ context.Context, input control.VerifyRequest) (control.VerifyResult, error) {
	s.requests = append(s.requests, input)
	return control.VerifyResult{OK: true}, nil
}
func (s *endpointService) VerifyBrowser(_ context.Context, input control.VerifyRequest) (control.VerifyResult, error) {
	s.requests = append(s.requests, input)
	return control.VerifyResult{OK: true}, nil
}
func TestVerificationPreservesExplicitEndpoint(t *testing.T) {
	service := &endpointService{}
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"
	for _, name := range []string{"verify_http", "verify_browser"} {
		input += `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"` + name + `","arguments":{"runtime_id":"0123456789abcdef","endpoint":"admin","path":"/health"}}}` + "\n"
	}
	var output bytes.Buffer
	if err := (Server{Service: service}).Run(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if len(service.requests) != 2 {
		t.Fatalf("verification calls missing: %s", output.String())
	}
	for _, request := range service.requests {
		if request.Endpoint != "admin" || request.Path != "/health" {
			t.Fatalf("lost target selection: %+v", request)
		}
	}
}
