package fugato

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type StartConfig struct {
	Services []string `yaml:"services,omitempty" json:"services"`
	Commands []string `yaml:"commands,omitempty" json:"commands"`
}

type EndpointConfig struct {
	Service  string `yaml:"service" json:"service,omitempty"`
	Command  string `yaml:"command" json:"command,omitempty"`
	Port     int    `yaml:"port" json:"port"`
	Protocol string `yaml:"protocol" json:"protocol"`
	Browser  bool   `yaml:"browser" json:"browser"`
}

type CommandConfig struct {
	Mode string          `yaml:"mode" json:"mode"`
	Host *HostCommand    `yaml:"host" json:"host,omitempty"`
	Run  *ComposeCommand `yaml:"run" json:"run,omitempty"`
	Exec *ComposeCommand `yaml:"exec" json:"exec,omitempty"`
}

type HostCommand struct {
	Cwd  string            `yaml:"cwd" json:"cwd,omitempty"`
	Argv []string          `yaml:"argv" json:"argv"`
	Env  map[string]string `yaml:"env" json:"env,omitempty"`
}

type ComposeCommand struct {
	Service string   `yaml:"service" json:"service"`
	Argv    []string `yaml:"argv" json:"argv"`
}

type CheckConfig struct {
	HTTP    *HTTPCheck `yaml:"http" json:"http,omitempty"`
	Command string     `yaml:"command" json:"command,omitempty"`
}

