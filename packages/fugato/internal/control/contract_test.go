package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testID = "0123456789abcdef"

type testService struct {
	Service
	called bool
}

func (s *testService) Get(_ context.Context, id string) (Runtime, error) {
	s.called = true
	return Runtime{ID: id, Commit: "abc", Worktree: "/worktree"}, nil
}

func TestHandlerRequiresTokenAndRejectsOrigin(t *testing.T) {
	s := &testService{}
	h := Handler(s, "secret")
	for _, tc := range []struct {
		token, origin string
		status        int
	}{
		{"", "", 401}, {"Bearer secret", "http://evil.example", 403}, {"Bearer secret", "", 200},
	} {
		r := httptest.NewRequest(http.MethodGet, "/v1/agent/runtimes/"+testID, nil)
		r.Header.Set("Authorization", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status = %d, want %d", w.Code, tc.status)
		}
	}
	if !s.called {
		t.Fatal("authorized request did not reach service")
	}
}

func TestCommandPolicyDefaultsToDeny(t *testing.T) {
	r := CommandRequest{RuntimeID: testID, Service: "web", Argv: []string{"node", "test.js"}, TimeoutMS: 1000}
	if err := (CommandPolicy{}).Validate(r); err == nil {
		t.Fatal("default policy allowed execution")
	}
	p := CommandPolicy{Commands: map[string][][]string{"web": {{"node", "test.js"}}}}
	if err := p.Validate(r); err != nil {
		t.Fatal(err)
	}
	r.Argv = []string{"node", "-e", "process.exit(0)"}
	if err := p.Validate(r); err == nil {
		t.Fatal("unlisted arguments accepted")
	}
	r.Argv = []string{"node", "test.js"}
	r.Dir = "../other"
	if err := p.Validate(r); err == nil {
		t.Fatal("directory escape accepted")
	}
}

func TestVerifyPathRejectsExternalAndTraversal(t *testing.T) {
	for _, path := range []string{"//host/path", "/../secret", "/%2e%2e/secret", "https://host/", "/a\\b"} {
		if err := ValidateVerify(VerifyRequest{RuntimeID: testID, Path: path}); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	if err := ValidateVerify(VerifyRequest{RuntimeID: testID, Path: "/health?detail=1"}); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsNonLoopback(t *testing.T) {
	for _, base := range []string{"https://127.0.0.1:1234", "http://example.com:1234", "http://127.0.0.1:1234/path"} {
		if _, err := NewClient(base, "token"); err == nil {
			t.Errorf("accepted %s", base)
		}
	}
	if _, err := NewClient("http://127.0.0.1:1234", "token"); err != nil {
		t.Fatal(err)
	}
}
