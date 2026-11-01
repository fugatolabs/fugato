package fugato

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
	"github.com/fugatolabs/fugato/packages/fugato/internal/verify"
)

type destroyVerificationBackend struct {
	fakeBackend
	destroyed chan struct{}
}

func (b *destroyVerificationBackend) Destroy(context.Context, Runtime, bool) error {
	close(b.destroyed)
	return nil
}

type blockedCleanupVerification struct {
	entered chan struct{}
	release chan struct{}
}

func (v blockedCleanupVerification) wait(ctx context.Context) error {
	close(v.entered)
	select {
	case <-v.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (v blockedCleanupVerification) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := v.wait(request.Context()); err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
}
func (v blockedCleanupVerification) Run(ctx context.Context, _ verify.Target, _ string) (verify.BrowserEvidence, error) {
	err := v.wait(ctx)
	return verify.BrowserEvidence{Status: 200}, err
}

func TestDestroyWaitsForVerificationBeforeRemovingResourcesAndReports(t *testing.T) {
	for _, kind := range []string{"http", "browser"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			dir := testProject(t)
			config := "version: 1\ncompose: compose.yaml\nstart: {services: [web]}\nbrowser: {service: browser}\nendpoints:\n  site: {service: web, port: 3000, protocol: http, browser: true}\n"
			if err := os.WriteFile(filepath.Join(dir, ".fugato.yaml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web: {image: test}\n  browser: {image: test}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// Startup planning reads Compose configuration even with the fake
			// runtime backend. Keep this ordering test independent of Docker.
			wrapperDir := t.TempDir()
			script := "#!/bin/sh\nfor arg in \"$@\"; do\n  if [ \"$arg\" = config ]; then\n    printf '%s\\n' '{\"services\":{\"web\":{},\"browser\":{}}}'\n    exit 0\n  fi\ndone\nexit 97\n"
			if err := os.WriteFile(filepath.Join(wrapperDir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			store, err := OpenStore(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			backend := &destroyVerificationBackend{destroyed: make(chan struct{})}
			manager := NewManager(store, backend, 46656)
			r, err := manager.Up(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			blocker := blockedCleanupVerification{entered: make(chan struct{}), release: make(chan struct{})}
			service := ControlService{Manager: manager, Backend: backend, Verifier: verify.Verifier{Runner: blocker, HTTPClient: &http.Client{Transport: blocker}}}
			verifyDone := make(chan error, 1)
			destroyDone := make(chan error, 1)
			var workers sync.WaitGroup
			defer func() { cancel(); workers.Wait() }()
			workers.Go(func() {
				var err error
				if kind == "http" {
					_, err = service.VerifyHTTP(ctx, control.VerifyRequest{RuntimeID: r.ID})
				} else {
					_, err = service.VerifyBrowser(ctx, control.VerifyRequest{RuntimeID: r.ID})
				}
				verifyDone <- err
			})
			select {
			case <-blocker.entered:
			case err := <-verifyDone:
				t.Fatalf("verification ended before blocking: %v", err)
			case <-ctx.Done():
				t.Fatal("verification did not enter")
			}
			workers.Go(func() { _, err := manager.Destroy(ctx, r.ID, true); destroyDone <- err })
			// Observe the destroy waiter in the gate, rather than relying on a delay.
			for {
				manager.locksMu.Lock()
				gate := manager.locks[r.ID]
				waiting := gate != nil && gate.refs == 2
				manager.locksMu.Unlock()
				if waiting {
					break
				}
				select {
				case <-backend.destroyed:
					t.Fatal("destroy raced in-flight verification")
				case <-ctx.Done():
					t.Fatal("destroy never waited on verification")
				default:
					runtime.Gosched()
				}
			}
			select {
			case <-backend.destroyed:
				t.Fatal("resources destroyed during verification")
			default:
			}
			close(blocker.release)
			if err := <-verifyDone; err != nil {
				t.Fatal(err)
			}
			if err := <-destroyDone; err != nil {
				t.Fatal(err)
			}
			latest := manager.reports.latest(r.ID)
			if latest.HTTP != nil || latest.Browser != nil {
				t.Fatal("completed verification resurrected destroyed runtime report")
			}
			if _, found, err := store.Get(ctx, r.ID); err != nil || found {
				t.Fatalf("destroyed runtime remained: %v %v", found, err)
			}
		})
	}
}
