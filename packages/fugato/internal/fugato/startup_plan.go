package fugato

import (
	"context"
	"fmt"
	"slices"
)

// StartupPlan describes configuration choices, not live process state or readiness.
type StartupPlan struct {
	SelectedServices   []string `json:"selected_services"`
	DependencyServices []string `json:"dependency_services"`
	SelectedCommands   []string `json:"selected_commands"`
	UnselectedServices []string `json:"unselected_services"`
	OtherCommands      []string `json:"other_commands"`
}

func (plan StartupPlan) Empty() bool {
	return len(plan.SelectedServices) == 0 && len(plan.SelectedCommands) == 0
}

// ProjectStartupPlan reads the effective Compose model without starting work.
func ProjectStartupPlan(ctx context.Context, p Project) (StartupPlan, error) {
	plan := StartupPlan{
		SelectedServices:   append([]string{}, p.Config.Start.Services...),
		DependencyServices: []string{},
		SelectedCommands:   append([]string{}, p.Config.Start.Commands...),
		UnselectedServices: []string{},
		OtherCommands:      []string{},
	}
	for name := range p.Config.Commands {
		if !slices.Contains(plan.SelectedCommands, name) {
			plan.OtherCommands = append(plan.OtherCommands, name)
		}
	}
	slices.Sort(plan.OtherCommands)
	if len(p.ComposeFiles) == 0 {
		return plan, nil
	}
	model, err := readComposeModel(ctx, p)
	if err != nil {
		return plan, err
	}
	return completeStartupPlan(plan, model)
}

func completeStartupPlan(plan StartupPlan, model composeModel) (StartupPlan, error) {
	started := map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if started[name] {
			return nil
		}
		service, ok := model.Services[name]
		if !ok {
			return fmt.Errorf("startup service or dependency %q is missing from Compose", name)
		}
		started[name] = true
		for dependency := range service.DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range plan.SelectedServices {
		if err := visit(name); err != nil {
			return plan, err
		}
	}
	for name := range model.Services {
		if started[name] && !slices.Contains(plan.SelectedServices, name) {
			plan.DependencyServices = append(plan.DependencyServices, name)
		} else if !started[name] {
			plan.UnselectedServices = append(plan.UnselectedServices, name)
		}
	}
	slices.Sort(plan.DependencyServices)
	slices.Sort(plan.UnselectedServices)
	return plan, nil
}
