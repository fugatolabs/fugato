package main

import (
	"fmt"
	"io"
)

const rootHelp = `Fugato runs Compose and native development environments in Git worktrees.

Usage:
  fugato <command> [flags]
  fugato help [command]

Commands:
  version Print release version and source commit (--version also works)
  doctor  Check project setup without starting workloads or a daemon
  prepare Copy declared local files into an existing worktree
  projects Discover configured projects and manifest candidates
  worktrees List repository worktrees
  run     Execute a configured task or interactive command
  check   Run a named HTTP or command check
  init    Scaffold environment configuration
  schema  Print the bundled version 1 JSON schema
  example Print a complete native, Compose, or mixed config example
  create  Validate and register an environment without starting workloads
  up      Start a worktree runtime
  ls      List runtimes
  open    Open the preview in a browser
  logs    Stream runtime logs
  ports   Show assigned host ports
  down    Stop a runtime and retain its data
  destroy Remove a runtime; --volumes also deletes its data
  daemon  Run the local daemon in the foreground

Run "fugato help <command>" for command flags. Help never starts the daemon.
`

var commandHelp = map[string]string{
	"create": `Usage: fugato create [--worktree PATH]

Validate the project and effective Compose configuration, then register a stopped
environment. Existing environments retain their current state. This does not
start workloads or copy preparation files; use prepare explicitly for file copies
and up when ready to start. --worktree defaults to the current directory.
`,
	"destroy": `Usage: fugato destroy [--worktree PATH | --runtime ID] [--volumes]

Remove one runtime's containers, networks, and registration. Named volumes and
native data are retained unless --volumes explicitly requests their deletion.
--worktree defaults to the current directory. Use --runtime with an ID from
fugato ls to clean up a deleted worktree or broken configuration. Cleanup verifies
saved identity and Docker ownership labels; it never deletes images, source
files, worktrees, arbitrary host data, or another runtime's resources.
`,
	"version": `Usage: fugato version | fugato --version

Print release version and source commit without starting a daemon.
`,
	"prepare": `Usage: fugato prepare [--worktree PATH] --from SOURCE_PROJECT_DIR [--dry-run] [--json]

Copy declared local preparation files into an existing worktree without starting
a daemon or workload. --worktree defaults to the current directory; --from is
required. --dry-run reports the plan without writing files. Existing files are
preserved. --json preserves the structured report on preparation failure.
`,
	"projects": `Usage: fugato projects [--worktree PATH] [--json]

Discover projects and candidates without executing scripts or starting a daemon.
Reports malformed configuration and bounded scan truncation.
`,
	"worktrees": `Usage: fugato worktrees [--worktree PATH] [--json]

List repository worktrees without starting a daemon.
`,
	"run": `Usage: fugato run NAME [--worktree PATH] [--json]

Run a named task or attach an interactive command to this terminal.
Task exit status is preserved. Tasks have a five-minute limit and bounded output.
Interactive commands do not support --json. Compose run uses --no-deps;
start its prerequisites explicitly with up. Noninteractive exec requires timeout
inside the container. Host commands have your permissions and must honor the
configured FUGATO_PORT_<name> and FUGATO_DATA_DIR variables for isolation.
`,
	"check": `Usage: fugato check NAME [--worktree PATH] [--json]

Run an explicitly configured HTTP or task check; a failed check exits nonzero.
HTTP checks do not follow redirects. Start endpoint services before checking.
`,
	"doctor": `Usage: fugato doctor [--worktree PATH] [--json]

Check the current or specified Git worktree without starting the daemon.
--json writes one JSON object with schema_version, ok, project, startup,
sources, and findings. Startup reports selected work and available unselected
services; it does not report live process state or run checks. A blocking finding
returns a nonzero exit status while preserving valid JSON on stdout.
`,
	"schema": `Usage: fugato schema

Print the version-matched .fugato.yaml JSON schema to stdout. No project,
daemon, or source checkout is required.
`,
	"example": `Usage: fugato example native|compose|mixed

Print a complete version 1 configuration example to stdout. Adapt command
names, paths, ports, and services to your project before using it. The command
does not write files or start a daemon.
`,
	"init": `Usage: fugato init [--worktree PATH] [--compose FILE] [--service NAME ...]
                   [--browser-service NAME] [--browser-profile NAME]

Write the sole supported configuration format: version: 1 in .fugato.yaml.
Repeat --service to select
Compose startup roots. Without selections, use active unprofiled Compose services.
Native-only repositories receive commented guidance with no startup work selected.
Use fugato example native for a complete editable example.
No browser endpoint is inferred. Existing configuration is never overwritten.
`,
	"up": `Usage: fugato up [--worktree PATH]

Start the selected environment and print its state and available endpoints.
The environment starts explicit Compose roots and ordered commands; previews are optional.
`,
	"ls": `Usage: fugato ls

List known runtimes.
`,
	"open": `Usage: fugato open [--worktree PATH] [--endpoint NAME]

Open a declared browser endpoint. Multiple previews require --endpoint NAME.
This command does not start an environment or daemon.
`,
	"logs": `Usage: fugato logs [--worktree PATH] [--follow]

Stream the selected runtime logs. --follow keeps streaming new lines.
`,
	"ports": `Usage: fugato ports [--worktree PATH]

Show the dynamic loopback host ports assigned to the selected runtime.
`,
	"down": `Usage: fugato down [--worktree PATH]

Stop the selected runtime and retain its data.
`,
	"daemon": `Usage: fugato daemon

Run the local daemon in the foreground. Runtime mutations start it on demand.
Discovery, doctor, help, and open do not start a daemon. Interactive host commands
run in the invoking terminal. Native services stop when their owning daemon exits.
`,
}

func knownCommand(command string) bool {
	_, ok := commandHelp[command]
	return ok
}

func printHelp(output io.Writer, command string) error {
	if command == "" {
		_, err := io.WriteString(output, rootHelp)
		return err
	}
	help, ok := commandHelp[command]
	if !ok {
		return fmt.Errorf("unknown command %q; run fugato --help", command)
	}
	_, err := io.WriteString(output, help)
	return err
}
