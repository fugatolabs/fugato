package fugato

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
	"github.com/fugatolabs/fugato/packages/fugato/internal/verify"
)

type Settings struct {
	StateDir  string
	APIAddr   string
	ProxyAddr string
	ProxyPort int
}

func DefaultSettings() (Settings, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return Settings{}, err
	}
	stateDir := os.Getenv("FUGATO_STATE_DIR")
	if stateDir == "" {
		stateDir = filepath.Join(base, "fugato")
	}
	apiPort, proxyPort := 46655, 46656
	if value := os.Getenv("FUGATO_API_PORT"); value != "" {
		apiPort, err = strconv.Atoi(value)
		if err != nil || apiPort < 1 || apiPort > 65535 {
			return Settings{}, errors.New("FUGATO_API_PORT must be a valid TCP port")
		}
	}
	if value := os.Getenv("FUGATO_PROXY_PORT"); value != "" {
		proxyPort, err = strconv.Atoi(value)
		if err != nil || proxyPort < 1 || proxyPort > 65535 {
			return Settings{}, errors.New("FUGATO_PROXY_PORT must be a valid TCP port")
		}
	}
	if apiPort == proxyPort {
		return Settings{}, errors.New("API and preview ports must differ")
	}
	return Settings{StateDir: stateDir, APIAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(apiPort)),
		ProxyAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort)), ProxyPort: proxyPort}, nil
}

func RunDaemon(ctx context.Context, settings Settings) error {
	if err := os.MkdirAll(settings.StateDir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(settings.StateDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("state directory must be a real directory")
	}
	if err := os.Chmod(settings.StateDir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(settings.StateDir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	ownedResources := true
	defer func() {
		if ownedResources {
			_ = lock.Close()
		}
	}()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another Fugato daemon is already running")
	}
	defer func() {
		if ownedResources {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		}
	}()
	token, err := loadOrCreateToken(settings.StateDir)
	if err != nil {
		return err
	}
	store, err := OpenStore(ctx, settings.StateDir)
	if err != nil {
		return err
	}
	defer func() {
		if ownedResources {
			_ = store.Close()
		}
	}()
	backend := NewEnvironmentBackend(settings.StateDir)
	manager := NewManager(store, backend, settings.ProxyPort)
	apiListener, err := net.Listen("tcp", settings.APIAddr)
	if err != nil {
		return fmt.Errorf("listen for local API: %w", err)
	}
	defer apiListener.Close()
	proxyListener, err := net.Listen("tcp", settings.ProxyAddr)
	if err != nil {
		return fmt.Errorf("listen for previews: %w", err)
	}
	defer proxyListener.Close()
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	apiRequests, proxyRequests := &requestSet{}, &requestSet{}
	apiConnections, proxyConnections := &connectionSet{}, &connectionSet{}
	transport := previewTransport()
	defer transport.CloseIdleConnections()
	baseContext := func(net.Listener) context.Context { return runCtx }
	api := &http.Server{Handler: apiRequests.wrap(apiHandler(manager, token, settings.StateDir, settings.APIAddr)), BaseContext: baseContext, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: requestBodyTimeout, IdleTimeout: time.Minute, MaxHeaderBytes: 1 << 20}
	proxy := &http.Server{Handler: proxyRequests.wrap(proxyHandlerWithTransport(manager, settings.ProxyPort, transport)), BaseContext: baseContext, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: requestBodyTimeout, IdleTimeout: time.Minute, MaxHeaderBytes: 1 << 20}
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- api.Serve(ownedListener{apiListener, apiConnections}) }()
	go func() { errorsCh <- proxy.Serve(ownedListener{proxyListener, proxyConnections}) }()
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		if err := manager.Reconcile(runCtx); err != nil && runCtx.Err() == nil {
			slog.Error("initial reconciliation failed", "error", err)
		}
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := manager.Reconcile(runCtx); err != nil && runCtx.Err() == nil {
					slog.Error("reconciliation failed", "error", err)
				}
			}
		}
	}()
	slog.Info("Fugato daemon ready", "api", settings.APIAddr, "preview", settings.ProxyAddr)
	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-errorsCh:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	apiRequests.stop()
	proxyRequests.stop()
	cancelRun()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		_ = api.Close()
	}
	if err := proxy.Shutdown(shutdownCtx); err != nil {
		_ = proxy.Close()
	}
	apiConnections.closeAll()
	proxyConnections.closeAll()
	// From here one cleanup owner holds the store and instance lock until every
	// worker has exited, even if a broken backend misses the shutdown deadline.
	ownedResources = false
	done := make(chan struct{})
	go func() {
		<-reconcileDone
		apiRequests.wg.Wait()
		proxyRequests.wg.Wait()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := backend.Close(cleanupCtx); err != nil {
			slog.Error("stop native processes", "error", err)
			// Keep the registry/instance cleanup owner until every child has
			// exited, even if the caller's shutdown budget has elapsed.
			_ = backend.Close(context.Background())
		}
		close(done)
	}()
	err = finishShutdown(shutdownCtx, done, func() {
		_ = store.Close()
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	})
	return errors.Join(serveErr, err)

}

