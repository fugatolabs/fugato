package fugato

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestInfoAdvertisesEditorLifecycleCapabilities(t *testing.T) {
	stateDir := t.TempDir()
	store, err := OpenStore(t.Context(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := apiHandler(NewManager(store, &fakeBackend{}, 46656), "test-token", stateDir, "127.0.0.1:46655")
	response := extensionRequest(t, handler, http.MethodGet, "/v1/info", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("info status: %d %s", response.Code, response.Body.String())
	}
	var info struct {
		APIVersion string          `json:"api_version"`
		InstanceID string          `json:"instance_id"`
		Features   map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.APIVersion != "v1" || info.InstanceID == "" {
		t.Fatalf("invalid API identity: %+v", info)
	}
	for _, feature := range []string{"create_environment", "destroy_environment", "worktree_lifecycle", "service_inventory", "service_logs"} {
		if !info.Features[feature] {
			t.Errorf("missing editor lifecycle capability %q", feature)
		}
	}
}

func TestLocalAPIAuthorizationAndHost(t *testing.T) {
	stateDir := t.TempDir()
	store, err := OpenStore(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const addr = "127.0.0.1:46655"
	handler := apiHandler(NewManager(store, &fakeBackend{}, 46656), "test-token", stateDir, addr)
	for _, tt := range []struct {
		name, host, token, origin string
		want                      int
	}{
		{"authorized", addr, "Bearer test-token", "", http.StatusOK},
		{"missing token", addr, "", "", http.StatusUnauthorized},
		{"browser origin", addr, "Bearer test-token", "https://evil.example", http.StatusUnauthorized},
		{"wrong host", "attacker.example:46655", "Bearer test-token", "", http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://"+addr+"/v1/runtimes", nil)
			request.Host = tt.host
			request.Header.Set("Authorization", tt.token)
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.want {
				t.Fatalf("status %d, wanted %d", response.Code, tt.want)
			}
		})
	}
}

func TestTokenFileRequiresUserOnlyPermissions(t *testing.T) {
	stateDir := t.TempDir()
	token, err := loadOrCreateToken(stateDir)
	if err != nil || len(token) != 64 {
		t.Fatalf("create token: %q, %v", token, err)
	}
	path := filepath.Join(stateDir, "token")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateToken(stateDir); err == nil {
		t.Fatal("insecure token file was accepted")
	}
}

type logFailureBackend struct {
	fakeBackend
	output  string
	failure error
}

func (b *logFailureBackend) Logs(_ context.Context, _ Project, _ bool, w io.Writer) error {
	if b.output != "" {
		if _, err := io.WriteString(w, b.output); err != nil {
			return err
		}
	}
	return b.failure
}
func TestLogFailuresAreObservable(t *testing.T) {
	for _, tt := range []struct {
		name, output string
		failure      error
		status       int
		trailer      string
	}{
		{"startup failure", "", errors.New("backend unavailable"), http.StatusBadGateway, ""},
		{"stream failure", "one line\n", errors.New("backend disappeared"), http.StatusOK, "error"},
		{"empty success", "", nil, http.StatusOK, "ok"},
		{"stream success", "one line\n", nil, http.StatusOK, "ok"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, err := OpenStore(t.Context(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			manager := NewManager(store, &logFailureBackend{output: tt.output, failure: tt.failure}, 46656)
			runtime, err := manager.Up(t.Context(), testProject(t))
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:46655/v1/runtimes/"+runtime.ID+"/logs", nil)
			request.Header.Set("Authorization", "Bearer test-token")
			response := httptest.NewRecorder()
			apiHandler(manager, "test-token", t.TempDir(), "127.0.0.1:46655").ServeHTTP(response, request)
			result := response.Result()
			defer result.Body.Close()
			if result.StatusCode != tt.status {
				t.Fatalf("status %d want %d", result.StatusCode, tt.status)
			}
			if got := result.Trailer.Get(LogStatusTrailer); got != tt.trailer {
				t.Fatalf("trailer %q want %q", got, tt.trailer)
			}
		})
	}
}
