package fugato

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDestroyHTTPAuthenticationAndExplicitDataPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, body, token string
		status            int
	}{
		{"unauthenticated", `{"volumes":true}`, "", http.StatusUnauthorized},
		{"unknown field", `{"volumes":true,"all":true}`, "secret", http.StatusBadRequest},
		{"wrong type", `{"volumes":"true"}`, "secret", http.StatusBadRequest},
		{"destroy", `{"volumes":false}`, "secret", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			state := t.TempDir()
			store, err := OpenStore(ctx, state)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			manager := NewManager(store, NewEnvironmentBackend(state), 46656)
			runtime, err := manager.Up(ctx, testProject(t))
			if err != nil {
				t.Fatal(err)
			}
			handler := apiHandler(manager, "secret", state, "127.0.0.1:46655")
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:46655/v1/runtimes/"+runtime.ID+"/destroy", strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer "+tc.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
			_, found, err := store.Get(ctx, runtime.ID)
			if err != nil || found == (tc.status == http.StatusOK) {
				t.Fatalf("registry found %v: %v", found, err)
			}
		})
	}
}
