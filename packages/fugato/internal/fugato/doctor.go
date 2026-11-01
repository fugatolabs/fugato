package fugato

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
}

type DoctorReport struct {
	Project       Project           `json:"-"`
	Plan          StartupPlan       `json:"plan"`
	PlanAvailable bool              `json:"-"`
	Sources       map[string]string `json:"sources,omitempty"`
	Findings      []Finding         `json:"findings"`
}

func (r DoctorReport) OK() bool {
	for _, finding := range r.Findings {
		if finding.Severity == "error" {
			return false
		}
	}
	return true
}

// Doctor validates setup without starting workloads or the daemon.
func Doctor(ctx context.Context, path string) DoctorReport {
	report := DoctorReport{Findings: []Finding{}}
	add := func(code, severity, subject, message string) {
		report.Findings = append(report.Findings, Finding{Code: code, Severity: severity, Subject: subject, Message: message})
	}
	p, err := DiscoverProject(ctx, path, "")
	if err != nil {
		add("PROJECT_DISCOVERY", "error", path, err.Error())
		return report
	}
	report.Project = p
	report.Sources = map[string]string{"start": "explicit", "endpoints": "explicit", "commands": "explicit", "checks": "explicit"}
	completed, err := CompleteProject(ctx, p)
	if err != nil {
		add("PROJECT_INVALID", "error", p.Dir, err.Error())
		return report
	}
	report.Project = completed
	p = completed
	plan, planErr := ProjectStartupPlan(ctx, p)
	if planErr != nil {
		add("START_PLAN_UNAVAILABLE", "error", "start", planErr.Error())
	} else {
		report.Plan, report.PlanAvailable = plan, true
	}
	if report.PlanAvailable && report.Plan.Empty() {
		add("START_EMPTY", "info", "start", "no services or commands are selected for up; task-only projects are valid; add persistent commands to start.commands before starting them")
	}
	if p.ConfigFile == "" {
		report.Sources["start"] = "inferred"
	}
	if len(p.ComposeFiles) > 0 {
		report.Sources["compose"] = "discovered"
		if p.Config.Compose != "" {
			report.Sources["compose"] = "explicit"
		}
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		output, dockerErr := exec.CommandContext(checkCtx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput()
		cancel()
		if dockerErr != nil {
			add("DOCKER_UNAVAILABLE", "error", "docker", fmt.Sprintf("Docker Engine is unavailable: %s", strings.TrimSpace(string(output))))
		}
		report.Findings = append(report.Findings, doctorCompose(ctx, p)...)
	}
	names := make([]string, 0, len(p.Config.Commands))
	for name := range p.Config.Commands {
		names = append(names, name)
	}
	slices.Sort(names)
	hasHost := false
	for _, name := range names {
		command := p.Config.Commands[name]
		if command.Run != nil {
			add("COMPOSE_RUN_DEPENDENCIES", "info", name, "Compose run uses --no-deps; start required services explicitly before running this command")
		}
		if command.Exec != nil && command.Mode != "interactive" {
			add("COMPOSE_EXEC_TIMEOUT", "info", name, "noninteractive Compose exec requires timeout in the container and has a 300-second execution limit")
		}
		if command.Host == nil {
			continue
		}
		hasHost = true
		executable := command.Host.Argv[0]
		if strings.Contains(executable, "${") {
			add("HOST_EXECUTABLE_DYNAMIC", "warning", name, "executable contains runtime variables and cannot be checked before startup")
			continue
		}
		commandPath := os.Getenv("PATH")
		if configured, ok := command.Host.Env["PATH"]; ok {
			commandPath = configured
		}
		if _, err := hostExecutable(executable, filepath.Join(p.Dir, command.Host.Cwd), commandPath); err != nil {
			add("HOST_EXECUTABLE_UNAVAILABLE", "error", name, err.Error())
		}

	}
	if hasHost {
		add("HOST_ISOLATION", "warning", p.Dir, "host commands run with your permissions; applications must honor Fugato port/data variables to isolate worktrees, and prerequisites must already be installed")
	}
	for name, endpoint := range p.Config.Endpoints {
		if endpoint.Command != "" && endpoint.Port != 0 {
			add("HOST_FIXED_PORT", "warning", name, "fixed native ports may collide across worktrees; use port: 0 and the supplied FUGATO_PORT variable where supported")
		}
	}
	if report.OK() {
		add("PROJECT_READY", "info", p.Dir, "project passed static checks; commands and checks have not been executed")
	}
	return report
}

func doctorCompose(ctx context.Context, p Project) []Finding {
	findings := []Finding{}
	add := func(code, severity, subject, message string) {
		findings = append(findings, Finding{Code: code, Severity: severity, Subject: subject, Message: message})
	}
	if value := strings.TrimSpace(os.Getenv("COMPOSE_FILE")); value != "" {
		add("COMPOSE_FILE_IGNORED", "info", "COMPOSE_FILE", "Fugato uses the discovered Compose files and ignores the shell COMPOSE_FILE value")
	}
	model, err := readComposeModel(ctx, p)
	if err != nil {
		add("COMPOSE_INVALID", "error", p.ComposeFile, err.Error())
		return findings
	}
	selected, err := selectedServices(p, model)
	if err != nil {
		add("COMPOSE_INVALID", "error", p.ComposeFile, err.Error())
		return findings
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		service := model.Services[name]
		for key, value := range service.Environment {
			if text, ok := value.(string); ok && strings.Contains(strings.ToLower(text), "localhost") {
				add("APP_LOCALHOST_REFERENCE", "warning", name+".environment."+key, "check whether localhost names another container; use its Compose service name if so")
			}
		}
		if finding := missingResourceLimits(name, service); finding != nil {
			add(finding.Code, finding.Severity, finding.Subject, finding.Message)
		}
		for _, port := range service.Ports {
			for _, finding := range portBindingFindings(name, port) {
				add(finding.Code, finding.Severity, finding.Subject, finding.Message)
			}
		}
	}
	if _, err := (ComposeBackend{}).Prepare(ctx, p); err != nil {
		code := "COMPOSE_UNSUPPORTED"
		message := err.Error()
		switch {
		case strings.Contains(message, "network_mode"):
			code = "COMPOSE_HOST_NETWORK"
		case strings.Contains(message, "published port range"):
			code = "PORT_RANGE_UNSUPPORTED"
		case strings.Contains(message, "publishes") && strings.Contains(message, "more than once"):
			code = "PORT_DUPLICATE"
		case strings.Contains(message, "host or privilege"):
			code = "COMPOSE_HOST_OR_PRIVILEGE"
		case strings.Contains(message, "outside the project directory"):
			code = "COMPOSE_OUTSIDE_WORKTREE"
		case strings.HasPrefix(message, "volume ") && strings.Contains(message, "external or has a shared explicit name"):
			code = "COMPOSE_EXTERNAL_VOLUME"
		case strings.HasPrefix(message, "network ") && strings.Contains(message, "external or has a shared explicit name"):
			code = "COMPOSE_EXTERNAL_NETWORK"
		}
		add(code, "error", p.ComposeFile, message)
	}

	return findings
}

func missingResourceLimits(name string, service composeService) *Finding {
	missing := []string{}
	if service.MemLimit == "" && service.Deploy.Resources.Limits.Memory == "" {
		missing = append(missing, "memory")
	}
	if service.CPUs <= 0 && service.Deploy.Resources.Limits.CPUs <= 0 {
		missing = append(missing, "CPU")
	}
	if len(missing) == 0 {
		return nil
	}
	return &Finding{Code: "RESOURCE_LIMIT_UNSET", Severity: "info", Subject: name,
		Message: "Compose does not set " + strings.Join(missing, " and ") + " limit for this service; add a per-service limit in Compose if needed"}
}

func portBindingFindings(service string, port composePort) []Finding {
	protocol := port.Protocol
	if protocol == "" {
		protocol = "tcp"
	}
	subject := fmt.Sprintf("%s:%d/%s", service, port.Target, protocol)
	findings := []Finding{{Code: "PORT_REMAP", Severity: "info", Subject: subject,
		Message: "Fugato will assign a dynamic 127.0.0.1 host port; use fugato ports after startup"}}
	// Compose omits host_ip for a publication on all host interfaces.
	if port.HostIP != "127.0.0.1" {
		findings = append(findings, Finding{Code: "PORT_LAN_BINDING_CHANGED", Severity: "warning", Subject: subject,
			Message: "source host binding becomes loopback-only under Fugato"})
	}
	return findings
}
