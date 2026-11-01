package main

import (
	"flag"
	"fmt"
	"slices"

	"github.com/fugatolabs/fugato/packages/fugato/internal/fugato"
)

// doctorJSONReport is the CLI's versioned machine-readable contract. Keep this
// separate from Project so new internal fields cannot accidentally leak into
// command output, including Compose contents or environment values.
type doctorJSONReport struct {
	SchemaVersion int                 `json:"schema_version"`
	OK            bool                `json:"ok"`
	Project       *doctorJSONProject  `json:"project,omitempty"`
	Sources       map[string]string   `json:"sources,omitempty"`
	Startup       *fugato.StartupPlan `json:"startup,omitempty"`
	Findings      []fugato.Finding    `json:"findings"`
}

type doctorJSONProject struct {
	Version      int                              `json:"version,omitempty"`
	Endpoints    map[string]fugato.EndpointConfig `json:"endpoints,omitempty"`
	Commands     []fugato.DiscoveredCommand       `json:"commands,omitempty"`
	Checks       []string                         `json:"checks,omitempty"`
	RuntimeID    string                           `json:"runtime_id"`
	Directory    string                           `json:"directory"`
	ComposeFiles []string                         `json:"compose_files"`
	ConfigFile   string                           `json:"config_file,omitempty"`
	Browser      *doctorJSONBrowser               `json:"browser,omitempty"`
}

type doctorJSONBrowser struct {
	Service string `json:"service"`
	Profile string `json:"profile,omitempty"`
}

func doctorArgs(args []string) (string, bool, error) {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	path := flags.String("worktree", ".", "path inside a Git worktree project")
	jsonOutput := flags.Bool("json", false, "write a machine-readable doctor report")
	if err := flags.Parse(args); err != nil {
		return "", false, err
	}
	if len(flags.Args()) != 0 {
		return "", false, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	return *path, *jsonOutput, nil
}

func newDoctorJSONReport(report fugato.DoctorReport) doctorJSONReport {
	result := doctorJSONReport{
		SchemaVersion: 1,
		OK:            report.OK(),
		Sources:       report.Sources,
		Findings:      append([]fugato.Finding{}, report.Findings...),
	}
	if report.Project.ID == "" {
		return result
	}
	p := report.Project
	result.Project = &doctorJSONProject{
		Version:      p.Config.Version,
		RuntimeID:    p.ID,
		Directory:    p.Dir,
		ComposeFiles: append([]string{}, p.ComposeFiles...),
		ConfigFile:   p.ConfigFile,
	}
	if report.PlanAvailable {
		result.Startup = &report.Plan
	}
	result.Project.Endpoints = p.Config.Endpoints
	for name, command := range p.Config.Commands {
		result.Project.Commands = append(result.Project.Commands, fugato.DiscoveredCommand{Name: name, Mode: command.Mode})
	}
	slices.SortFunc(result.Project.Commands, func(a, b fugato.DiscoveredCommand) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	for name := range p.Config.Checks {
		result.Project.Checks = append(result.Project.Checks, name)
	}
	slices.Sort(result.Project.Checks)
	if p.Config.Browser.Service != "" {
		result.Project.Browser = &doctorJSONBrowser{
			Service: p.Config.Browser.Service,
			Profile: p.Config.Browser.Profile,
		}
	}
	return result
}
