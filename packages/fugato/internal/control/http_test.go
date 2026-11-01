package control

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

type busyService struct{ Service }

func (busyService) Get(context.Context, string) (Runtime, error) {
	return Runtime{}, fmt.Errorf("metadata: %w", ErrBusy)
}

func TestHandlerReportsOverloadAsUnavailable(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/agent/runtimes/"+testID, nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	Handler(busyService{}, "secret").ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
}

func writeTestToken(t *testing.T, path, token string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body))}
}

func TestFileClientReloadsTokenForEachRequest(t *testing.T) {
	oldToken := strings.Repeat("a", 64)
	newToken := strings.Repeat("b", 64)
	path := filepath.Join(t.TempDir(), "token")
	writeTestToken(t, path, oldToken)
	var expected atomic.Value
	expected.Store(oldToken)
	client, err := NewFileClient("http://127.0.0.1:46655", path)
	if err != nil {
		t.Fatal(err)
	}
	staleClient, err := NewClient("http://127.0.0.1:46655", oldToken)
	if err != nil {
		t.Fatal(err)
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+expected.Load().(string) {
			return testResponse(http.StatusUnauthorized, `{"error":"unauthorized"}`), nil
		}
		if r.Method == http.MethodGet {
			return testResponse(http.StatusOK, `[]`), nil
		}
		return testResponse(http.StatusOK, `{}`), nil
	})
	client.http.Transport = transport
	staleClient.http.Transport = transport
	if _, err := client.List(context.Background()); err != nil {
		t.Fatalf("initial request: %v", err)
	}
	replacement := filepath.Join(filepath.Dir(path), "replacement")
	writeTestToken(t, replacement, newToken)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	expected.Store(newToken)
	if _, err := staleClient.List(context.Background()); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("cached token request = %v, want unauthorized", err)
	}
	if _, err := client.List(context.Background()); err != nil {
		t.Fatalf("request after token replacement: %v", err)
	}
	if _, err := client.Start(context.Background(), testID); err != nil {
		t.Fatalf("mutation after token replacement: %v", err)
	}
}

func TestFileClientRejectsUnsafeTokenFile(t *testing.T) {
	token := strings.Repeat("a", 64)
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	writeTestToken(t, path, token)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileClient("http://127.0.0.1:46655", path); err == nil {
		t.Fatal("permissive token file was accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileClient("http://127.0.0.1:46655", link); err == nil {
		t.Fatal("symlink token file was accepted")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileClient("http://127.0.0.1:46655", fifo); err == nil {
		t.Fatal("FIFO token file was accepted")
	}
}

func TestFileClientRejectsUnsafeReplacementBeforeRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTestToken(t, path, strings.Repeat("a", 64))
	client, err := NewFileClient("http://127.0.0.1:46655", path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return testResponse(http.StatusOK, `[]`), nil
	})
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(path), "missing"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := client.List(context.Background()); err == nil {
		t.Fatal("request accepted a symlink token replacement")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("unsafe replacement sent %d requests", got)
	}
}

func TestFileClientDoesNotRetryUnauthorizedMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTestToken(t, path, strings.Repeat("a", 64))
	var calls atomic.Int32
	client, err := NewFileClient("http://127.0.0.1:46655", path)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return testResponse(http.StatusUnauthorized, `{"error":"unauthorized"}`), nil
	})
	_, err = client.Start(context.Background(), testID)
	if err == nil || !strings.Contains(err.Error(), "FUGATO_API_URL and FUGATO_API_TOKEN_FILE") {
		t.Fatalf("authorization diagnostic = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("mutation requests = %d, want 1", got)
	}
}
