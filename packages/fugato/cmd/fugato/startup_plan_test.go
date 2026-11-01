package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fugatolabs/fugato/packages/fugato/internal/fugato"
)

func TestPrintStartupPlanExplainsEmptyAndUnselectedWork(t *testing.T) {
	var output bytes.Buffer
	printStartupPlan(&output, fugato.StartupPlan{UnselectedServices: []string{"web"}, OtherCommands: []string{"test"}})
	for _, want := range []string{"no work selected", "Available Compose services not selected: web", "Named commands not selected for up: test"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in %q", want, output.String())
		}
	}
}
