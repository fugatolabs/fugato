package fugato

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

type Manager struct {
	admission    *admission
	reports      *reportCache
	store        *Store
	backend      Backend
	proxyPort    int
	locksMu      sync.Mutex
	locks        map[string]*runtimeGate
	registration chan struct{}
}

func NewManager(store *Store, backend Backend, proxyPort int) *Manager {
	return &Manager{admission: newAdmission(), reports: newReportCache(), store: store, backend: backend, proxyPort: proxyPort, locks: make(map[string]*runtimeGate)}
}

// runtimeGate serializes work for one runtime. refs includes the holder and
// waiters, so the map entry is reclaimed only when nobody can still acquire it.
type runtimeGate struct {
	token chan struct{}
	refs  int
}

func (m *Manager) lock(ctx context.Context, id string) (func(), error) {
	if err := control.ValidateID(id); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.locksMu.Lock()
	gate := m.locks[id]
	if gate == nil {
		gate = &runtimeGate{token: make(chan struct{}, 1)}
		m.locks[id] = gate
	}
	gate.refs++
	m.locksMu.Unlock()
	releaseRef := func() {
		m.locksMu.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(m.locks, id)
		}
		m.locksMu.Unlock()
	}
	select {
	case gate.token <- struct{}{}:
		unlock := func() { <-gate.token; releaseRef() }
		// A cancelled waiter must not start work if the gate became available
		// at the same time its context was cancelled.
		if err := ctx.Err(); err != nil {
			unlock()
			return nil, err
		}
		return unlock, nil
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
}

// Backend cancellation may leave resources partially changed. Preserve that
// failure for reconciliation, but never detach cleanup without a deadline.
func (m *Manager) recordFailure(ctx context.Context, r *Runtime) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	return m.store.Save(cleanupCtx, r)
}

func (m *Manager) decorate(r Runtime) Runtime {
	if backend, ok := m.backend.(interface{ SnapshotReady(string) bool }); ok && r.Observed == "running" && !backend.SnapshotReady(r.ID) {
		r.Observed = "unknown"
		r.Endpoints = nil
		r.PreviewURL = ""
		return r
	}
	if backend, ok := m.backend.(interface{ Snapshot(string) []PortBinding }); ok {
		if r.Observed == "running" {
			r.Endpoints = m.previewBindings(r.ID, backend.Snapshot(r.ID))
			var previews []string
			for _, ep := range r.Endpoints {
				if ep.Browser && ep.URL != "" {
					previews = append(previews, ep.URL)
				}
			}
			if len(previews) == 1 {
				r.PreviewURL = previews[0]
			}
		}
		return r
	}

	return r
}

func (m *Manager) Up(ctx context.Context, path string) (Runtime, error) {
	return m.UpExpected(ctx, path, "")
}

func (m *Manager) UpExpected(ctx context.Context, path, expectedRuntimeID string) (Runtime, error) {
	release, err := m.admission.lifecycle.acquire(ctx)
	if err != nil {
		return Runtime{}, err
	}
	defer release()
	p, err := resolveExpectedProject(ctx, path, expectedRuntimeID)
	if err != nil {
		return Runtime{}, err
	}
	unlock, err := m.lock(ctx, p.ID)
	if err != nil {
		return Runtime{}, err
	}
	defer unlock()
	r, found, err := m.store.Get(ctx, p.ID)
	if err != nil {
		return Runtime{}, err
	}
	if found && (r.ProjectDir != p.Dir || r.Repository != p.Repository) {
		return Runtime{}, errors.New("runtime ID collision; refusing to reuse another project's resources")
	}
	registrationDone := func() {}
	if !found {
		p, registrationDone, err = m.acquireProjectRegistration(ctx, path, p)
		if err != nil {
			return Runtime{}, err
		}
		defer registrationDone()
	}
	if found && r.Desired == "running" && r.Observed == "running" {
		running, inspectErr := m.backend.Inspect(ctx, p)
		if inspectErr == nil && running {
			return m.decorate(r), nil
		}
	}
	if !found {
		r = Runtime{ID: p.ID, ProjectDir: p.Dir, Worktree: p.Worktree, Repository: p.Repository,
			ComposeProject: p.ComposeProject, Backend: "compose", Desired: "running", Observed: "starting"}
	} else {
		r.Desired, r.Observed = "running", "starting"
	}
	// Keep evidence that this identity has owned Compose resources even when a
	// later configuration is native-only; retained volumes still need cleanup.
	if !found || r.Backend == "native" {
		r.Backend = "native"
	}
	if p.ComposeFile != "" {
		r.Backend = "environment"
	}
	if err := m.store.Save(ctx, &r); err != nil {
		return Runtime{}, err
	}
	registrationDone()
	err = m.backend.Start(ctx, p)
	if err != nil {
		r.Observed, r.LastError = "error", err.Error()
		if saveErr := m.recordFailure(ctx, &r); saveErr != nil {
			return Runtime{}, errors.Join(err, fmt.Errorf("recording start failure: %w", saveErr))
		}
		return m.decorate(r), err
	}
	r.Observed, r.LastError = "running", ""
	if err := m.store.Save(ctx, &r); err != nil {
		return Runtime{}, err
	}
	return m.decorate(r), nil
}

