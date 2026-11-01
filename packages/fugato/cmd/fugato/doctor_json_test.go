package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fugatolabs/fugato/packages/fugato/internal/fugato"
)

func TestDoctorArgs(t *testing.T) {
	for _, tt := range []struct {
		name, path string
		args       []string
		json       bool
		wantErr    bool
	}{
		{name: "defaults", path: "."},
		{name: "json and worktree", path: "/tmp/project", json: true, args: []string{"--json", "--worktree", "/tmp/project"}},
		{name: "unexpected positional", args: []string{"extra"}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, jsonOutput, err := doctorArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("doctorArgs error = %v, wantErr = %v", err, tt.wantErr)
			}
			if err == nil && (path != tt.path || jsonOutput != tt.json) {
				t.Fatalf("doctorArgs = (%q, %t), want (%q, %t)", path, jsonOutput, tt.path, tt.json)
			}
		})
	}
}

func TestDoctorJSONReportHasStableFieldsWithoutComposeContents(t *testing.T) {
	p := fugato.Project{ID: "1234567890abcdef", Dir: "/tmp/worktree", ComposeFiles: []string{"/tmp/worktree/compose.yaml"},
		OverrideYAML: "SENSITIVE_VALUE"}
	p.Config.Version = 1
	p.Config.Endpoints = map[string]fugato.EndpointConfig{"site": {Service: "web", Port: 3000, Protocol: "http", Browser: true}}
	report := fugato.DoctorReport{Project: p, Sources: map[string]string{"endpoints": "explicit"},
		Plan: fugato.StartupPlan{SelectedServices: []string{"web"}, UnselectedServices: []string{"worker"}}, PlanAvailable: true,
		Findings: []fugato.Finding{{Code: "PROJECT_READY", Severity: "info", Message: "ready"}}}
	data, err := json.Marshal(newDoctorJSONReport(report))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"schema_version":1`, `"ok":true`, `"runtime_id":"1234567890abcdef"`,
		`"port":3000`, `"endpoints":"explicit"`, `"startup":`, `"selected_services":["web"]`, `"unselected_services":["worker"]`, `"findings":[`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("JSON %s missing %s", data, want)
		}
	}
	if strings.Contains(string(data), "SENSITIVE_VALUE") {
		t.Fatal("doctor JSON leaked internal Compose content")
	}
}

func TestDoctorJSONReportDiscoveryFailure(t *testing.T) {
	report := fugato.DoctorReport{Findings: []fugato.Finding{{Code: "PROJECT_DISCOVERY", Severity: "error", Message: "missing"}}}
	data, err := json.Marshal(newDoctorJSONReport(report))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"project"`) || !strings.Contains(string(data), `"findings":[`) || !strings.Contains(string(data), `"ok":false`) {
		t.Fatalf("unexpected discovery JSON: %s", data)
	}
}

func TestDoctorJSONOmitsHostArgumentsAndEnvironment(t *testing.T) {
	p := fugato.Project{ID: "native", Dir: "/tmp/native", Config: fugato.Config{Version: 1,
		Commands:  map[string]fugato.CommandConfig{"test": {Mode: "task", Host: &fugato.HostCommand{Argv: []string{"echo", "SECRET_ARGUMENT"}, Env: map[string]string{"TOKEN": "SECRET_VALUE"}}}},
		Endpoints: map[string]fugato.EndpointConfig{"site": {Command: "web", Port: 0, Protocol: "http", Browser: true}},
		Checks:    map[string]fugato.CheckConfig{"test": {Command: "test"}},
	}}
	data, err := json.Marshal(newDoctorJSONReport(fugato.DoctorReport{Project: p, Findings: []fugato.Finding{}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"version":1`, `"compose_files":[]`, `"endpoints":`, `"commands":[{"name":"test","mode":"task"}]`, `"checks":["test"]`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s in %s", want, data)
		}
	}
	if strings.Contains(string(data), "SECRET_") {
		t.Fatalf("doctor leaked host command content: %s", data)
	}
}
