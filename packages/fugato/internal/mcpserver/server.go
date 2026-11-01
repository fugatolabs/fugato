// Package mcpserver exposes Fugato's daemon API over the MCP stdio transport.
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
	"github.com/fugatolabs/fugato/packages/fugato/internal/version"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

func schema(properties map[string]any, required ...string) any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func field(description string) any {
	return map[string]any{"type": "string", "description": description}
}
func integer(description string) any {
	return map[string]any{"type": "integer", "description": description}
}

var tools = []tool{
	{"prepare_worktree", "Copy declared local files into an existing worktree before registration. Dry-run reports without writing; existing files are preserved.", schema(map[string]any{"path": field("Absolute target project directory path"), "from": field("Absolute source project directory path"), "dry_run": map[string]any{"type": "boolean", "description": "Report without writing files"}}, "path", "from")},
	{"list_runtimes", "List registered worktree runtimes and their commit IDs.", schema(map[string]any{})},
	{"get_runtime", "Inspect one registered runtime.", schema(map[string]any{"runtime_id": field("16-character runtime ID")}, "runtime_id")},
	{"start_runtime", "Start an existing registered runtime.", schema(map[string]any{"runtime_id": field("16-character runtime ID")}, "runtime_id")},
	{"stop_runtime", "Stop a runtime while retaining its data.", schema(map[string]any{"runtime_id": field("16-character runtime ID")}, "runtime_id")},
	{"get_preview_url", "Get a healthy runtime's stable preview URL.", schema(map[string]any{"runtime_id": field("16-character runtime ID")}, "runtime_id")},
	{"get_logs", "Read bounded recent logs for one runtime.", schema(map[string]any{"runtime_id": field("16-character runtime ID"), "lines": integer("1 to 200 lines")}, "runtime_id")},
	{"run_command", "Run an argv command in an allowed service under daemon policy.", schema(map[string]any{"runtime_id": field("16-character runtime ID"), "service": field("service name"), "argv": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "timeout_ms": integer("1 to 30000 milliseconds")}, "runtime_id", "service", "argv")},
	{"verify_http", "Check an HTTP path in the selected runtime.", schema(map[string]any{"runtime_id": field("16-character runtime ID"), "endpoint": field("declared HTTP endpoint name"), "path": field("relative URL path"), "timeout_ms": integer("maximum 60000 milliseconds")}, "runtime_id")},
	{"verify_browser", "Run a fresh browser context and return screenshot, page, request and log evidence.", schema(map[string]any{"runtime_id": field("16-character runtime ID"), "endpoint": field("declared HTTP endpoint name"), "path": field("relative URL path"), "timeout_ms": integer("maximum 60000 milliseconds")}, "runtime_id")},
}

type Server struct{ Service control.Service }

// Run handles one newline-delimited JSON-RPC message at a time. stdout is
// reserved for protocol frames; callers must send diagnostics to stderr.
func (s Server) Run(ctx context.Context, input io.Reader, output io.Writer) error {
	if s.Service == nil {
		return fmt.Errorf("MCP service is nil")
	}
	scan := bufio.NewScanner(input)
	scan.Buffer(make([]byte, 4096), 1<<20)
	enc := json.NewEncoder(output)
	initialized := false
	for scan.Scan() {
		line := scan.Bytes()
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			if err := enc.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}}); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue
		} // notifications have no response
		res := response{JSONRPC: "2.0", ID: req.ID}
		switch {
		case req.JSONRPC != "2.0":
			res.Error = &rpcError{-32600, "invalid JSON-RPC version"}
		case req.Method == "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if json.Unmarshal(req.Params, &params) != nil {
				res.Error = &rpcError{-32602, "invalid initialize parameters"}
				break
			}
			initialized = true
			res.Result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]string{"name": "fugato", "version": version.String()}}
		case !initialized:
			res.Error = &rpcError{-32000, "initialize first"}
		case req.Method == "ping":
			res.Result = map[string]any{}
		case req.Method == "tools/list":
			res.Result = map[string]any{"tools": tools}
		case req.Method == "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(req.Params, &params) != nil {
				res.Error = &rpcError{-32602, "invalid tool call"}
				break
			}
			known := false
			for _, t := range tools {
				if t.Name == params.Name {
					known = true
					break
				}
			}
			if !known {
				res.Error = &rpcError{-32602, "unknown tool"}
				break
			}
			callCtx, cancel := context.WithTimeout(ctx, 65*time.Second)
			value, err := s.call(callCtx, params.Name, params.Arguments)
			cancel()
			if err != nil {
				result := map[string]any{"content": []any{map[string]string{"type": "text", "text": err.Error()}}, "isError": true}
				if len(value) > 0 {
					result["structuredContent"] = json.RawMessage(value)
					result["content"] = []any{map[string]string{"type": "text", "text": string(value)}}
				}
				res.Result = result
			} else {
				res.Result = map[string]any{"content": []any{map[string]string{"type": "text", "text": string(value)}}, "structuredContent": json.RawMessage(value), "isError": false}
			}
		default:
			res.Error = &rpcError{-32601, "method not found"}
		}
		if err := enc.Encode(res); err != nil {
			return err
		}
	}
	return scan.Err()
}

