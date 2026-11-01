package fugato

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

// DestroyResult records the requested data policy, not a count of deleted resources.
type DestroyResult struct {
	ID      string `json:"id"`
	Volumes bool   `json:"volumes"`
}

type destroyBackend interface {
	Destroy(context.Context, Runtime, bool) error
}

// Destroy forgets a runtime only after its owned resources have been removed.
// Its saved identity permits cleanup after a worktree or its config disappears.
func (m *Manager) Destroy(ctx context.Context, id string, volumes bool) (DestroyResult, error) {
	release, err := m.admission.lifecycle.acquire(ctx)
	if err != nil {
		return DestroyResult{}, err
	}
	defer release()
	unlock, err := m.lock(ctx, id)
	if err != nil {
		return DestroyResult{}, err
	}
	defer unlock()
	r, found, err := m.store.Get(ctx, id)
	if err != nil {
		return DestroyResult{}, err
	}
	if !found {
		return DestroyResult{}, fmt.Errorf("unknown runtime %q", id)
	}
	if err := validateDestroyIdentity(r); err != nil {
		return DestroyResult{}, err
	}
	backend, ok := m.backend.(destroyBackend)
	if !ok {
		return DestroyResult{}, errors.New("backend does not support runtime destruction")
	}
	r.Desired, r.Observed, r.LastError = "stopped", "destroying", ""
	if err := m.store.Save(ctx, &r); err != nil {
		return DestroyResult{}, err
	}
	if err := backend.Destroy(ctx, r, volumes); err != nil {
		r.Observed, r.LastError = "error", err.Error()
		return DestroyResult{}, errors.Join(err, m.recordFailure(ctx, &r))
	}
	if _, err := m.store.db.ExecContext(ctx, "DELETE FROM runtimes WHERE id = ?", id); err != nil {
		r.Observed, r.LastError = "stopped", err.Error()
		return DestroyResult{}, errors.Join(err, m.recordFailure(ctx, &r))
	}
	m.reports.remove(id)
	return DestroyResult{ID: id, Volumes: volumes}, nil
}

func validateDestroyIdentity(r Runtime) error {
	if err := control.ValidateID(r.ID); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(r.Repository + "\x00" + r.ProjectDir))
	if hex.EncodeToString(sum[:8]) != r.ID || r.ComposeProject != "fugato-"+r.ID ||
		!filepath.IsAbs(r.ProjectDir) || !filepath.IsAbs(r.Repository) || !filepath.IsAbs(r.Worktree) ||
		!within(r.Worktree, r.ProjectDir) || filepath.Clean(r.Worktree) != r.Worktree ||
		filepath.Clean(r.ProjectDir) != r.ProjectDir || filepath.Clean(r.Repository) != r.Repository {
		return errors.New("saved runtime identity is inconsistent; refusing to destroy resources")
	}
	switch r.Backend {
	case "native", "compose", "environment":
		return nil
	default:
		return fmt.Errorf("unsupported runtime backend %q", r.Backend)
	}
}

func (b *EnvironmentBackend) Destroy(ctx context.Context, r Runtime, volumes bool) error {
	if err := validateDestroyIdentity(r); err != nil {
		return err
	}
	if env := b.environment(r.ID); env != nil {
		if env.project.Dir != r.ProjectDir || env.project.Repository != r.Repository {
			return errors.New("native owner identity differs from saved runtime")
		}
		if err := b.stopNative(ctx, env); err != nil {
			return err
		}
		b.mu.Lock()
		env.bindings = nil
		b.mu.Unlock()
	}
	if r.Backend != "native" {
		if err := destroyDockerResources(ctx, r, volumes, destroyDockerOutput); err != nil {
			return err
		}
	}
	if volumes {
		if err := removeRuntimeData(ctx, b.stateDir, r.ID); err != nil {
			return err
		}
	}
	b.mu.Lock()
	delete(b.environments, r.ID)
	b.mu.Unlock()
	return nil
}

func (ComposeBackend) Destroy(ctx context.Context, r Runtime, volumes bool) error {
	if err := validateDestroyIdentity(r); err != nil {
		return err
	}
	return destroyDockerResources(ctx, r, volumes, destroyDockerOutput)
}

type dockerCleanupResource struct {
	kind string
	id   string
}

type dockerCleanupCommand func(context.Context, ...string) ([]byte, error)

