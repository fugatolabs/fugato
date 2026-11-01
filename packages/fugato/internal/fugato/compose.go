package fugato

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Backend interface {
	Start(context.Context, Project) error
	Inspect(context.Context, Project) (bool, error)
	Stop(context.Context, Project) error
	Logs(context.Context, Project, bool, io.Writer) error
}

type ComposeBackend struct{}

var healthClient = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}

type composeModel struct {
	Services map[string]composeService `json:"services"`
	Volumes  map[string]struct {
		External bool   `json:"external"`
		Name     string `json:"name"`
	} `json:"volumes"`
	Networks map[string]struct {
		External bool   `json:"external"`
		Name     string `json:"name"`
	} `json:"networks"`
	Secrets map[string]json.RawMessage `json:"secrets"`
	Configs map[string]json.RawMessage `json:"configs"`
}

type composeService struct {
	Build struct {
		Context            string          `json:"context"`
		Dockerfile         string          `json:"dockerfile"`
		AdditionalContexts json.RawMessage `json:"additional_contexts"`
		Secrets            json.RawMessage `json:"secrets"`
		SSH                json.RawMessage `json:"ssh"`
	} `json:"build"`
	CPUs     float64 `json:"cpus"`
	MemLimit string  `json:"mem_limit"`
	Deploy   struct {
		Resources struct {
			Limits struct {
				CPUs   float64 `json:"cpus"`
				Memory string  `json:"memory"`
			} `json:"limits"`
		} `json:"resources"`
	} `json:"deploy"`
	Profiles      []string                   `json:"profiles"`
	Expose        []string                   `json:"expose"`
	DependsOn     map[string]json.RawMessage `json:"depends_on"`
	Networks      map[string]json.RawMessage `json:"networks"`
	Environment   map[string]any             `json:"environment"`
	Secrets       json.RawMessage            `json:"secrets"`
	Configs       json.RawMessage            `json:"configs"`
	NetworkMode   string                     `json:"network_mode"`
	Privileged    bool                       `json:"privileged"`
	ContainerName string                     `json:"container_name"`
	PID           string                     `json:"pid"`
	IPC           string                     `json:"ipc"`
	UserNSMode    string                     `json:"userns_mode"`
	CgroupNSMode  string                     `json:"cgroupns_mode"`
	Devices       []any                      `json:"devices"`
	CapAdd        []string                   `json:"cap_add"`
	SecurityOpt   []string                   `json:"security_opt"`
	VolumesFrom   []string                   `json:"volumes_from"`
	ExternalLinks []string                   `json:"external_links"`
	Volumes       []struct {
		Type   string `json:"type"`
		Source string `json:"source"`
	} `json:"volumes"`
	Ports []composePort `json:"ports"`
}

type composePort struct {
	HostIP    string          `json:"host_ip"`
	Target    int             `json:"target"`
	Protocol  string          `json:"protocol"`
	Published json.RawMessage `json:"published"`
	Mode      string          `json:"mode"`
}