func (m *Manager) Down(ctx context.Context, id string) (Runtime, error) {
	release, err := m.admission.lifecycle.acquire(ctx)
	if err != nil {
		return Runtime{}, err
	}
	defer release()
	unlock, err := m.lock(ctx, id)
	if err != nil {
		return Runtime{}, err
	}
	defer unlock()
	r, found, err := m.store.Get(ctx, id)
	if err != nil {
		return Runtime{}, err
	}
	if !found {
		return Runtime{}, fmt.Errorf("unknown runtime %q", id)
	}
	if r.Observed == "stopped" && r.Desired == "stopped" {
		return m.decorate(r), nil
	}
	var p Project
	if backend, ok := m.backend.(*EnvironmentBackend); ok {
		if owned, found := backend.ownedProject(id); found {
			p = owned
			err = validateOwnedProjectIdentity(ctx, p)
		} else {
			p, err = ResolveProject(ctx, r.ProjectDir)
		}
	} else {
		p, err = ResolveProject(ctx, r.ProjectDir)
	}
	if ctx.Err() != nil {
		return Runtime{}, ctx.Err()
	}
	if err != nil || p.ID != id {
		return Runtime{}, errors.New("worktree identity changed; refusing to stop resources")
	}
	r.Desired, r.Observed = "stopped", "stopping"
	if err := m.store.Save(ctx, &r); err != nil {
		return Runtime{}, err
	}
	if err := m.backend.Stop(ctx, p); err != nil {
		r.Observed, r.LastError = "error", err.Error()
		if saveErr := m.recordFailure(ctx, &r); saveErr != nil {
			return Runtime{}, errors.Join(err, fmt.Errorf("recording stop failure: %w", saveErr))
		}
		return m.decorate(r), err
	}
	r.Observed, r.LastError = "stopped", ""
	if err := m.store.Save(ctx, &r); err != nil {
		return Runtime{}, err
	}
	return m.decorate(r), nil
}

func (m *Manager) Get(ctx context.Context, id string) (Runtime, bool, error) {
	if err := control.ValidateID(id); err != nil {
		return Runtime{}, false, err
	}
	r, found, err := m.store.Get(ctx, id)
	return m.decorate(r), found, err
}

func (m *Manager) List(ctx context.Context) ([]Runtime, error) {
	items, err := m.store.List(ctx)
	for i := range items {
		items[i] = m.decorate(items[i])
	}
	return items, err
}

func (m *Manager) Logs(ctx context.Context, id string, follow bool, output io.Writer) error {
	return m.logs(ctx, id, "", "", follow, output)
}

func (m *Manager) LogsService(ctx context.Context, id, service, kind string, output io.Writer) error {
	if !serviceName.MatchString(service) {
		return errors.New("invalid log service name")
	}
	if kind != "" && kind != "native" && kind != "compose" {
		return errors.New("service_kind must be native or compose")
	}
	return m.logs(ctx, id, service, kind, false, output)
}

