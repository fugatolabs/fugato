package fugato

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInspectComposeServices(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		wrongOwner, badPort, cancelled bool
	}{
		{name: "published and internal ports"},
		{name: "foreign container refused", wrongOwner: true},
		{name: "invalid port refused", badPort: true},
		{name: "caller cancellation", cancelled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Project{ID: "aaaaaaaaaaaaaaaa", Worktree: t.TempDir(), ComposeProject: "fugato-aaaaaaaaaaaaaaaa"}
			p.Config.Endpoints = map[string]EndpointConfig{"site": {Service: "web", Port: 3000, Protocol: "http", Browser: true}}
			id := strings.Repeat("a", 64)
			owner := p.ComposeProject
			if tc.wrongOwner {
				owner = "another-project"
			}
			portKey := "5432/tcp"
			if tc.badPort {
				portKey = "65536/tcp"
			}
			container := map[string]any{
				"Id":              id,
				"Config":          map[string]any{"Labels": map[string]string{"com.docker.compose.project": owner, "com.docker.compose.service": "web", "com.docker.compose.project.working_dir": p.Worktree}, "ExposedPorts": map[string]any{"3000/tcp": nil, portKey: nil}},
				"State":           map[string]any{"Status": "running", "Health": map[string]string{"Status": "healthy"}},
				"NetworkSettings": map[string]any{"Ports": map[string]any{"3000/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "32000"}}, portKey: nil}},
			}
			output, err := json.Marshal([]any{container})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			calls := 0
			services, err := inspectComposeServices(ctx, p, func(ctx context.Context, args ...string) ([]byte, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 10*time.Second {
					t.Fatal("missing inventory deadline")
				}
				calls++
				if calls == 1 {
					if strings.Join(args, " ") != "container ls --all --no-trunc --filter label=com.docker.compose.project="+p.ComposeProject+" --format {{.ID}}" {
						t.Fatalf("unexpected Docker command: %v", args)
					}
					return []byte(id), nil
				}
				if strings.Join(args, " ") != "container inspect "+id {
					t.Fatalf("unexpected Docker command: %v", args)
				}
				return output, nil
			})
			if tc.wrongOwner || tc.badPort || tc.cancelled {
				if err == nil {
					t.Fatal("unsafe/incomplete inventory accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || len(services) != 1 || services[0].Name != "web" || services[0].Health != "healthy" || len(services[0].Ports) != 2 {
				t.Fatalf("inventory: %+v", services)
			}
			published, internal := services[0].Ports[0], services[0].Ports[1]
			if published.Name != "site" || !published.Browser || published.HostPort != 32000 || published.URL != "http://127.0.0.1:32000/" {
				t.Fatalf("published: %+v", published)
			}
			if internal.ContainerPort != 5432 || internal.HostPort != 0 || internal.URL != "" {
				t.Fatalf("internal port published: %+v", internal)
			}
		})
	}
}

func TestNativeServiceInventoryDoesNotAdoptExitedPorts(t *testing.T) {
	b := NewEnvironmentBackend(t.TempDir())
	live, exited := make(chan struct{}), make(chan struct{})
	close(exited)
	b.environments["aaaaaaaaaaaaaaaa"] = &nativeEnvironment{processes: map[string]*nativeProcess{"live": {done: live}, "exited": {done: exited}}, bindings: []PortBinding{{Command: "live", HostPort: 3001}, {Command: "exited", HostPort: 3002}}}
	services, err := b.Services(t.Context(), Project{ID: "aaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 2 || services[0].Name != "exited" || services[0].State != "exited" || len(services[0].Ports) != 0 || services[1].State != "running" || len(services[1].Ports) != 1 {
		t.Fatalf("services: %+v", services)
	}
}

type serviceTestBackend struct {
	fakeBackend
	calls int
}

func (b *serviceTestBackend) Services(ctx context.Context, p Project) ([]RuntimeService, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.calls++
	return []RuntimeService{{Name: "db", Kind: "compose", State: "running", Ports: []PortBinding{{Service: "db", ContainerPort: 5432, Protocol: "tcp"}}}}, nil
}

func TestServicesHTTPIsAuthenticatedReadOnlyAndRuntimeScoped(t *testing.T) {
	store, err := OpenStore(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b := &serviceTestBackend{}
	manager := NewManager(store, b, 46656)
	runtime, err := manager.Up(t.Context(), testProject(t))
	if err != nil {
		t.Fatal(err)
	}
	handler := apiHandler(manager, "secret", t.TempDir(), "127.0.0.1:46655")
	for _, tc := range []struct {
		name, token, id string
		status          int
	}{
		{"authentication required", "", runtime.ID, http.StatusUnauthorized},
		{"invalid identity", "secret", "invalid", http.StatusBadRequest},
		{"unknown identity", "secret", "bbbbbbbbbbbbbbbb", http.StatusBadRequest},
		{"inventory", "secret", runtime.ID, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:46655/v1/runtimes/"+tc.id+"/services", nil)
			request.Header.Set("Authorization", "Bearer "+tc.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			if tc.status == http.StatusOK && !strings.Contains(response.Body.String(), `"container_port":5432`) {
				t.Fatalf("missing internal port: %s", response.Body.String())
			}
		})
	}
	if b.starts != 1 || b.calls != 1 {
		t.Fatalf("inspection changed workload state: starts=%d calls=%d", b.starts, b.calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := manager.Services(ctx, runtime.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled inspection: %v", err)
	}
	if _, err := manager.Down(t.Context(), runtime.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Services(t.Context(), runtime.ID); err == nil || b.calls != 1 {
		t.Fatal("stopped service inventory must not inspect stale ports")
	}
}
