package fugato

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestStartupPlanCountsComposeDependenciesAsStarted(t *testing.T) {
	model := composeModel{Services: map[string]composeService{
		"web":    {DependsOn: map[string]json.RawMessage{"db": nil}},
		"db":     {},
		"worker": {},
	}}
	plan, err := completeStartupPlan(StartupPlan{SelectedServices: []string{"web"}}, model)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.DependencyServices, []string{"db"}) || !slices.Equal(plan.UnselectedServices, []string{"worker"}) {
		t.Fatalf("incorrect effective Compose selection: %+v", plan)
	}
}

func TestDoctorShowsExplicitEmptyStartupWithoutStartingWork(t *testing.T) {
	dir := t.TempDir()
	if _, err := gitOutput(context.Background(), dir, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	config := "version: 1\ncommands:\n  test:\n    mode: task\n    host:\n      argv: [echo, ok]\n"
	if err := os.WriteFile(filepath.Join(dir, ".fugato.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	report := Doctor(context.Background(), dir)
	if !report.OK() || !report.Plan.Empty() || !slices.Equal(report.Plan.OtherCommands, []string{"test"}) {
		t.Fatalf("unexpected task-only report: %+v", report)
	}
	found := false
	for _, finding := range report.Findings {
		found = found || finding.Code == "START_EMPTY" && finding.Severity == "info"
	}
	if !found {
		t.Fatalf("missing informational empty-start finding: %+v", report.Findings)
	}
}

func TestProjectStartupPlanPreservesSelectedCommandOrder(t *testing.T) {
	p := Project{Config: Config{Start: StartConfig{Commands: []string{"prepare", "web"}}, Commands: map[string]CommandConfig{
		"web": {Mode: "service"}, "prepare": {Mode: "task"}, "test": {Mode: "task"},
	}}}
	plan, err := ProjectStartupPlan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.SelectedCommands, []string{"prepare", "web"}) || !slices.Equal(plan.OtherCommands, []string{"test"}) {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}
