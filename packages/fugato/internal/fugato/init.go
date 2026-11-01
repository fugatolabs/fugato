package fugato

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type InitChoices struct {
	Compose        string
	Services       []string
	BrowserService string
	BrowserProfile string
}

// Init writes the environment contract without running project workloads.
func Init(ctx context.Context, path string, choices InitChoices) (Project, bool, error) {
	p, err := discoverProject(ctx, path, choices.Compose, true)
	if err != nil {
		return Project{}, false, err
	}
	if p.ConfigFile != "" {
		return Project{}, false, fmt.Errorf("%s already exists; edit it to change project choices", p.ConfigFile)
	}
	if choices.BrowserProfile != "" && choices.BrowserService == "" {
		return Project{}, false, errors.New("--browser-profile requires --browser-service")
	}
	p.Config.Start.Services = choices.Services
	p.Config.Browser.Service = choices.BrowserService
	p.Config.Browser.Profile = choices.BrowserProfile
	if err := validateConfig(p.Config); err != nil {
		return Project{}, false, err
	}
	p, err = CompleteProject(ctx, p)
	if err != nil {
		return Project{}, false, err
	}
	if len(p.ComposeFiles) > 0 {
		if _, err := (ComposeBackend{}).Prepare(ctx, p); err != nil {
			return Project{}, false, err
		}
	}
	data, err := yaml.Marshal(p.Config)
	if err != nil {
		return Project{}, false, fmt.Errorf("encode project configuration: %w", err)
	}
	if len(p.ComposeFiles) == 0 {
		data = append(data, []byte(nativeInitGuide)...)
	}
	if _, err := parseProjectConfig(data); err != nil {
		return Project{}, false, err
	}
	configPath := filepath.Join(p.Dir, ".fugato.yaml")
	file, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return Project{}, false, err
	}
	if _, err := file.Write(data); err != nil {
		closeErr := file.Close()
		removeErr := os.Remove(configPath)
		return Project{}, false, errors.Join(err, closeErr, removeErr)
	}
	if err := file.Close(); err != nil {
		return Project{}, false, err
	}
	p.ConfigFile = configPath
	return p, true, nil
}

// The commented template leaves startup empty until the project's real commands
// are known. `fugato example native` provides a complete editable example.
const nativeInitGuide = `
# No work is selected yet. Replace placeholders with existing project commands,
# then uncomment the sections you need. See: fugato example native
# start:
#   commands: [web]       # Only selected commands start with fugato up.
# endpoints:
#   website:
#     command: web
#     port: 0              # Passed as FUGATO_PORT_website.
#     protocol: http
#     browser: true
# commands:
#   web:
#     mode: service
#     host:
#       cwd: .             # Relative to this file; use an existing directory.
#       argv: [npm, run, dev] # Replace with an existing command.
#       env:
#         PORT: '${FUGATO_PORT_website}'
#   test:
#     mode: task
#     host:
#       cwd: .
#       argv: [npm, test]  # Replace with an existing command.
# checks:
#   tests:
#     command: test
`
