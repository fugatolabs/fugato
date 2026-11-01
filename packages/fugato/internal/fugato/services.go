package fugato

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// RuntimeService describes observed processes and ports, without implying readiness.
type RuntimeService struct {
	Name   string        `json:"name"`
	Kind   string        `json:"kind"`
	State  string        `json:"state"`
	Health string        `json:"health,omitempty"`
	Ports  []PortBinding `json:"ports"`
}

func (m *Manager) Services(ctx context.Context, id string) ([]RuntimeService, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
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
	if !found || r.Observed != "running" {
		return nil, errors.New("runtime is not running")
	}
	p, err := ResolveProject(ctx, r.ProjectDir)
	if err != nil {
		return nil, err
	}
	if p.ID != id {
		return nil, errors.New("worktree identity changed; refusing to inspect services")
	}
	backend, ok := m.backend.(interface {
		Services(context.Context, Project) ([]RuntimeService, error)
	})
	if !ok {
		return nil, errors.New("runtime backend does not report services")
	}
	services, err := backend.Services(ctx, p)
	if err != nil {
		return nil, err
	}
	for i := range services {
		services[i].Ports = m.previewBindings(id, services[i].Ports)
	}
	return services, nil
}

func (b *EnvironmentBackend) Services(ctx context.Context, p Project) ([]RuntimeService, error) {
	services := []RuntimeService{}
	if p.ComposeFile != "" {
		var err error
		services, err = b.ComposeBackend.Services(ctx, p)
		if err != nil {
			return nil, err
		}
	}
	b.mu.Lock()
	if env := b.environments[p.ID]; env != nil {
		for name, process := range env.processes {
			service := RuntimeService{Name: name, Kind: "native", State: "running", Ports: []PortBinding{}}
			select {
			case <-process.done:
				service.State = "exited"
			default:
			}
			if service.State == "running" {
				for _, binding := range env.bindings {
					if binding.Command == name {
						service.Ports = append(service.Ports, binding)
					}
				}
			}
			services = append(services, service)
		}
	}
	b.mu.Unlock()
	slices.SortFunc(services, func(a, b RuntimeService) int { return strings.Compare(a.Kind+":"+a.Name, b.Kind+":"+b.Name) })
	return services, nil
}

type serviceDockerCommand func(context.Context, ...string) ([]byte, error)

func (ComposeBackend) Services(ctx context.Context, p Project) ([]RuntimeService, error) {
	return inspectComposeServices(ctx, p, serviceDockerOutput)
}

// Inspect only owned containers. Configuration and project scripts are never executed.
func inspectComposeServices(ctx context.Context, p Project, run serviceDockerCommand) ([]RuntimeService, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := run(ctx, "container", "ls", "--all", "--no-trunc", "--filter", "label=com.docker.compose.project="+p.ComposeProject, "--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(output))
	if len(ids) > 256 {
		return nil, errors.New("service inventory exceeds 256 containers")
	}
	services := []RuntimeService{}
	if len(ids) == 0 {
		return services, nil
	}
	for _, id := range ids {
		if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
			return nil, errors.New("invalid service container ID")
		}
	}
	output, err = run(ctx, append([]string{"container", "inspect"}, ids...)...)
	if err != nil {
		return nil, err
	}
	var containers []struct {
		ID     string `json:"Id"`
		Config struct {
			Labels       map[string]string          `json:"Labels"`
			ExposedPorts map[string]json.RawMessage `json:"ExposedPorts"`
		} `json:"Config"`
		State struct {
			Status string `json:"Status"`
			Health *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(output, &containers); err != nil {
		return nil, fmt.Errorf("decode service inventory: %w", err)
	}
	if len(containers) != len(ids) {
		return nil, errors.New("incomplete service container inventory")
	}
	seen := map[string]bool{}
	for _, container := range containers {
		labels := container.Config.Labels
		if !slices.Contains(ids, container.ID) || seen[container.ID] || labels["com.docker.compose.project"] != p.ComposeProject || labels["com.docker.compose.service"] == "" || !filepath.IsAbs(labels["com.docker.compose.project.working_dir"]) || !within(p.Worktree, labels["com.docker.compose.project.working_dir"]) {
			return nil, errors.New("service container ownership mismatch")
		}
		seen[container.ID] = true
		if strings.EqualFold(labels["com.docker.compose.oneoff"], "true") {
			continue
		}
		name := labels["com.docker.compose.service"]
		index := slices.IndexFunc(services, func(s RuntimeService) bool { return s.Name == name })
		if index < 0 {
			services = append(services, RuntimeService{Name: name, Kind: "compose", State: container.State.Status, Ports: []PortBinding{}})
			index = len(services) - 1
		}
		service := &services[index]
		if service.State != container.State.Status {
			service.State = "mixed"
		}
		if container.State.Health != nil {
			if service.Health != "" && service.Health != container.State.Health.Status {
				service.Health = "mixed"
			} else {
				service.Health = container.State.Health.Status
			}
		}
		keys := map[string]bool{}
		for key := range container.Config.ExposedPorts {
			keys[key] = true
		}
		for key := range container.NetworkSettings.Ports {
			keys[key] = true
		}
		if len(keys) > 1000 {
			return nil, errors.New("service port inventory exceeds limit")
		}
		for key := range keys {
			number, protocol, ok := strings.Cut(key, "/")
			port, err := strconv.Atoi(number)
			if !ok || err != nil || port < 1 || port > 65535 || (protocol != "tcp" && protocol != "udp") {
				return nil, errors.New("invalid service container port")
			}
			base := PortBinding{Service: name, ContainerPort: port, Protocol: protocol}
			bindings := container.NetworkSettings.Ports[key]
			if container.State.Status != "running" || len(bindings) == 0 {
				service.Ports = append(service.Ports, base)
				continue
			}
			for _, published := range bindings {
				hostPort, err := strconv.Atoi(published.HostPort)
				if err != nil || hostPort < 1 || hostPort > 65535 || net.ParseIP(published.HostIP) == nil {
					return nil, errors.New("invalid published service port")
				}
				binding := base
				binding.HostIP, binding.HostPort = published.HostIP, hostPort
				matched := false
				for endpointName, endpoint := range p.Config.Endpoints {
					if endpoint.Service == name && endpoint.Port == port && endpointProtocol(endpoint.Protocol) == protocol {
						named := binding
						named.Name, named.Protocol, named.Browser = endpointName, endpoint.Protocol, endpoint.Browser
						setEndpointURL(&named)
						service.Ports = append(service.Ports, named)
						matched = true
					}
				}
				if !matched {
					service.Ports = append(service.Ports, binding)
				}
			}
		}
	}
	for i := range services {
		slices.SortFunc(services[i].Ports, func(a, b PortBinding) int {
			return strings.Compare(fmt.Sprintf("%05d/%s/%05d/%s/%s", a.ContainerPort, a.Protocol, a.HostPort, a.HostIP, a.Name), fmt.Sprintf("%05d/%s/%05d/%s/%s", b.ContainerPort, b.Protocol, b.HostPort, b.HostIP, b.Name))
		})
		services[i].Ports = slices.Compact(services[i].Ports)
	}
	slices.SortFunc(services, func(a, b RuntimeService) int { return strings.Compare(a.Name, b.Name) })
	return services, nil
}

func serviceDockerOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = dockerEnvironment()
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("inspect Docker services: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() >= 1<<20 {
		return nil, errors.New("service inventory exceeds 1 MiB")
	}
	return stdout.Bytes(), nil
}
