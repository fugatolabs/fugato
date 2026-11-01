package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintHelp(t *testing.T) {
	for _, tt := range []struct {
		name    string
		command string
		want    string
	}{
		{name: "root", want: "fugato help [command]"},
		{name: "doctor", command: "doctor", want: "--json"},
		{name: "prepare", command: "prepare", want: "--from SOURCE_PROJECT_DIR"},
		{name: "create", command: "create", want: "register a stopped"},
		{name: "init", command: "init", want: "--service"},
		{name: "daemon", command: "daemon", want: "foreground"},
		{name: "destroy", command: "destroy", want: "--volumes"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := printHelp(&output, tt.command); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), tt.want) {
				t.Fatalf("help output %q does not contain %q", output.String(), tt.want)
			}
		})
	}
}

func TestPrintHelpRejectsUnknownCommand(t *testing.T) {
	var output bytes.Buffer
	if err := printHelp(&output, "missing"); err == nil {
		t.Fatal("expected unknown command error")
	}
	if output.Len() != 0 {
		t.Fatalf("unexpected help output: %q", output.String())
	}
}
