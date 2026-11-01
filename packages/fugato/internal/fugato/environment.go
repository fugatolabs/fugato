package fugato

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// EnvironmentBackend owns native child processes for this daemon instance.
// Process identities are never restored from disk or signalled by a later daemon.
type EnvironmentBackend struct {
	ComposeBackend
	stateDir     string
	mu           sync.Mutex
	environments map[string]*nativeEnvironment
	closed       bool
}
type nativeEnvironment struct {
	project   Project
	processes map[string]*nativeProcess
	bindings  []PortBinding
	logs      *nativeLog
}
type nativeProcess struct {
	cmd    *exec.Cmd
	logs   *nativeLog
	done   chan struct{}
	mu     sync.Mutex
	exited bool
	err    error
}
type nativeLog struct {
	mu       sync.Mutex
	text     boundedWriter
	sequence int64
}

func (l *nativeLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sequence++
	return l.text.Write(p)
}
func (l *nativeLog) snapshot() (string, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.String(), l.sequence
}

func NewEnvironmentBackend(stateDir string) *EnvironmentBackend {
	return &EnvironmentBackend{stateDir: stateDir, environments: map[string]*nativeEnvironment{}}
}
func (b *EnvironmentBackend) environment(id string) *nativeEnvironment {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.environments[id]
}
func (b *EnvironmentBackend) Start(ctx context.Context, p Project) error {
	if old := b.environment(p.ID); old != nil {
		if err := b.stopNative(ctx, old); err != nil {
			return err
		}
	}
	env := &nativeEnvironment{project: p, processes: map[string]*nativeProcess{}, logs: &nativeLog{text: boundedWriter{max: 256 << 10}}}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("native process owner is shutting down")
	}
	if _, ok := b.environments[p.ID]; !ok && len(b.environments) >= 100 {
		b.mu.Unlock()
		return errors.New("native environment limit reached")
	}
	b.environments[p.ID] = env
	b.mu.Unlock()
	fail := func(err error) error {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return errors.Join(err, b.Stop(cleanup, p))
	}
	if p.ComposeFile != "" {
		if err := b.ComposeBackend.Start(ctx, p); err != nil {
			return fail(err)
		}
	}
	// Reserve all selected native endpoint ports together, then release just before launch.
	var reservations []net.Listener
	defer func() {
		for _, listener := range reservations {
			_ = listener.Close()
		}
	}()
	names := make([]string, 0, len(p.Config.Endpoints))
	for name := range p.Config.Endpoints {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		endpoint := p.Config.Endpoints[name]
		if endpoint.Command == "" || !slices.Contains(p.Config.Start.Commands, endpoint.Command) {
			continue
		}
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(endpoint.Port)))
		if err != nil {
			return fail(fmt.Errorf("reserve endpoint %s: %w", name, err))
		}
		reservations = append(reservations, listener)
		binding := PortBinding{Name: name, Command: endpoint.Command, ContainerPort: endpoint.Port, Protocol: endpoint.Protocol, HostIP: "127.0.0.1", HostPort: listener.Addr().(*net.TCPAddr).Port, Browser: endpoint.Browser}
		setEndpointURL(&binding)
		b.mu.Lock()
		env.bindings = append(env.bindings, binding)
		b.mu.Unlock()
	}
	if p.ComposeFile != "" {
		bindings, err := b.ComposeBackend.Ports(ctx, p)
		if err != nil {
			return fail(err)
		}
		b.mu.Lock()
		env.bindings = append(env.bindings, bindings...)
		b.mu.Unlock()
	}
	for _, name := range p.Config.Start.Commands {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		command := p.Config.Commands[name]
		if command.Mode == "task" {
			result, err := b.RunNamed(ctx, p, name, false, nil, env.logs, env.logs)
			if err != nil {
				return fail(err)
			}
			if result.ExitCode != 0 {
				return fail(fmt.Errorf("startup task %s exited with status %d", name, result.ExitCode))
			}
			continue
		}
		cmd, err := b.hostCommand(context.Background(), p, command.Host, env.bindings)
		if err != nil {
			return fail(err)
		}
		logs := &nativeLog{text: boundedWriter{max: 64 << 10}}
		output := io.MultiWriter(env.logs, logs)
		cmd.Stdout, cmd.Stderr = output, output
		for i, binding := range env.bindings {
			if binding.Command == name {
				_ = reservations[i].Close()
			}
		}
		process := &nativeProcess{cmd: cmd, logs: logs, done: make(chan struct{})}
		if err := cmd.Start(); err != nil {
			return fail(fmt.Errorf("start %s: %w", name, err))
		}
		b.mu.Lock()
		env.processes[name] = process
		b.mu.Unlock()
		go process.wait()
		// Catch immediate launch and bind failures without treating an HTTP response as readiness.
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-process.done:
			return fail(fmt.Errorf("service %s exited during startup: %v", name, process.err))
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}

	return nil
}
func (p *nativeProcess) wait() {
	// Keep the leader unreaped until its entire process group has been killed.
	// The reserved PID cannot be reused between group signalling and cmd.Wait.
	observeErr := waitNativeExit(p.cmd.Process.Pid)
	p.mu.Lock()
	if observeErr == nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
	p.err = errors.Join(observeErr, p.cmd.Wait())
	p.exited = true
	close(p.done)
	p.mu.Unlock()
}
func (p *nativeProcess) stop(ctx context.Context) error {
	p.mu.Lock()
	if p.exited {
		p.mu.Unlock()
		return nil
	}
	err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	p.mu.Unlock()
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
	case <-timer.C:
	}
	p.mu.Lock()
	if !p.exited {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
	p.mu.Unlock()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (b *EnvironmentBackend) stopNative(ctx context.Context, env *nativeEnvironment) error {
	b.mu.Lock()
	processes := make([]*nativeProcess, 0, len(env.processes))
	for _, p := range env.processes {
		processes = append(processes, p)
	}
	b.mu.Unlock()
	var errs []error
	for _, process := range processes {
		if err := process.stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
func (b *EnvironmentBackend) Stop(ctx context.Context, p Project) error {
	if env := b.environment(p.ID); env != nil {
		p = env.project
	}
	var nativeErr error
	if env := b.environment(p.ID); env != nil {
		nativeErr = b.stopNative(ctx, env)
		b.mu.Lock()
		env.bindings = nil
		b.mu.Unlock()
	}
	if p.ComposeFile != "" {
		return errors.Join(nativeErr, b.ComposeBackend.Stop(ctx, p))
	}
	return nativeErr
}
func (b *EnvironmentBackend) Close(ctx context.Context) error {
	b.mu.Lock()
	b.closed = true
	envs := make([]*nativeEnvironment, 0, len(b.environments))
	for _, env := range b.environments {
		envs = append(envs, env)
	}
	b.mu.Unlock()
	var errs []error
	for _, env := range envs {
		if err := b.stopNative(ctx, env); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
func (b *EnvironmentBackend) Inspect(ctx context.Context, p Project) (bool, error) {
	env := b.environment(p.ID)
	for _, name := range p.Config.Start.Commands {
		if p.Config.Commands[name].Mode != "service" {
			continue
		}
		if env == nil {
			return false, nil
		}
		b.mu.Lock()
		process := env.processes[name]
		b.mu.Unlock()
		if process == nil {
			return false, nil
		}
		select {
		case <-process.done:
			return false, fmt.Errorf("native service %s exited: %v", name, process.err)
		default:
		}
	}
	if p.ComposeFile != "" {
		running, err := b.ComposeBackend.Inspect(ctx, p)
		if err == nil && running {
			bindings, portsErr := b.Ports(ctx, p)
			if portsErr != nil {
				return false, portsErr
			}
			b.mu.Lock()
			if env == nil {
				env = &nativeEnvironment{project: p, processes: map[string]*nativeProcess{}, logs: &nativeLog{text: boundedWriter{max: 256 << 10}}}
				b.environments[p.ID] = env
			}
			env.bindings = bindings
			b.mu.Unlock()
		}
		return running, err
	}
	return env != nil, nil
}
func (b *EnvironmentBackend) Ports(ctx context.Context, p Project) ([]PortBinding, error) {
	bindings := []PortBinding{}
	b.mu.Lock()
	if env := b.environments[p.ID]; env != nil {
		for _, binding := range env.bindings {
			if binding.Command != "" {
				process := env.processes[binding.Command]
				if process == nil {
					continue
				}
				select {
				case <-process.done:
					continue
				default:
				}
				bindings = append(bindings, binding)
			}
		}
	}
	b.mu.Unlock()
	if p.ComposeFile != "" {
		compose, err := b.ComposeBackend.Ports(ctx, p)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, compose...)
	}
	slices.SortFunc(bindings, func(a, b PortBinding) int { return strings.Compare(a.Name, b.Name) })
	return bindings, nil
}
func (b *EnvironmentBackend) Snapshot(id string) []PortBinding {
	b.mu.Lock()
	defer b.mu.Unlock()
	if env := b.environments[id]; env != nil {
		return slices.Clone(env.bindings)
	}
	return nil
}
func (b *EnvironmentBackend) Logs(ctx context.Context, p Project, follow bool, output io.Writer) error {
	env := b.environment(p.ID)
	if env == nil && p.ComposeFile == "" {
		return errors.New("native logs are unavailable after daemon restart")
	}
	if !follow {
		if env != nil {
			text, _ := env.logs.snapshot()
			if _, err := io.WriteString(output, text); err != nil {
				return err
			}
		}
		if p.ComposeFile != "" {
			return b.ComposeBackend.Logs(ctx, p, false, output)
		}
		return nil
	}
	if p.ComposeFile != "" {
		followCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		writer := &synchronizedOutput{output: output}
		composeDone := make(chan error, 1)
		go func() { composeDone <- b.ComposeBackend.Logs(followCtx, p, true, writer); cancel() }()
		nativeProject := p
		nativeProject.ComposeFile = ""
		nativeErr := b.Logs(followCtx, nativeProject, true, writer)
		cancel()
		composeErr := <-composeDone
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(nativeErr, context.Canceled) {
			nativeErr = nil
		}
		if errors.Is(composeErr, context.Canceled) {
			composeErr = nil
		}
		return errors.Join(nativeErr, composeErr)
	}
	// One owner writes to output. Poll snapshots to avoid unbounded subscriber queues.
	if started, ok := output.(interface{ Started() error }); ok {
		if err := started.Started(); err != nil {
			return err
		}
	}
	var previous string
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if env != nil {
			text, _ := env.logs.snapshot()
			if text != previous {
				delta := text
				if strings.HasPrefix(text, previous) {
					delta = strings.TrimPrefix(text, previous)
				}
				if _, err := io.WriteString(output, delta); err != nil {
					return err
				}
				previous = text
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (b *EnvironmentBackend) LogsService(ctx context.Context, p Project, service, kind string, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !serviceName.MatchString(service) {
		return errors.New("invalid log service name")
	}
	if kind != "" && kind != "native" && kind != "compose" {
		return errors.New("service_kind must be native or compose")
	}
	command, native := p.Config.Commands[service]
	native = native && command.Mode == "service" && command.Host != nil
	if kind == "native" && !native {
		return fmt.Errorf("unknown native service %q", service)
	}
	if native && kind == "" && p.ComposeFile != "" {
		model, err := readComposeModel(ctx, p)
		if err != nil {
			return err
		}
		if _, found := model.Services[service]; found {
			return fmt.Errorf("service %q is ambiguous; choose service_kind native or compose", service)
		}
	}
	if native && kind != "compose" {
		b.mu.Lock()
		var logs *nativeLog
		if env := b.environments[p.ID]; env != nil {
			if process := env.processes[service]; process != nil {
				logs = process.logs
			}
		}
		b.mu.Unlock()
		if logs == nil {
			return errors.New("native service logs are unavailable; start the environment to capture logs")
		}
		text, _ := logs.snapshot()
		_, err := io.WriteString(output, text)
		return err
	}
	if p.ComposeFile != "" {
		return b.ComposeBackend.LogsService(ctx, p, service, kind, output)
	}
	return fmt.Errorf("unknown service %q", service)
}
func (b *EnvironmentBackend) hostCommand(ctx context.Context, p Project, host *HostCommand, bindings []PortBinding) (*exec.Cmd, error) {
	if host == nil || len(host.Argv) == 0 {
		return nil, errors.New("host command has no argv")
	}
	cwd := p.Dir
	if host.Cwd != "" {
		cwd = filepath.Join(p.Dir, host.Cwd)
	}
	cwd, err := filepath.EvalSymlinks(cwd)
	if err != nil || !within(projectScope(p), cwd) {
		return nil, errors.New("host cwd must exist within the worktree")
	}
	dataDir := filepath.Join(b.stateDir, "data", p.ID)
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	variables := map[string]string{"FUGATO_RUNTIME_ID": p.ID, "FUGATO_DATA_DIR": dataDir}
	for _, binding := range bindings {
		if binding.Name != "" {
			variables["FUGATO_PORT_"+binding.Name] = strconv.Itoa(binding.HostPort)
		}
	}
	expand := func(value string) (string, error) {
		var missing string
		value = nativeVariable.ReplaceAllStringFunc(value, func(token string) string {
			key := token[2 : len(token)-1]
			resolved, ok := variables[key]
			if !ok {
				missing = key
			}
			return resolved
		})
		if missing != "" {
			return "", fmt.Errorf("unavailable Fugato variable %s", missing)
		}
		return value, nil
	}
	argv := make([]string, len(host.Argv))
	for i, arg := range host.Argv {
		argv[i], err = expand(arg)
		if err != nil {
			return nil, err
		}
	}
	env := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	for key, value := range host.Env {
		env[key], err = expand(value)
		if err != nil {
			return nil, err
		}
	}
	for key, value := range variables {
		env[key] = value
	}
	executable, err := hostExecutable(argv[0], cwd, env["PATH"])
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, executable, argv[1:]...)
	cmd.Dir = cwd
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd, nil
}

var nativeVariable = regexp.MustCompile(`\$\{FUGATO_[A-Za-z0-9_.-]+\}`)

type synchronizedOutput struct {
	mu     sync.Mutex
	output io.Writer
}

func (w *synchronizedOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.Write(data)
}
func (w *synchronizedOutput) Started() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if writer, ok := w.output.(interface{ Started() error }); ok {
		return writer.Started()
	}
	return nil
}

// Resolve using the same cwd and PATH which the configured process receives.
func hostExecutable(name, cwd, pathValue string) (string, error) {
	candidates := []string{}
	if strings.ContainsRune(name, filepath.Separator) {
		if !filepath.IsAbs(name) {
			name = filepath.Join(cwd, name)
		}
		candidates = append(candidates, name)
	} else {
		for _, dir := range filepath.SplitList(pathValue) {
			if dir == "" {
				continue
			}
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(cwd, dir)
			}
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("host executable %q is unavailable on the configured PATH", name)
}

func (b *EnvironmentBackend) ownedProject(id string) (Project, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	env := b.environments[id]
	if env == nil {
		return Project{}, false
	}
	return env.project, true
}

// Stopping captured processes must remain possible after configuration edits.
// Recheck filesystem/Git identity without trusting the edited config contents.
func validateOwnedProjectIdentity(ctx context.Context, p Project) error {
	dir, err := filepath.EvalSymlinks(p.Dir)
	if err != nil || dir != p.Dir {
		return errors.New("owned project directory identity changed")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	root, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil || root != p.Worktree {
		return errors.New("owned worktree identity changed")
	}
	repository, err := gitOutput(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	repository, err = filepath.EvalSymlinks(repository)
	if err != nil || repository != p.Repository {
		return errors.New("owned repository identity changed")
	}
	return nil
}

// SnapshotReady distinguishes an observed zero-endpoint environment from a
// persisted running record whose live bindings have not been restored yet.
func (b *EnvironmentBackend) SnapshotReady(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.environments[id] != nil
}