func strict(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("multiple argument objects")
	}
	return nil
}

func (s Server) call(ctx context.Context, name string, raw json.RawMessage) (json.RawMessage, error) {
	if name == "prepare_worktree" {
		var input control.PrepareRequest
		if err := strict(raw, &input); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if err := control.ValidatePrepare(input); err != nil {
			return nil, err
		}
		preparer, ok := s.Service.(control.PrepareService)
		if !ok {
			return nil, fmt.Errorf("preparation unavailable")
		}
		report, err := preparer.Prepare(ctx, input)
		if err != nil && report.SchemaVersion == 0 {
			return nil, err
		}
		value, marshalErr := json.Marshal(report)
		if marshalErr != nil {
			return nil, marshalErr
		}
		return value, err
	}
	var input struct {
		RuntimeID string   `json:"runtime_id"`
		Service   string   `json:"service"`
		Lines     int      `json:"lines"`
		Argv      []string `json:"argv"`
		Dir       string   `json:"dir"`
		Path      string   `json:"path"`
		TimeoutMS int      `json:"timeout_ms"`
		Endpoint  string   `json:"endpoint"`
	}
	if err := strict(raw, &input); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if name != "list_runtimes" {
		if err := control.ValidateID(input.RuntimeID); err != nil {
			return nil, err
		}
	}
	var value any
	var err error
	switch name {
	case "list_runtimes":
		items, listErr := s.Service.List(ctx)
		if items == nil {
			items = []control.Runtime{}
		}
		value, err = map[string]any{"runtimes": items}, listErr
	case "get_runtime", "get_preview_url":
		var r control.Runtime
		r, err = s.Service.Get(ctx, input.RuntimeID)
		if name == "get_preview_url" && err == nil && r.PreviewURL == "" {
			err = fmt.Errorf("runtime %s has no healthy preview", r.ID)
		}
		value = r
	case "start_runtime":
		value, err = s.Service.Start(ctx, input.RuntimeID)
	case "stop_runtime":
		value, err = s.Service.Stop(ctx, input.RuntimeID)
	case "get_logs":
		if input.Lines == 0 {
			input.Lines = 100
		}
		if input.Lines < 1 || input.Lines > 200 {
			return nil, fmt.Errorf("lines must be between 1 and 200")
		}
		value, err = s.Service.Logs(ctx, control.LogsRequest{RuntimeID: input.RuntimeID, Service: input.Service, Lines: input.Lines})
	case "run_command":
		if input.TimeoutMS == 0 {
			input.TimeoutMS = 10000
		}
		value, err = s.Service.RunCommand(ctx, control.CommandRequest{RuntimeID: input.RuntimeID, Service: input.Service, Argv: input.Argv, Dir: input.Dir, TimeoutMS: input.TimeoutMS})
	case "verify_http", "verify_browser":
		v := control.VerifyRequest{RuntimeID: input.RuntimeID, Endpoint: input.Endpoint, Path: input.Path, TimeoutMS: input.TimeoutMS}
		if err := control.ValidateVerify(v); err != nil {
			return nil, err
		}
		if name == "verify_http" {
			value, err = s.Service.VerifyHTTP(ctx, v)
		} else {
			value, err = s.Service.VerifyBrowser(ctx, v)
		}
	default:
		return nil, fmt.Errorf("unknown tool %q", strings.TrimSpace(name))
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