type HTTPCheck struct {
	Endpoint string `yaml:"endpoint" json:"endpoint"`
	Path     string `yaml:"path" json:"path"`
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var fugatoVariable = regexp.MustCompile(`\$\{([^}]+)\}`)

func validateConfig(cfg Config) error {
	if err := validatePrepareConfig(cfg.Prepare); err != nil {
		return err
	}
	if cfg.Browser.Profile != "" && cfg.Browser.Service == "" {
		return errors.New("browser.profile requires browser.service")
	}
	for _, name := range []string{cfg.Browser.Service, cfg.Browser.Profile} {
		if name != "" && !serviceName.MatchString(name) {
			return fmt.Errorf("invalid browser service or profile %q", name)
		}
	}
	for name, command := range cfg.Commands {
		if !serviceName.MatchString(name) {
			return fmt.Errorf("invalid command name %q", name)
		}
		switch command.Mode {
		case "service", "task", "interactive":
		default:
			return fmt.Errorf("command %q requires mode: service, task, or interactive", name)
		}
		count := 0
		if command.Host != nil {
			count++
		}
		if command.Run != nil {
			count++
		}
		if command.Exec != nil {
			count++
		}
		if count != 1 {
			return fmt.Errorf("command %q requires exactly one of host, run, or exec", name)
		}
		if command.Mode == "service" && command.Host == nil {
			return fmt.Errorf("service command %q requires host", name)
		}
		if command.Host != nil {
			if err := validateArgv(command.Host.Argv); err != nil {
				return fmt.Errorf("command %q: %w", name, err)
			}
			if filepath.IsAbs(command.Host.Cwd) {
				return fmt.Errorf("command %q host.cwd must be relative", name)
			}
			for _, value := range command.Host.Argv {
				if err := validateHostVariables(cfg, name, value); err != nil {
					return err
				}
			}
			for key, value := range command.Host.Env {
				if !environmentName.MatchString(key) {
					return fmt.Errorf("command %q has invalid environment name %q", name, key)
				}
				if strings.HasPrefix(key, "FUGATO_") {
					return fmt.Errorf("command %q cannot override reserved environment name %q", name, key)
				}
				if strings.ContainsRune(value, 0) {
					return fmt.Errorf("command %q environment value contains NUL", name)
				}
				if err := validateHostVariables(cfg, name, value); err != nil {
					return err
				}
			}
		}
		for _, executor := range []*ComposeCommand{command.Run, command.Exec} {
			if executor == nil {
				continue
			}
			if !serviceName.MatchString(executor.Service) {
				return fmt.Errorf("command %q requires a valid Compose service", name)
			}
			if err := validateArgv(executor.Argv); err != nil {
				return fmt.Errorf("command %q: %w", name, err)
			}
		}
	}
	for name, endpoint := range cfg.Endpoints {
		if endpoint.Browser && endpoint.Protocol == "http" && !previewEndpointName.MatchString(name) {
			return fmt.Errorf("browser HTTP endpoint %q requires a lowercase DNS label of 1–63 characters", name)
		}
		if !serviceName.MatchString(name) {
			return fmt.Errorf("invalid endpoint name %q", name)
		}
		if (endpoint.Service == "") == (endpoint.Command == "") {
			return fmt.Errorf("endpoint %q requires exactly one service or command", name)
		}
		switch endpoint.Protocol {
		case "http", "https", "tcp", "udp":
		default:
			return fmt.Errorf("endpoint %q requires protocol: http, https, tcp, or udp", name)
		}
		if endpoint.Browser && endpoint.Protocol != "http" && endpoint.Protocol != "https" {
			return fmt.Errorf("endpoint %q browser requires HTTP(S)", name)
		}
		if endpoint.Port < 0 || endpoint.Port > 65535 {
			return fmt.Errorf("endpoint %q has invalid port", name)
		}
		if endpoint.Service != "" {
			if !serviceName.MatchString(endpoint.Service) || endpoint.Port == 0 {
				return fmt.Errorf("endpoint %q requires a valid service and container port", name)
			}
		} else {
			command, ok := cfg.Commands[endpoint.Command]
			if !ok || command.Mode != "service" || command.Host == nil {
				return fmt.Errorf("endpoint %q must target a persistent host command", name)
			}
			if endpoint.Protocol == "udp" {
				return fmt.Errorf("native endpoint %q does not support UDP", name)
			}
		}
	}
	for name, check := range cfg.Checks {
		if !serviceName.MatchString(name) {
			return fmt.Errorf("invalid check name %q", name)
		}
		if (check.HTTP == nil) == (check.Command == "") {
			return fmt.Errorf("check %q requires exactly one http or command", name)
		}
		if check.HTTP != nil {
			endpoint, ok := cfg.Endpoints[check.HTTP.Endpoint]
			if !ok || (endpoint.Protocol != "http" && endpoint.Protocol != "https") {
				return fmt.Errorf("check %q requires an HTTP(S) endpoint", name)
			}
			if !validHealthPath(check.HTTP.Path) {
				return fmt.Errorf("check %q requires a valid HTTP path", name)
			}
		} else if command, ok := cfg.Commands[check.Command]; !ok || command.Mode != "task" {
			return fmt.Errorf("check %q must reference a task command", name)
		}
	}
	seen := map[string]bool{}
	for _, name := range cfg.Start.Services {
		if !serviceName.MatchString(name) || seen[name] {
			return fmt.Errorf("invalid or duplicate start service %q", name)
		}
		seen[name] = true
	}
	seen = map[string]bool{}
	for _, name := range cfg.Start.Commands {
		command, ok := cfg.Commands[name]
		if !ok || command.Mode == "interactive" || seen[name] {
			return fmt.Errorf("start command %q must be a unique service or task command", name)
		}
		seen[name] = true
	}
	return nil
}

func validateArgv(argv []string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return errors.New("argv must contain an executable")
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return errors.New("argv must not contain NUL")
		}
	}
	return nil
}

func validateHostVariables(cfg Config, commandName, value string) error {
	for _, match := range fugatoVariable.FindAllStringSubmatch(value, -1) {
		name := match[1]
		if name == "FUGATO_RUNTIME_ID" || name == "FUGATO_DATA_DIR" {
			continue
		}
		_, ok := cfg.Endpoints[strings.TrimPrefix(name, "FUGATO_PORT_")]
		if strings.HasPrefix(name, "FUGATO_PORT_") && ok {
			continue
		}
		return fmt.Errorf("command %q uses unsupported variable %q", commandName, name)
	}
	remaining := fugatoVariable.ReplaceAllString(value, "")
	if strings.Contains(remaining, "${") || strings.Contains(remaining, "$FUGATO_") {
		return fmt.Errorf("command %q has malformed variable syntax; use ${FUGATO_VARIABLE}", commandName)
	}
	return nil
}

