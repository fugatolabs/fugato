package fugato

import (
	"context"
	"errors"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

// Create validates and registers an environment without starting its workloads.
// Existing registrations are returned unchanged, including running environments.
// File copying remains the separate, explicitly sourced PrepareFiles operation.
func (m *Manager) Create(ctx context.Context, path, expectedRuntimeID string) (Runtime, error) {
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
	if found {
		if r.ProjectDir != p.Dir || r.Repository != p.Repository {
			return Runtime{}, errors.New("runtime ID collision; refusing to reuse another project's resources")
		}
		return m.decorate(r), nil
	}
	p, releaseRegistration, err := m.acquireProjectRegistration(ctx, path, p)
	if err != nil {
		return Runtime{}, err
	}
	defer releaseRegistration()
	// Compose validation evaluates configuration only. It creates no containers,
	// networks, native children, or files in the worktree. Native configuration
	// has already been validated by ResolveProject.
	if p.ComposeFile != "" {
		if err := (ComposeBackend{}).Validate(ctx, p); err != nil {
			if ctx.Err() != nil {
				return Runtime{}, ctx.Err()
			}
			return Runtime{}, err
		}
	}
	r = Runtime{ID: p.ID, ProjectDir: p.Dir, Worktree: p.Worktree, Repository: p.Repository,
		ComposeProject: p.ComposeProject, Backend: "native", Desired: "stopped", Observed: "stopped"}
	if p.ComposeFile != "" {
		r.Backend = "environment"
	}
	if err := m.store.Save(ctx, &r); err != nil {
		return Runtime{}, err
	}
	return r, nil
}

func resolveExpectedProject(ctx context.Context, path, expectedRuntimeID string) (Project, error) {
	p, err := ResolveProject(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			return Project{}, ctx.Err()
		}
		return Project{}, err
	}
	if expectedRuntimeID != "" {
		if err := control.ValidateID(expectedRuntimeID); err != nil {
			return Project{}, err
		}
		if expectedRuntimeID != p.ID {
			return Project{}, errors.New("expected_runtime_id does not match resolved project")
		}
	}
	return p, nil
}