func loadOrCreateToken(stateDir string) (string, error) {
	path := filepath.Join(stateDir, "token")
	if data, err := os.ReadFile(path); err == nil {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return "", errors.New("token file must be a user-only regular file")
		}
		token := strings.TrimSpace(string(data))
		decoded, err := hex.DecodeString(token)
		if err != nil || len(decoded) != 32 {
			return "", errors.New("token file contains an invalid secret")
		}
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	token := hex.EncodeToString(secret)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(file, token+"\n"); err != nil {
		file.Close()
		return "", err
	}
	return token, file.Close()
}

func apiHandler(manager *Manager, token, stateDir, apiAddr string) http.Handler {
	mux := http.NewServeMux()
	var instanceNonce [16]byte
	instanceID := fmt.Sprintf("%d-%p", time.Now().UnixNano(), manager)
	if _, err := rand.Read(instanceNonce[:]); err == nil {
		instanceID = hex.EncodeToString(instanceNonce[:])
	}
	mux.Handle("/v1/agent/", control.Handler(ControlService{Manager: manager, Backend: manager.backend, PolicyForRuntime: func(id string) (control.CommandPolicy, error) { return control.LoadCommandPolicy(stateDir, id) }, Verifier: verify.Verifier{Runner: ComposeBrowserRunner{}, ArtifactsDir: filepath.Join(stateDir, "artifacts")}}, token))
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"api_version": control.APIVersion, "instance_id": instanceID,
			"features": map[string]bool{"repository_worktrees": true, "project_resolution": true, "latest_verification": true, "environments": true, "project_discovery": true, "named_commands": true, "prepare_worktree": true, "service_inventory": true, "create_environment": true, "destroy_environment": true, "worktree_lifecycle": true, "service_logs": true}})
	})
	mux.HandleFunc("POST /v1/repositories/worktrees", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		if err := readJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := manager.RepositoryWorktrees(r.Context(), body.Path)
		if err != nil {
			writeMetadataError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /v1/projects/resolve", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		if err := readJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := manager.ResolveProjectMetadata(r.Context(), body.Path)
		if err != nil {
			writeMetadataError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	registerEnvironmentRoutes(mux, manager)
	registerWorktreeRoutes(mux, manager)
	registerDestroyRoute(mux, manager)
	registerCreateRoute(mux, manager)
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/runtimes", func(w http.ResponseWriter, r *http.Request) {
		items, err := manager.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if items == nil {
			items = []Runtime{}
		}
		writeJSON(w, http.StatusOK, items)
	})
	mux.HandleFunc("POST /v1/runtimes", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path              string `json:"path"`
			ExpectedRuntimeID string `json:"expected_runtime_id,omitempty"`
		}
		if err := readJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := manager.UpExpected(r.Context(), body.Path, body.ExpectedRuntimeID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /v1/runtimes/{id}", func(w http.ResponseWriter, r *http.Request) {
		result, found, err := manager.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
		} else if !found {
			writeError(w, http.StatusNotFound, errors.New("unknown runtime"))
		} else {
			writeJSON(w, http.StatusOK, result)
		}
	})
	mux.HandleFunc("GET /v1/runtimes/{id}/ports", func(w http.ResponseWriter, r *http.Request) {
		bindings, err := manager.Ports(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, bindings)
	})
	mux.HandleFunc("GET /v1/runtimes/{id}/services", func(w http.ResponseWriter, r *http.Request) {
		services, err := manager.Services(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, services)
	})
	mux.HandleFunc("POST /v1/runtimes/{id}/down", func(w http.ResponseWriter, r *http.Request) {
		result, err := manager.Down(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /v1/runtimes/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, found, err := manager.Get(r.Context(), id); err != nil || !found {
			writeError(w, http.StatusNotFound, errors.New("unknown runtime"))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Trailer", LogStatusTrailer)
		streamCtx, cancelStream := context.WithCancel(r.Context())
		defer cancelStream()
		stream := &flushWriter{ResponseWriter: w, cancel: cancelStream}
		err := manager.Logs(streamCtx, id, r.URL.Query().Get("follow") == "1", stream)
		if err != nil {
			if !stream.started() {
				w.Header().Del("Trailer")
				writeError(w, http.StatusBadGateway, err)
				return
			}
			w.Header().Set(LogStatusTrailer, "error")
			if r.Context().Err() == nil {
				slog.Error("log stream failed", "runtime", id, "error", err)
			}
			return
		}
		w.Header().Set(LogStatusTrailer, "ok")
		if !stream.started() {
			w.WriteHeader(http.StatusOK)
		}

	})
	return boundedControl(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != apiAddr || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			writeError(w, http.StatusUnauthorized, errors.New("unauthorized"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	}))
}

func writeMetadataError(w http.ResponseWriter, err error) {
	if errors.Is(err, control.ErrBusy) {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeError(w, http.StatusBadRequest, err)
}

func proxyHandler(manager *Manager, port int) http.Handler {
	return proxyHandlerWithTransport(manager, port, previewTransport())
}

func proxyHandlerWithTransport(manager *Manager, port int, transport *http.Transport) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if endpointName, id, ok := parseNamedPreviewHost(r.Host, port); ok {
			runtime, found, err := manager.Get(r.Context(), id)
			if err != nil || !found || runtime.Observed != "running" {
				http.NotFound(w, r)
				return
			}
			var binding *PortBinding
			for i := range runtime.Endpoints {
				ep := &runtime.Endpoints[i]
				if ep.Name == endpointName && ep.Browser && ep.Protocol == "http" {
					binding = ep
					break
				}
			}
			if binding == nil {
				http.NotFound(w, r)
				return
			}
			upstream := &url.URL{Scheme: "http", Host: net.JoinHostPort(binding.HostIP, strconv.Itoa(binding.HostPort))}
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			proxy.Transport = transport
			proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
				http.Error(w, "preview upstream unavailable", http.StatusBadGateway)
			}
			proxy.ServeHTTP(&deadlineWriter{w, streamWriteTimeout}, r)
			return
		}
		http.NotFound(w, r)
	})
}

func readJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	if errors.Is(err, control.ErrBusy) {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// flushWriter delays success until output or an explicit subprocess-start signal.
// exec copies stdout in another goroutine, so the start callback and writes share
// one lock; all calls finish before Backend.Logs returns.
type flushWriter struct {
	http.ResponseWriter
	mu        sync.Mutex
	committed bool
	cancel    context.CancelFunc
}

func (w *flushWriter) started() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.committed }
func (w *flushWriter) Started() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.committed = true
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *flushWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.committed = true
	n, err := w.ResponseWriter.Write(data)
	if err == nil {
		err = http.NewResponseController(w.ResponseWriter).Flush()
	}
	if err != nil && w.cancel != nil {
		w.cancel()
	}
	return n, err
}

func parseNamedPreviewHost(value string, port int) (string, string, bool) {
	host, number, err := net.SplitHostPort(value)
	if err != nil || number != strconv.Itoa(port) || !strings.HasSuffix(host, ".localhost") {
		return "", "", false
	}
	stem := strings.TrimSuffix(host, ".localhost")
	split := strings.LastIndexByte(stem, '.')
	if split < 1 {
		return "", "", false
	}
	name, id := stem[:split], stem[split+1:]
	return name, id, serviceName.MatchString(name) && control.ValidateID(id) == nil
}
