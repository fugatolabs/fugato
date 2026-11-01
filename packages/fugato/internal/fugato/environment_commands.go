package fugato

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type NamedCommandResult struct {
	Name      string `json:"name"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
}
type NamedCheckResult struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Status    int    `json:"status,omitempty"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// RunNamed accepts only commands saved in the validated configuration. It does
// not accept caller-supplied argv and does not use the agent exec allowlist.
func (b *EnvironmentBackend) RunNamed(ctx context.Context, p Project, name string, interactive bool, input io.Reader, output, errOutput io.Writer) (NamedCommandResult, error) {
	result := NamedCommandResult{Name: name}
	command, ok := p.Config.Commands[name]
	if !ok {
		return result, fmt.Errorf("unknown command %q", name)
	}
	if command.Mode == "service" {
		return result, errors.New("persistent commands must be selected in start.commands and started with up")
	}
	if command.Mode == "interactive" && !interactive {
		return result, errors.New("interactive command requires fugato run in a terminal")
	}
	if !interactive {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}
	var cmd *exec.Cmd
	var err error
	cleanup := func() {}
	if command.Host != nil {
		bindings := b.Snapshot(p.ID)
		if !interactive {
			bindings, err = b.Ports(ctx, p)
		}
		if err == nil {
			cmd, err = b.hostCommand(ctx, p, command.Host, bindings)
		}
	} else {
		p, err = b.ComposeBackend.Prepare(ctx, p)
		if err == nil {
			// Use a temporary override file so stdin belongs to the interactive command.
			var override string
			override, err = writeCommandOverride(p)
			if err == nil {
				p.ComposeFiles = append(p.ComposeFiles, override)
				p.OverrideYAML = ""
				cleanup = func() { _ = os.Remove(override) }
				var tail []string
				if command.Run != nil {
					nonce := make([]byte, 8)
					if _, err = rand.Read(nonce); err != nil {
						cleanup()
						return result, err
					}
					container := fmt.Sprintf("%s-task-%x", p.ComposeProject, nonce)
					tail = []string{"run", "--rm", "--name", container}
					if !interactive {
						tail = append(tail, "-T")
					}
					// Dependencies are started deliberately through up. Tasks do not create
					// untracked dependency containers outside the runtime lifecycle.
					tail = append(tail, "--no-deps", command.Run.Service)
					tail = append(tail, command.Run.Argv...)
					previousCleanup := cleanup
					cleanup = func() {
						previousCleanup()
						if ctx.Err() != nil {
							cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
							defer cancel()
							remove := exec.CommandContext(cleanupCtx, "docker", "rm", "-f", container)
							remove.Env = dockerEnvironment()
							_ = remove.Run()
						}
					}
				} else {
					tail = []string{"exec"}
					if !interactive {
						tail = append(tail, "-T")
					}
					tail = append(tail, command.Exec.Service)
					if !interactive {
						tail = append(tail, "timeout", "-s", "KILL", "300s")
					}
					tail = append(tail, command.Exec.Argv...)
				}
				cmd = composeCommand(ctx, p, tail...)
			}
		}
	}
	defer cleanup()
	if err != nil {
		return result, err
	}
	stdout, stderr := &capBuffer{max: 64 << 10}, &capBuffer{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if interactive {
		if command.Host != nil {
			cmd.SysProcAttr = nil
			cmd.Cancel = func() error { return cmd.Process.Kill() }
		}
		cmd.Stdin = input
		cmd.Stdout, cmd.Stderr = output, errOutput
	} else {
		if output != nil {
			cmd.Stdout = io.MultiWriter(stdout, output)
		}
		if errOutput != nil {
			cmd.Stderr = io.MultiWriter(stderr, errOutput)
		}
	}
	var runErr error
	if command.Host != nil && !interactive {
		if runErr = cmd.Start(); runErr == nil {
			process := &nativeProcess{cmd: cmd, done: make(chan struct{})}
			go process.wait()
			<-process.done
			runErr = process.err
		}
	} else {
		runErr = cmd.Run()
	}
	result.Stdout, result.Stderr = strings.ToValidUTF8(stdout.String(), "�"), strings.ToValidUTF8(stderr.String(), "�")
	result.Truncated = stdout.exceeded || stderr.exceeded
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if runErr == nil {
		return result, nil
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		result.ExitCode = exit.ExitCode()
		return result, nil
	}
	return result, runErr
}
func writeCommandOverride(p Project) (string, error) {
	file, err := os.CreateTemp("", "fugato-compose-*.yaml")
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(file, p.OverrideYAML); err != nil {
		file.Close()
		os.Remove(file.Name())
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

// RunInteractive is deliberately foreground-only: the invoking terminal owns
// input, output and cancellation. No daemon is started for a host-only command.
func RunInteractive(ctx context.Context, p Project, name, stateDir string, bindings []PortBinding, input io.Reader, output, errOutput io.Writer) (NamedCommandResult, error) {
	backend := NewEnvironmentBackend(stateDir)
	backend.environments[p.ID] = &nativeEnvironment{project: p, bindings: bindings}
	return backend.RunNamed(ctx, p, name, true, input, output, errOutput)
}

func (m *Manager) namedProject(ctx context.Context, path, id string) (Project, error) {
	if id != "" {
		r, found, err := m.store.Get(ctx, id)
		if err != nil {
			return Project{}, err
		}
		if !found {
			return Project{}, errors.New("unknown runtime")
		}
		path = r.ProjectDir
	}
	p, err := ResolveProject(ctx, path)
	if err != nil {
		return Project{}, err
	}
	if id != "" && p.ID != id {
		return Project{}, errors.New("worktree identity changed")
	}
	return p, nil
}
func (m *Manager) RunNamed(ctx context.Context, path, id, name string) (NamedCommandResult, error) {
	release, err := m.admission.commands.acquire(ctx)
	if err != nil {
		return NamedCommandResult{}, err
	}
	defer release()
	p, err := m.namedProject(ctx, path, id)
	if err != nil {
		return NamedCommandResult{}, err
	}
	unlock, err := m.lock(ctx, p.ID)
	if err != nil {
		return NamedCommandResult{}, err
	}
	defer unlock()
	backend, ok := m.backend.(*EnvironmentBackend)
	if !ok {
		return NamedCommandResult{}, errors.New("named command backend unavailable")
	}
	return backend.RunNamed(ctx, p, name, false, nil, nil, nil)
}
func (m *Manager) CheckNamed(ctx context.Context, path, id, name string) (NamedCheckResult, error) {
	result := NamedCheckResult{Name: name}
	release, err := m.admission.verification.acquire(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	p, err := m.namedProject(ctx, path, id)
	if err != nil {
		return result, err
	}
	check, ok := p.Config.Checks[name]
	if !ok {
		return result, fmt.Errorf("unknown check %q", name)
	}
	if check.Command != "" {
		command, err := m.RunNamed(ctx, p.Dir, "", check.Command)
		result.OK = err == nil && command.ExitCode == 0
		result.ExitCode = command.ExitCode
		result.Stdout, result.Stderr = command.Stdout, command.Stderr
		result.Truncated = command.Truncated
		return result, err
	}
	unlock, err := m.lock(ctx, p.ID)
	if err != nil {
		return result, err
	}
	defer unlock()
	backend, ok := m.backend.(interface {
		Ports(context.Context, Project) ([]PortBinding, error)
	})
	if !ok {
		return result, errors.New("endpoint backend unavailable")
	}
	bindings, err := backend.Ports(ctx, p)
	if err != nil {
		return result, err
	}
	var target string
	for _, binding := range bindings {
		if binding.Name == check.HTTP.Endpoint {
			target = strings.TrimSuffix(binding.URL, "/") + check.HTTP.Path
		}
	}
	if target == "" {
		return result, errors.New("check endpoint is unavailable; start its environment first")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return result, err
	}
	client := http.Client{Timeout: 10 * time.Second, Transport: healthClient.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	result.Status = response.StatusCode
	result.OK = response.StatusCode >= 200 && response.StatusCode < 300
	return result, nil
}