// PortBinding is an assigned host endpoint for a running Compose service.
type PortBinding struct {
	Name          string `json:"name,omitempty"`
	Command       string `json:"command,omitempty"`
	Browser       bool   `json:"browser,omitempty"`
	URL           string `json:"url,omitempty"`
	Service       string `json:"service"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
	HostIP        string `json:"host_ip"`
	HostPort      int    `json:"host_port"`
}

func (ComposeBackend) Validate(ctx context.Context, p Project) error {
	_, err := (ComposeBackend{}).Prepare(ctx, p)
	return err
}

func readComposeModel(ctx context.Context, p Project) (composeModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	p.OverrideYAML = ""
	out, err := composeOutput(ctx, p, "--profile", "*", "config", "--format", "json")
	if err != nil {
		return composeModel{}, fmt.Errorf("cannot read Compose configuration: %w", err)
	}
	var model composeModel
	if err := json.Unmarshal(out, &model); err != nil {
		return composeModel{}, fmt.Errorf("invalid Compose JSON: %w", err)
	}
	return model, nil
}

// Prepare validates the source model and adds ephemeral loopback host bindings.
func (ComposeBackend) Prepare(ctx context.Context, p Project) (Project, error) {
	model, err := readComposeModel(ctx, p)
	if err != nil {
		return Project{}, err
	}
	for name, ep := range p.Config.Endpoints {
		if ep.Service == "" {
			continue
		}
		service, ok := model.Services[ep.Service]
		if !ok {
			return Project{}, fmt.Errorf("endpoint %q service is absent", name)
		}
		declared := false
		matching := false
		for _, port := range service.Ports {
			declared = true
			if port.Target == ep.Port && endpointProtocol(port.Protocol) == endpointProtocol(ep.Protocol) {
				matching = true
			}
		}
		for _, value := range service.Expose {
			number, protocol, _ := strings.Cut(value, "/")
			port, _ := strconv.Atoi(number)
			declared = true
			if port == ep.Port && endpointProtocol(protocol) == endpointProtocol(ep.Protocol) {
				matching = true
			}
		}
		if declared && !matching {
			return Project{}, fmt.Errorf("endpoint %q port %d/%s is not declared by service %q", name, ep.Port, ep.Protocol, ep.Service)
		}
	}
	override, err := composePortOverride(p, model)
	if err != nil {
		return Project{}, err
	}
	p.OverrideYAML = override
	out, err := composeOutput(ctx, p, "--profile", "*", "config", "--format", "json")
	if err != nil {
		return Project{}, fmt.Errorf("cannot apply Fugato port bindings: %w", err)
	}
	var effective composeModel
	if err := json.Unmarshal(out, &effective); err != nil {
		return Project{}, fmt.Errorf("invalid effective Compose JSON: %w", err)
	}
	if err := validateComposeModel(p, effective); err != nil {
		return Project{}, err
	}
	return p, nil
}

func validateComposeModel(p Project, model composeModel) error {
	selected, err := selectedServices(p, model)
	if err != nil {
		return err
	}
	usedVolumes := map[string]bool{}
	usedNetworks := map[string]bool{}
	for name := range selected {
		service := model.Services[name]
		if len(service.Secrets) > 0 && string(service.Secrets) != "null" || len(service.Configs) > 0 && string(service.Configs) != "null" {
			return fmt.Errorf("service %q uses unsupported Compose secrets or configs", name)
		}
		for network := range service.Networks {
			usedNetworks[network] = true
		}
		if service.NetworkMode != "" {
			return fmt.Errorf("service %q uses unsupported network_mode %q; use a project-scoped Compose network", name, service.NetworkMode)
		}
		if service.Privileged || service.ContainerName != "" || service.PID != "" || service.IPC != "" || service.UserNSMode != "" || service.CgroupNSMode != "" || len(service.Devices) > 0 || len(service.CapAdd) > 0 || len(service.SecurityOpt) > 0 || len(service.VolumesFrom) > 0 || len(service.ExternalLinks) > 0 {
			return fmt.Errorf("service %q uses unsupported host or privilege settings", name)
		}
		if len(service.Build.AdditionalContexts) > 0 && string(service.Build.AdditionalContexts) != "null" || len(service.Build.Secrets) > 0 && string(service.Build.Secrets) != "null" || len(service.Build.SSH) > 0 && string(service.Build.SSH) != "null" {
			return fmt.Errorf("service %q uses unsupported build contexts or credentials", name)
		}
		if service.Build.Context != "" {
			contextPath, err := filepath.EvalSymlinks(service.Build.Context)
			if err != nil || !within(projectScope(p), contextPath) {
				return fmt.Errorf("service %q builds outside the project directory", name)
			}
			if service.Build.Dockerfile != "" {
				dockerfile := service.Build.Dockerfile
				if !filepath.IsAbs(dockerfile) {
					dockerfile = filepath.Join(contextPath, dockerfile)
				}
				dockerfile, err = filepath.EvalSymlinks(dockerfile)
				if err != nil || !within(projectScope(p), dockerfile) {
					return fmt.Errorf("service %q uses a Dockerfile outside the project directory", name)
				}
			}
		}
		for _, volume := range service.Volumes {
			switch volume.Type {
			case "bind":
				source, err := filepath.EvalSymlinks(volume.Source)
				if err != nil || !within(projectScope(p), source) {
					return fmt.Errorf("service %q mounts outside the project directory: %s", name, volume.Source)
				}
			case "volume":
				usedVolumes[volume.Source] = true
				if _, ok := model.Volumes[volume.Source]; !ok {
					return fmt.Errorf("service %q uses undeclared volume %q", name, volume.Source)
				}
			default:
				return fmt.Errorf("service %q uses unsupported mount type %q", name, volume.Type)
			}
		}
		for _, port := range service.Ports {
			if port.HostIP != "127.0.0.1" || (len(port.Published) > 0 && string(port.Published) != "null" && string(port.Published) != `""`) {
				return fmt.Errorf("service %q must use dynamic 127.0.0.1 host bindings", name)
			}

		}
	}
	for name, volume := range model.Volumes {
		if !usedVolumes[name] {
			continue
		}
		if volume.External || !strings.HasPrefix(volume.Name, p.ComposeProject+"_") {
			return fmt.Errorf("volume %q is external or has a shared explicit name", name)
		}
	}
	for name, network := range model.Networks {
		if len(usedNetworks) > 0 && !usedNetworks[name] {
			continue
		}
		if network.External || !strings.HasPrefix(network.Name, p.ComposeProject+"_") {
			return fmt.Errorf("network %q is external or has a shared explicit name", name)
		}
	}
	return nil
}

func selectedServices(p Project, model composeModel) (map[string]bool, error) {
	selected := map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if selected[name] {
			return nil
		}
		service, ok := model.Services[name]
		if !ok {
			return fmt.Errorf("selected service or dependency %q is missing from Compose", name)
		}
		selected[name] = true
		for dep := range service.DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range p.Config.Start.Services {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	for _, command := range p.Config.Commands {
		if command.Run != nil {
			if err := visit(command.Run.Service); err != nil {
				return nil, err
			}
		}
		if command.Exec != nil {
			if err := visit(command.Exec.Service); err != nil {
				return nil, err
			}
		}
	}
	if p.Config.Browser.Service != "" {
		if err := visit(p.Config.Browser.Service); err != nil {
			return nil, err
		}
	}
	return selected, nil
}

func projectScope(p Project) string {
	if p.Worktree != "" {
		return p.Worktree
	}
	return p.Dir
}

// A failed or cancelled Compose up can leave dependency containers running.
// Teardown is bounded and keeps named volumes for a later retry. The manager
// still holds this runtime's lifecycle gate until Start returns.
func cleanupFailedStart(ctx context.Context, p Project, startErr error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := composeOutput(cleanupCtx, p, "down", "--remove-orphans"); err != nil {
		return errors.Join(startErr, fmt.Errorf("clean up failed Compose start: %w", err))
	}
	return startErr
}

func (c ComposeBackend) Stop(ctx context.Context, p Project) error {
	var err error
	p, err = c.Prepare(ctx, p)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, err = composeOutput(ctx, p, "down") // Deliberately no -v: retain PostgreSQL data.
	return err
}

func (c ComposeBackend) Logs(ctx context.Context, p Project, follow bool, output io.Writer) error {
	return c.logs(ctx, p, "", follow, output)
}

func (c ComposeBackend) LogsService(ctx context.Context, p Project, service, kind string, output io.Writer) error {
	if kind != "" && kind != "compose" {
		return errors.New("Compose log service_kind must be compose")
	}
	if !serviceName.MatchString(service) {
		return errors.New("invalid log service name")
	}
	model, err := readComposeModel(ctx, p)
	if err != nil {
		return err
	}
	if _, found := model.Services[service]; !found {
		return fmt.Errorf("unknown Compose service %q", service)
	}
	return c.logs(ctx, p, service, false, output)
}

func (c ComposeBackend) logs(ctx context.Context, p Project, service string, follow bool, output io.Writer) error {
	var err error
	p, err = c.Prepare(ctx, p)
	if err != nil {
		return err
	}
	args := []string{"logs", "--no-color", "--tail", "200"}
	if follow {
		args = append(args, "--follow")
	} else {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
	}
	if service != "" {
		args = append(args, "--", service)
	}
	cmd := composeCommand(ctx, p, args...)
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if !follow {
		return cmd.Run()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// A quiet follow stream can publish headers once the subprocess exists.
	// This is process startup, not Docker readiness; later failures must still
	// be reported by the caller's terminal stream status.
	if started, ok := output.(interface{ Started() error }); ok {
		if err := started.Started(); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return err
		}
	}
	return cmd.Wait()
}

func composeCommand(ctx context.Context, p Project, tail ...string) *exec.Cmd {
	args := []string{"compose"}
	files := p.ComposeFiles
	if len(files) == 0 {
		files = []string{p.ComposeFile}
	}
	for _, file := range files {
		args = append(args, "-f", file)
	}
	if p.OverrideYAML != "" {
		args = append(args, "-f", "-")
	}
	args = append(args, "-p", p.ComposeProject)
	args = append(args, tail...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = p.Dir
	cmd.Env = dockerEnvironment()
	if p.OverrideYAML != "" {
		cmd.Stdin = strings.NewReader(p.OverrideYAML)
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

func dockerEnvironment() []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "USER": true, "TMPDIR": true,
		"DOCKER_HOST": true, "DOCKER_CONTEXT": true, "DOCKER_CONFIG": true, "XDG_CONFIG_HOME": true, "SSH_AUTH_SOCK": true}
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if allowed[key] {
			env = append(env, value)
		}
	}
	return env
}

func composeOutput(ctx context.Context, p Project, tail ...string) ([]byte, error) {
	cmd := composeCommand(ctx, p, tail...)
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("docker compose %s failed: %w: %s", strings.Join(tail, " "), err, strings.TrimSpace(stderr.String()))
	}
	return bytes.TrimSpace(stdout.Bytes()), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	const maxOutput = 1 << 20
	if b.Len() < maxOutput {
		b.Buffer.Write(p[:min(len(p), maxOutput-b.Len())])
	}
	return len(p), nil
}
