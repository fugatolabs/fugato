package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/fugatolabs/fugato/packages/fugato/internal/fugato"
)

func printStartupPlan(output io.Writer, plan fugato.StartupPlan) {
	if plan.Empty() {
		fmt.Fprintln(output, "Startup: no work selected; up starts no services or commands.")
	} else {
		fmt.Fprintf(output, "Startup services: %s\n", listOrNone(plan.SelectedServices))
		fmt.Fprintf(output, "Startup commands (in order): %s\n", listOrNone(plan.SelectedCommands))
	}
	if len(plan.DependencyServices) > 0 {
		fmt.Fprintf(output, "Compose dependencies of selected roots: %s\n", strings.Join(plan.DependencyServices, ", "))
	}
	if len(plan.UnselectedServices) > 0 {
		fmt.Fprintf(output, "Available Compose services not selected: %s\n", strings.Join(plan.UnselectedServices, ", "))
	}
	if len(plan.OtherCommands) > 0 {
		fmt.Fprintf(output, "Named commands not selected for up: %s\n", strings.Join(plan.OtherCommands, ", "))
	}
}

func listOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}