func validateHostDirectories(cfg Config, dir, root string) error {
	for name, command := range cfg.Commands {
		if command.Host == nil {
			continue
		}
		cwd := filepath.Join(dir, command.Host.Cwd)
		resolved, err := filepath.EvalSymlinks(cwd)
		if err != nil || !within(root, resolved) {
			return fmt.Errorf("command %q host.cwd must exist within the worktree", name)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("command %q host.cwd must be a directory", name)
		}
	}
	return nil
}

// CompleteProject validates references and selects default Compose roots only when no config exists.
func CompleteProject(ctx context.Context, p Project) (Project, error) {
	services := append([]string{}, p.Config.Start.Services...)
	for _, endpoint := range p.Config.Endpoints {
		if endpoint.Service != "" {
			services = append(services, endpoint.Service)
		}
	}
	for _, command := range p.Config.Commands {
		for _, executor := range []*ComposeCommand{command.Run, command.Exec} {
			if executor != nil {
				services = append(services, executor.Service)
			}
		}
	}
	if p.Config.Browser.Service != "" {
		services = append(services, p.Config.Browser.Service)
	}
	if len(p.ComposeFiles) == 0 {
		if len(services) > 0 {
			return Project{}, errors.New("Compose service references require a Compose file")
		}
		return p, nil
	}
	model, err := readComposeModel(ctx, p)
	if err != nil {
		return Project{}, err
	}
	if p.ConfigFile == "" && p.Config.Start.Services == nil {
		p.Config.Start.Services = []string{}
		for name, service := range model.Services {
			if len(service.Profiles) == 0 {
				p.Config.Start.Services = append(p.Config.Start.Services, name)
			}
		}
		slices.Sort(p.Config.Start.Services)
	}
	for _, name := range services {
		if _, ok := model.Services[name]; !ok {
			return Project{}, fmt.Errorf("service %q is missing from Compose", name)
		}
	}
	if p.Config.Browser.Service != "" {
		browser := model.Services[p.Config.Browser.Service]
		if p.Config.Browser.Profile == "" && len(browser.Profiles) == 1 {
			p.Config.Browser.Profile = browser.Profiles[0]
		}
		if p.Config.Browser.Profile == "" && len(browser.Profiles) > 1 {
			return Project{}, fmt.Errorf("browser service %q has multiple profiles; set browser.profile", p.Config.Browser.Service)
		}
		if p.Config.Browser.Profile != "" && !slices.Contains(browser.Profiles, p.Config.Browser.Profile) {
			return Project{}, fmt.Errorf("browser profile %q is not declared by service %q", p.Config.Browser.Profile, p.Config.Browser.Service)
		}
	}

	return p, nil
}

// YAML decoding alone cannot distinguish an absent integer from an explicit zero.
// Keep presence checks separate so a native dynamic port must be deliberate.
func validateConfigFields(data []byte) error {
	// Decode presence separately from Config. yaml resolves aliases and merge keys
	// here, so explicit fields and merged fields receive identical validation.
	var fields map[string]any
	if err := yaml.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New(".fugato.yaml must contain a mapping")
	}
	for key, value := range fields {
		if err := rejectNullConfig(value, key); err != nil {
			return err
		}
		switch key {
		case "browser":
			browser, ok := value.(map[string]any)
			service, hasService := browser["service"].(string)
			if !ok || !hasService || service == "" {
				return errors.New("browser requires service")
			}
		case "endpoints", "checks":
			entries, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s must be a mapping", key)
			}
			for name, value := range entries {
				entry, ok := value.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.%s must be a mapping", key, name)
				}
				if key == "endpoints" {
					if _, ok := entry["port"]; !ok {
						return fmt.Errorf("endpoint %q requires an explicit port", name)
					}
					_, service := entry["service"]
					_, command := entry["command"]
					if service == command {
						return fmt.Errorf("endpoint %q requires exactly one service or command field", name)
					}
				} else {
					_, http := entry["http"]
					_, command := entry["command"]
					if http == command {
						return fmt.Errorf("check %q requires exactly one http or command field", name)
					}
				}
			}
		}
	}
	return nil
}

func rejectNullConfig(value any, path string) error {
	if value == nil {
		return fmt.Errorf("%s must not be null", path)
	}
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if err := rejectNullConfig(child, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := rejectNullConfig(child, path); err != nil {
				return err
			}
		}
	case map[any]any:
		return fmt.Errorf("%s requires string mapping keys", path)
	}
	return nil
}

var previewEndpointName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