// Docker labels are the ownership boundary. Names merely cross-check Compose's
// isolation convention; they never select a resource by themselves.
func destroyDockerResources(ctx context.Context, r Runtime, volumes bool, run dockerCleanupCommand) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	kinds := []string{"container", "network"}
	if volumes {
		kinds = append(kinds, "volume")
	}
	var resources []dockerCleanupResource
	for _, kind := range kinds {
		args := []string{kind, "ls", "--filter", "label=com.docker.compose.project=" + r.ComposeProject, "--format", "{{.ID}}"}
		if kind == "container" {
			args = append(args, "--all", "--no-trunc")
		}
		if kind == "network" {
			args = append(args, "--no-trunc")
		}
		if kind == "volume" {
			args[len(args)-1] = "{{.Name}}"
		}
		output, err := run(ctx, args...)
		if err != nil {
			return err
		}
		ids := strings.Fields(string(output))
		if len(ids) > 1000 {
			return errors.New("too many runtime resources to destroy safely")
		}
		for _, id := range ids {
			if !safeDockerResourceID(id) {
				return errors.New("invalid Docker resource identity")
			}
			resource := dockerCleanupResource{kind: kind, id: id}
			if err := verifyCleanupResource(ctx, r, resource, run); err != nil {
				return err
			}
			resources = append(resources, resource)
		}
	}
	// Validate the complete selection before the first mutation. Check again just
	// before each deletion; Docker refuses removal of volumes/networks still in use.
	for _, resource := range resources {
		if err := verifyCleanupResource(ctx, r, resource, run); err != nil {
			return err
		}
		args := []string{resource.kind, "rm"}
		if resource.kind == "container" {
			args = append(args, "--force")
		}
		args = append(args, resource.id)
		if _, err := run(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func safeDockerResourceID(id string) bool {
	if id == "" || len(id) > 255 || id[0] == '-' {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

func verifyCleanupResource(ctx context.Context, r Runtime, resource dockerCleanupResource, run dockerCleanupCommand) error {
	output, err := run(ctx, resource.kind, "inspect", resource.id)
	if err != nil {
		return err
	}
	var inspected []struct {
		ID     string            `json:"Id"`
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(output, &inspected); err != nil {
		return fmt.Errorf("inspect cleanup ownership: %w", err)
	}
	if len(inspected) != 1 {
		return errors.New("ambiguous Docker resource identity")
	}
	item := inspected[0]
	labels := item.Labels
	identity := item.ID
	if resource.kind == "container" {
		labels = item.Config.Labels
	}
	if resource.kind == "volume" {
		identity = item.Name
	}
	if identity != resource.id || labels["com.docker.compose.project"] != r.ComposeProject {
		return fmt.Errorf("refusing to remove %s %s: ownership mismatch", resource.kind, resource.id)
	}
	if resource.kind == "container" {
		if !filepath.IsAbs(labels["com.docker.compose.project.working_dir"]) || !within(r.Worktree, labels["com.docker.compose.project.working_dir"]) || labels["com.docker.compose.service"] == "" {
			return fmt.Errorf("refusing to remove container %s: project directory or service ownership mismatch", resource.id)
		}
	} else {
		logicalName := labels["com.docker.compose."+resource.kind]
		if logicalName == "" || !strings.HasPrefix(item.Name, r.ComposeProject+"_") {
			return fmt.Errorf("refusing to remove %s %s: Compose resource ownership mismatch", resource.kind, resource.id)
		}
	}
	return nil
}

func destroyDockerOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = dockerEnvironment()
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() >= 1<<20 {
		return nil, errors.New("Docker cleanup output exceeded limit")
	}
	return stdout.Bytes(), nil
}

// Pin both directories and reject symlink replacements before deleting. Root's
// operations remain confined to the opened directory even if paths are renamed.
func removeRuntimeData(ctx context.Context, stateDir, id string) error {
	if err := control.ValidateID(id); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(stateDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("state directory must be a real directory")
	}
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("state directory changed during cleanup")
	}
	info, err = root.Lstat("data")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("native data root must be a real directory")
	}
	data, err := root.OpenRoot("data")
	if err != nil {
		return err
	}
	defer data.Close()
	opened, err = data.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("native data root changed during cleanup")
	}
	info, err = data.Lstat(id)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("runtime data must be a real directory")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return data.RemoveAll(id)
}
