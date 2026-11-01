package fugato

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"
)

func endpointProtocol(protocol string) string {
	if protocol == "udp" {
		return "udp"
	}
	return "tcp"
}

func composePortOverride(p Project, model composeModel) (string, error) {
	selected, err := selectedServices(p, model)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	slices.Sort(names)
	var out strings.Builder
	out.WriteString("services:\n")
	if len(names) == 0 {
		return "services: {}\n", nil
	}
	for _, name := range names {
		fmt.Fprintf(&out, "  %s:\n    ports: !override", name)
		ports := map[string]EndpointConfig{}
		for _, port := range model.Services[name].Ports {
			protocol := port.Protocol
			if protocol == "" {
				protocol = "tcp"
			}
			if port.Target < 1 || port.Target > 65535 || (protocol != "tcp" && protocol != "udp") || (port.Mode != "" && port.Mode != "ingress") || strings.Contains(string(port.Published), "-") {
				return "", fmt.Errorf("service %q has unsupported published port", name)
			}
			key := fmt.Sprintf("%05d/%s", port.Target, protocol)
			if _, exists := ports[key]; exists {
				return "", fmt.Errorf("service %q publishes %d/%s more than once", name, port.Target, protocol)
			}
			ports[key] = EndpointConfig{Service: name, Port: port.Target, Protocol: protocol}
		}
		for _, ep := range p.Config.Endpoints {
			if ep.Service == name {
				ports[fmt.Sprintf("%05d/%s", ep.Port, endpointProtocol(ep.Protocol))] = ep
			}
		}
		if len(ports) == 0 {
			out.WriteString(" []\n")
			continue
		}
		out.WriteString("\n")
		keys := make([]string, 0, len(ports))
		for key := range ports {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			ep := ports[key]
			fmt.Fprintf(&out, "      - target: %d\n        host_ip: 127.0.0.1\n        protocol: %s\n", ep.Port, endpointProtocol(ep.Protocol))
		}
	}
	return out.String(), nil
}

func (c ComposeBackend) Start(ctx context.Context, p Project) error {
	if len(p.Config.Start.Services) == 0 {
		return nil
	}
	p, err := c.Prepare(ctx, p)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	args := append([]string{"up", "--build", "-d", "--wait"}, p.Config.Start.Services...)
	if _, err := composeOutput(ctx, p, args...); err != nil {
		return cleanupFailedStart(ctx, p, fmt.Errorf("start Compose project: %w", err))
	}
	return nil
}

func (c ComposeBackend) Inspect(ctx context.Context, p Project) (bool, error) {
	if len(p.Config.Start.Services) == 0 {
		return true, nil
	}
	p, err := c.Prepare(ctx, p)
	if err != nil {
		return false, err
	}
	out, err := composeOutput(ctx, p, "ps", "--status", "running", "--services")
	if err != nil {
		return false, err
	}
	running := strings.Fields(string(out))
	for _, name := range p.Config.Start.Services {
		if !slices.Contains(running, name) {
			return false, nil
		}
	}
	return true, nil
}

func (c ComposeBackend) Ports(ctx context.Context, p Project) ([]PortBinding, error) {
	bindings := []PortBinding{}
	if p.ComposeFile == "" {
		return bindings, nil
	}
	p, err := c.Prepare(ctx, p)
	if err != nil {
		return nil, err
	}
	out, err := composeOutput(ctx, p, "ps", "--status", "running", "--services")
	if err != nil {
		return nil, err
	}
	running := strings.Fields(string(out))
	model, err := readComposeModel(ctx, p)
	if err != nil {
		return nil, err
	}
	declared := map[string]bool{}
	for _, ep := range p.Config.Endpoints {
		if ep.Service != "" {
			declared[fmt.Sprintf("%s:%d/%s", ep.Service, ep.Port, endpointProtocol(ep.Protocol))] = true
		}
	}
	for _, service := range running {
		for _, port := range model.Services[service].Ports {
			protocol := endpointProtocol(port.Protocol)
			key := fmt.Sprintf("%s:%d/%s", service, port.Target, protocol)
			if declared[key] {
				continue
			}
			out, err := composeOutput(ctx, p, "port", "--protocol", protocol, service, strconv.Itoa(port.Target))
			if err != nil {
				return nil, err
			}
			host, number, err := net.SplitHostPort(strings.TrimSpace(string(out)))
			if err != nil || host != "127.0.0.1" {
				return nil, errors.New("Compose endpoint must be loopback")
			}
			hostPort, err := strconv.Atoi(number)
			if err != nil || hostPort < 1 || hostPort > 65535 {
				return nil, errors.New("invalid Compose host port")
			}
			bindings = append(bindings, PortBinding{Service: service, ContainerPort: port.Target, Protocol: protocol, HostIP: host, HostPort: hostPort})
		}
	}
	names := make([]string, 0, len(p.Config.Endpoints))
	for name := range p.Config.Endpoints {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		ep := p.Config.Endpoints[name]
		if ep.Service == "" || !slices.Contains(running, ep.Service) {
			continue
		}
		out, err := composeOutput(ctx, p, "port", "--protocol", endpointProtocol(ep.Protocol), ep.Service, strconv.Itoa(ep.Port))
		if err != nil {
			return nil, err
		}
		host, number, err := net.SplitHostPort(strings.TrimSpace(string(out)))
		if err != nil || host != "127.0.0.1" {
			return nil, errors.New("Compose endpoint must be loopback")
		}
		port, err := strconv.Atoi(number)
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("Compose endpoint port is invalid")
		}
		binding := PortBinding{Name: name, Service: ep.Service, ContainerPort: ep.Port, Protocol: ep.Protocol, HostIP: host, HostPort: port, Browser: ep.Browser}
		setEndpointURL(&binding)
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func setEndpointURL(binding *PortBinding) {
	if binding.Protocol == "http" || binding.Protocol == "https" {
		binding.URL = binding.Protocol + "://" + net.JoinHostPort(binding.HostIP, strconv.Itoa(binding.HostPort)) + "/"
	}
}
