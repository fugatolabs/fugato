package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type preparationService struct {
	Service
	input        PrepareRequest
	report       PrepareReport
	err          error
	calls        int
	contextValue any
}

type preparationContextKey struct{}

func (s *preparationService) Prepare(ctx context.Context, input PrepareRequest) (PrepareReport, error) {
	s.calls++
	s.input = input
	s.contextValue = ctx.Value(preparationContextKey{})
	return s.report, s.err
}

func TestPreparationHandlerRequiresAuthenticationAndAbsolutePaths(t *testing.T) {
	for _, tc := range []struct {
		name, token, origin, body string
		status                    int
	}{
		{"unauthenticated", "", "", `{"path":"/target","from":"/source"}`, 401},
		{"browser", "secret", "http://localhost", `{"path":"/target","from":"/source"}`, 403},
		{"relative target", "secret", "", `{"path":"target","from":"/source"}`, 400},
		{"missing source", "secret", "", `{"path":"/target"}`, 400},
		{"runtime id rejected", "secret", "", `{"path":"/target","from":"/source","runtime_id":"0123456789abcdef"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &preparationService{}
			request := httptest.NewRequest(http.MethodPost, "/v1/agent/prepare", strings.NewReader(tc.body))
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			request.Header.Set("Origin", tc.origin)
			response := httptest.NewRecorder()
			Handler(service, "secret").ServeHTTP(response, request)
			if response.Code != tc.status || service.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("token leaked: %s", response.Body.String())
			}
		})
	}
}

func TestPreparationClientPreservesBlockedReportWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		blocked bool
	}{{"dry run", false}, {"blocked", true}} {
		t.Run(tc.name, func(t *testing.T) {
			service := &preparationService{report: PrepareReport{SchemaVersion: 1, ProjectPath: "/target", SourcePath: "/source", DryRun: true, OK: !tc.blocked, Entries: []PrepareEntry{{Path: ".env.local", Status: "would_copy"}}}}
			if tc.blocked {
				service.report.Entries[0].Status = "missing"
				service.err = errors.New("sensitive internal failure")
			}
			client, err := NewClient("http://127.0.0.1:46655", "secret")
			if err != nil {
				t.Fatal(err)
			}
			handler := Handler(service, "secret")
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				return response.Result(), nil
			})
			ctx := context.WithValue(context.Background(), preparationContextKey{}, "request")
			report, err := client.Prepare(ctx, PrepareRequest{Path: "/target", From: "/source", DryRun: true})
			if (err != nil) != tc.blocked || report.SchemaVersion != 1 || report.OK == tc.blocked || !report.DryRun {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			if service.calls != 1 || !service.input.DryRun || service.contextValue != "request" {
				t.Fatalf("calls=%d input=%+v context=%v", service.calls, service.input, service.contextValue)
			}
			data, _ := json.Marshal(report)
			if strings.Contains(string(data), "sensitive") || err != nil && strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("internal detail leaked: %s %v", data, err)
			}
		})
	}
}

func TestPreparationClientRetainsCancellationAndDoesNotRetry(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:46655", "secret")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; return nil, r.Context().Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Prepare(ctx, PrepareRequest{Path: "/target", From: "/source"})
	if !errors.Is(err, context.Canceled) || calls > 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestPreparationHandlerReportsAdmissionOverload(t *testing.T) {
	service := &preparationService{err: ErrBusy}
	request := httptest.NewRequest(http.MethodPost, "/v1/agent/prepare", strings.NewReader(`{"path":"/target","from":"/source"}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	Handler(service, "secret").ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || service.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"report"`) {
		t.Fatalf("overload invented a preparation report: %s", response.Body.String())
	}
}

func TestPreparationClientPreservesLargeSuccessfulAndBlockedReports(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		name := "success"
		if blocked {
			name = "blocked"
		}
		t.Run(name, func(t *testing.T) {
			want := PrepareReport{SchemaVersion: 1, ProjectPath: "/target", SourcePath: "/source", OK: !blocked, Entries: make([]PrepareEntry, 9000)}
			for i := range want.Entries {
				want.Entries[i] = PrepareEntry{Path: fmt.Sprintf("local/%04d/%s", i, strings.Repeat("a", 240)), Status: "copied", Required: true}
			}
			status := http.StatusOK
			var body any = want
			if blocked {
				status = http.StatusUnprocessableEntity
				want.Error = "required file is missing"
				want.Entries[len(want.Entries)-1].Status = "missing"
				body = struct {
					Error  string        `json:"error"`
					Report PrepareReport `json:"report"`
				}{"preparation failed", want}
			}
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			if len(data) <= 2<<20 || len(data) >= 4<<20 {
				t.Fatalf("fixture report size = %d, want 2–4 MiB", len(data))
			}
			client, err := NewClient("http://127.0.0.1:46655", "secret")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != "/v1/agent/prepare" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Fatalf("unexpected preparation request: %s", r.URL.Path)
				}
				return testResponse(status, string(data)), nil
			})
			got, err := client.Prepare(context.Background(), PrepareRequest{Path: "/target", From: "/source"})
			if blocked {
				if err == nil || err.Error() != "daemon API: preparation failed" {
					t.Fatalf("blocked error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !reflect.DeepEqual(got, want) {
				t.Fatalf("report lost: calls=%d schema=%d entries=%d error=%q", calls, got.SchemaVersion, len(got.Entries), got.Error)
			}
		})
	}
}

func TestClientReportsResponseLimitWithoutDecodingPartialJSON(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare bool
		limit   int
	}{
		{"ordinary endpoint", false, 2 << 20},
		{"preparation endpoint", true, 8 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewClient("http://127.0.0.1:46655", "secret")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				// A valid object padded beyond the cap must be rejected explicitly.
				return testResponse(http.StatusOK, `{}`+strings.Repeat(" ", tc.limit)), nil
			})
			if tc.prepare {
				_, err = client.Prepare(context.Background(), PrepareRequest{Path: "/target", From: "/source"})
			} else {
				_, err = client.List(context.Background())
			}
			if err == nil || err.Error() != fmt.Sprintf("daemon API: response exceeds %d-byte limit", tc.limit) || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}