func (m *Manager) logs(ctx context.Context, id, service, kind string, follow bool, output io.Writer) error {
	release, err := m.admission.logs.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := control.ValidateID(id); err != nil {
		return err
	}
	r, found, err := m.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("unknown runtime %q", id)
	}
	p, err := ResolveProject(ctx, r.ProjectDir)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err != nil || p.ID != id {
		return errors.New("worktree identity changed; refusing to read resources")
	}
	if service != "" {
		backend, ok := m.backend.(interface {
			LogsService(context.Context, Project, string, string, io.Writer) error
		})
		if !ok {
			return errors.New("runtime backend does not support service logs")
		}
		return backend.LogsService(ctx, p, service, kind, output)
	}
	return m.backend.Logs(ctx, p, follow, output)
}

func (m *Manager) Ports(ctx context.Context, id string) ([]PortBinding, error) {
	release, err := m.admission.lifecycle.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	unlock, err := m.lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	r, found, err := m.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("unknown runtime %q", id)
	}
	if r.Observed != "running" {
		return nil, errors.New("runtime is not running")
	}
	p, err := ResolveProject(ctx, r.ProjectDir)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil || p.ID != id {
		return nil, errors.New("worktree identity changed; refusing to inspect ports")
	}
	backend, ok := m.backend.(interface {
		Ports(context.Context, Project) ([]PortBinding, error)
	})
	if !ok {
		return nil, errors.New("runtime backend does not report host ports")
	}
	bindings, err := backend.Ports(ctx, p)
	return m.previewBindings(id, bindings), err
}

func (m *Manager) Reconcile(ctx context.Context) error {
	release, err := m.admission.lifecycle.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	items, err := m.store.List(ctx)
	if err != nil {
		return err
	}
	for _, r := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		unlock, err := m.lock(ctx, r.ID)
		if err != nil {
			return err
		}
		// List supplies candidates only. Lifecycle requests may have changed
		// desired state while we waited, so reload under the same runtime gate.
		r, found, err := m.store.Get(ctx, r.ID)
		if err != nil || !found {
			unlock()
			if err != nil {
				return err
			}
			continue
		}
		p, resolveErr := ResolveProject(ctx, r.ProjectDir)
		if err := ctx.Err(); err != nil {
			unlock()
			return err
		}
		if resolveErr != nil || p.ID != r.ID {
			r.Observed, r.LastError = "error", "worktree identity unavailable or changed"
			saveErr := m.store.Save(ctx, &r)
			unlock()
			if saveErr != nil {
				return saveErr
			}
			continue
		}
		running, inspectErr := m.backend.Inspect(ctx, p)
		if err := ctx.Err(); err != nil {
			unlock()
			return err
		}
		if inspectErr != nil {
			r.Observed, r.LastError = "error", inspectErr.Error()
		} else if running && r.Desired == "stopped" {
			if stopErr := m.backend.Stop(ctx, p); stopErr != nil {
				r.Observed, r.LastError = "error", stopErr.Error()
			} else {
				r.Observed, r.LastError = "stopped", ""
			}
		} else if running {
			r.Observed, r.LastError = "running", ""
		} else {
			r.Observed = "stopped"
			if r.Desired == "running" {
				r.LastError = "Environment services are not running; run fugato up to restart"
			} else {
				r.LastError = ""
			}
		}
		saveErr := m.store.Save(ctx, &r)
		unlock()
		if saveErr != nil {
			return saveErr
		}
	}
	return nil
}

func (m *Manager) previewBindings(id string, bindings []PortBinding) []PortBinding {
	for i := range bindings {
		binding := &bindings[i]
		if binding.Browser && binding.Protocol == "http" && binding.Name != "" {
			binding.URL = fmt.Sprintf("http://%s.%s.localhost:%d/", binding.Name, id, m.proxyPort)
		}
	}
	return bindings
}
