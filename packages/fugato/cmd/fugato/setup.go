package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/fugatolabs/fugato/packages/fugato/internal/agentsetup"
)

func setupAgent(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := agentsetup.Options{}
	flags.StringVar(&options.Agent, "agent", "", "coding agent")
	flags.StringVar(&options.Project, "project", ".", "existing project directory")
	flags.StringVar(&options.BinDir, "bin-dir", "", "directory containing Fugato binaries")
	flags.BoolVar(&options.DryRun, "dry-run", false, "report changes without writing files")
	jsonOutput := flags.Bool("json", false, "write one JSON report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	result, err := agentsetup.Setup(options)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(result)
	}
	verb := "Updated"
	if result.DryRun {
		verb = "Would update"
	}
	if _, err := fmt.Fprintf(output, "%s %d files for %s in %s\n", verb, len(result.Files), result.Agent, result.Project); err != nil {
		return err
	}
	for _, file := range result.Files {
		if _, err := fmt.Fprintln(output, "  "+file); err != nil {
			return err
		}
	}
	if result.DryRun {
		_, err := fmt.Fprintln(output, "Dry run: no files written.")
		return err
	}
	_, err = fmt.Fprintln(output, "Restart your agent in this project and review its MCP trust prompt. Verify the fugato-workflow skill and fugato MCP tools are visible.\nSetup does not start a daemon. Connection check (starts the local daemon): /bin/sh '"+escapeShell(result.Launcher)+"' --check\nMCP settings contain machine-specific paths. Re-run setup after moving the project; do not share them unchanged.")
	return err
}

func escapeShell(value string) string {
	// Only used inside single quotes in the displayed connection-check command.
	return strings.ReplaceAll(value, "'", "'\\''")
}
