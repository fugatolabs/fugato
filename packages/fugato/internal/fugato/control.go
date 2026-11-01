package fugato

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
	"github.com/fugatolabs/fugato/packages/fugato/internal/verify"
)

// CommandBackend is the backend extension point for scoped in-service exec.
// Implementations must use argv (no shell), enforce the supplied timeout and
// output cap, and run only in the selected runtime's named service.
type CommandBackend interface {
	RunCommand(context.Context, Project, control.CommandRequest) (control.CommandResult, error)
}

// ControlService adapts the environment manager to the versioned daemon API.
// A backend may add CommandBackend and verify.BrowserRunner independently.
type ControlService struct {
	Manager          *Manager
	Backend          Backend
	Policy           control.CommandPolicy
	PolicyForRuntime func(string) (control.CommandPolicy, error)
	Verifier         verify.Verifier
}

// Prepare uses metadata capacity without requiring a registered runtime.
func (s ControlService) Prepare(ctx context.Context, input control.PrepareRequest) (control.PrepareReport, error) {
	release, err := s.Manager.admission.metadata.acquire(ctx)
	if err != nil {
		return control.PrepareReport{}, err
	}
	defer release()
	if err := control.ValidatePrepare(input); err != nil {
		return control.PrepareReport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return PrepareFiles(ctx, input.Path, input.From, input.DryRun)
}

func (s ControlService) get(ctx context.Context, id string) (Runtime, control.Runtime, error) {
	release, err := s.Manager.admission.metadata.acquire(ctx)
	if err != nil {
		return Runtime{}, control.Runtime{}, err
	}
	defer release()
	if err := control.ValidateID(id); err != nil {
		return Runtime{}, control.Runtime{}, err
	}
	r, found, err := s.Manager.Get(ctx, id)
	if err != nil {
		return Runtime{}, control.Runtime{}, err
	}
	if !found {
		return Runtime{}, control.Runtime{}, fmt.Errorf("unknown runtime %q", id)
	}
	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	commit, err := gitOutput(gitCtx, r.Worktree, "rev-parse", "HEAD")
	item := control.Runtime{ID: r.ID, Worktree: r.Worktree, Repository: r.Repository, ProjectDir: r.ProjectDir, Commit: commit, Backend: r.Backend, Desired: r.Desired, Observed: r.Observed, PreviewURL: r.PreviewURL, Health: health(r), LastError: r.LastError}
	if err != nil {
		if ctx.Err() != nil {
			return Runtime{}, control.Runtime{}, ctx.Err()
		}
		item.MetadataError = "worktree commit unavailable: " + err.Error()
	}
	return r, item, nil
}

func health(r Runtime) string {
	if r.Observed == "running" && len(r.Endpoints) > 0 {
		return "unchecked"
	}
	return "unavailable"
}

func (s ControlService) List(ctx context.Context) ([]control.Runtime, error) {
	items, err := s.Manager.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]control.Runtime, 0, len(items))
	for _, r := range items {
		_, item, err := s.get(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}
func (s ControlService) Get(ctx context.Context, id string) (control.Runtime, error) {
	_, item, err := s.get(ctx, id)
	return item, err
}

func (s ControlService) LatestVerification(ctx context.Context, id string) (control.LatestVerification, error) {
	release, err := s.Manager.admission.metadata.acquire(ctx)
	if err != nil {
		return control.LatestVerification{}, err
	}
	defer release()
	if err := control.ValidateID(id); err != nil {
		return control.LatestVerification{}, err
	}
	_, found, err := s.Manager.Get(ctx, id)
	if err != nil {
		return control.LatestVerification{}, err
	}
	if !found {
		return control.LatestVerification{}, fmt.Errorf("unknown runtime %q", id)
	}
	return s.Manager.reports.latest(id), nil
}
func (s ControlService) Start(ctx context.Context, id string) (control.Runtime, error) {
	r, _, err := s.get(ctx, id)
	if err != nil {
		return control.Runtime{}, err
	}
	if _, err := s.Manager.Up(ctx, r.ProjectDir); err != nil {
		return control.Runtime{}, err
	}
	return s.Get(ctx, id)
}
func (s ControlService) Stop(ctx context.Context, id string) (control.Runtime, error) {
	if _, _, err := s.get(ctx, id); err != nil {
		return control.Runtime{}, err
	}
	if _, err := s.Manager.Down(ctx, id); err != nil {
		return control.Runtime{}, err
	}
	return s.Get(ctx, id)
}

type boundedWriter struct {
	data      []byte
	max       int
	truncated bool
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if n >= w.max {
		w.data = append(w.data[:0], p[n-w.max:]...)
		w.truncated = true
	} else {
		if len(w.data)+n > w.max {
			w.data = append(w.data[:0], w.data[len(w.data)+n-w.max:]...)
			w.truncated = true
		}
		w.data = append(w.data, p...)
	}
	return n, nil
}

func (w *boundedWriter) String() string { return string(w.data) }

func (s ControlService) Logs(ctx context.Context, input control.LogsRequest) (control.LogsResult, error) {
	_, item, err := s.get(ctx, input.RuntimeID)
	if err != nil {
		return control.LogsResult{}, err
	}
	if input.Lines < 1 || input.Lines > 200 {
		return control.LogsResult{}, errors.New("lines must be between 1 and 200")
	}
	if input.ServiceKind != "" && input.Service == "" {
		return control.LogsResult{}, errors.New("service_kind requires a service")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	w := &boundedWriter{max: 64 << 10}
	if input.Service != "" {
		err = s.Manager.LogsService(ctx, input.RuntimeID, input.Service, input.ServiceKind, w)
	} else {
		err = s.Manager.Logs(ctx, input.RuntimeID, false, w)
	}
	if err != nil {
		return control.LogsResult{}, err
	}
	text := w.String()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) > input.Lines {
		text = strings.Join(lines[len(lines)-input.Lines:], "\n") + "\n"
		w.truncated = true
	}
	return control.LogsResult{Runtime: item, Service: input.Service, ServiceKind: input.ServiceKind, Text: text, Truncated: w.truncated}, nil
}

func (s ControlService) RunCommand(ctx context.Context, input control.CommandRequest) (control.CommandResult, error) {
	release, err := s.Manager.admission.commands.acquire(ctx)
	if err != nil {
		return control.CommandResult{}, err
	}
	defer release()
	policy := s.Policy
	if s.PolicyForRuntime != nil {
		var err error
		policy, err = s.PolicyForRuntime(input.RuntimeID)
		if err != nil {
			return control.CommandResult{}, err
		}
	}
	if err := policy.Validate(input); err != nil {
		return control.CommandResult{}, err
	}
	r, item, err := s.get(ctx, input.RuntimeID)
	if err != nil {
		return control.CommandResult{}, err
	}
	if r.Observed != "running" {
		return control.CommandResult{}, errors.New("runtime is not running")
	}
	executor, ok := s.Backend.(CommandBackend)
	if !ok {
		return control.CommandResult{}, errors.New("runtime backend does not support scoped command execution")
	}
	p, err := ResolveProject(ctx, r.ProjectDir)
	if err != nil || p.ID != r.ID {
		return control.CommandResult{}, errors.New("worktree identity changed")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(input.TimeoutMS)*time.Millisecond)
	defer cancel()
	result, err := executor.RunCommand(ctx, p, input)
	result.Runtime = item
	return result, err
}

func (s ControlService) target(ctx context.Context, id string, endpointName string) (verify.Target, error) {
	r, item, err := s.get(ctx, id)
	if err != nil {
		return verify.Target{}, err
	}
	if r.Observed != "running" {
		return verify.Target{}, errors.New("runtime is not healthy")
	}
	p, err := ResolveProject(ctx, r.ProjectDir)
	if err != nil || p.ID != r.ID {
		return verify.Target{}, errors.New("worktree identity changed")
	}
	backend, ok := s.Manager.backend.(interface {
		Ports(context.Context, Project) ([]PortBinding, error)
	})
	if !ok {
		return verify.Target{}, errors.New("endpoint backend unavailable")
	}
	bindings, err := backend.Ports(ctx, p)
	if err != nil {
		return verify.Target{}, err
	}
	var selected []PortBinding
	for _, binding := range bindings {
		if binding.Protocol != "http" && binding.Protocol != "https" {
			continue
		}
		if (endpointName != "" && binding.Name == endpointName) || (endpointName == "" && binding.Browser) {
			selected = append(selected, binding)
		}
	}
	if len(selected) != 1 {
		return verify.Target{}, errors.New("select exactly one running HTTP endpoint by name")
	}
	binding := selected[0]
	target := verify.Target{Runtime: item, ProjectDir: r.ProjectDir, HTTPURL: strings.TrimSuffix(binding.URL, "/")}
	if binding.Service != "" {
		target.BrowserURL = binding.Protocol + "://" + net.JoinHostPort(binding.Service, strconv.Itoa(binding.ContainerPort))
	}
	return target, nil
}
func (s ControlService) VerifyHTTP(ctx context.Context, input control.VerifyRequest) (control.VerifyResult, error) {
	started := time.Now()
	release, err := s.Manager.admission.verification.acquire(ctx)
	if err != nil {
		return control.VerifyResult{}, err
	}
	defer release()
	unlock, err := s.Manager.lock(ctx, input.RuntimeID)
	if err != nil {
		return control.VerifyResult{}, err
	}
	defer unlock()
	t, err := s.target(ctx, input.RuntimeID, input.Endpoint)
	if err != nil {
		return control.VerifyResult{}, err
	}
	result, err := s.Verifier.HTTP(ctx, t, input)
	if err == nil && !result.OK {
		result.Logs = s.failureLogs(ctx, input.RuntimeID)
	}
	if err == nil {
		s.Manager.reports.record(input.RuntimeID, "http", started, result)
	}
	return result, err
}
func (s ControlService) VerifyBrowser(ctx context.Context, input control.VerifyRequest) (control.VerifyResult, error) {
	started := time.Now()
	release, err := s.Manager.admission.verification.acquire(ctx)
	if err != nil {
		return control.VerifyResult{}, err
	}
	defer release()
	unlock, err := s.Manager.lock(ctx, input.RuntimeID)
	if err != nil {
		return control.VerifyResult{}, err
	}
	defer unlock()
	t, err := s.target(ctx, input.RuntimeID, input.Endpoint)
	if err != nil {
		return control.VerifyResult{}, err
	}
	p, err := ResolveProject(ctx, t.ProjectDir)
	if err != nil {
		return control.VerifyResult{}, err
	}
	if p.Config.Browser.Service == "" {
		return control.VerifyResult{}, errors.New("browser verification is unconfigured; set browser.service in .fugato.yaml")
	}
	if t.BrowserURL == "" {
		return control.VerifyResult{}, errors.New("browser automation requires a Compose endpoint; native endpoints support HTTP and task checks")
	}
	result, err := s.Verifier.Browser(ctx, t, input)
	if err == nil && !result.OK {
		result.Logs = s.failureLogs(ctx, input.RuntimeID)
	}
	if err == nil {
		s.Manager.reports.record(input.RuntimeID, "browser", started, result)
	}
	return result, err
}
func (s ControlService) failureLogs(ctx context.Context, id string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	w := &boundedWriter{max: 16 << 10}
	if err := s.Manager.Logs(ctx, id, false, w); err != nil {
		return ""
	}
	return verify.RedactLogs(w.String())
}

var _ control.Service = ControlService{}
var _ control.PrepareService = ControlService{}
