package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fugatolabs/fugato/packages/fugato/internal/fugato"
)

type commandExitError struct{ code int }

func (e commandExitError) Error() string { return fmt.Sprintf("command exited with status %d", e.code) }

func environmentReadOnly(ctx context.Context, command string, args []string, output io.Writer) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("worktree", ".", "worktree directory")
	asJSON := flags.Bool("json", false, "write JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	var result any
	if command == "projects" {
		value, err := fugato.DiscoverProjects(ctx, *path)
		if err != nil {
			return err
		}
		result = value
	} else {
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		value, err := fugato.DiscoverWorktrees(ctx, absolute)
		if err != nil {
			return err
		}
		result = value
	}
	if *asJSON {
		return json.NewEncoder(output).Encode(result)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, string(data))
	return err
}

func environmentCommand(ctx context.Context, command string, args []string, output io.Writer) error {
	// Accept both conventional NAME --flags and --flags NAME forms.
	var name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("worktree", ".", "project directory")
	asJSON := flags.Bool("json", false, "write JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if name == "" && len(flags.Args()) == 1 {
		name = flags.Args()[0]
	} else if len(flags.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	if name == "" {
		return fmt.Errorf("usage: fugato %s NAME [--worktree PATH] [--json]", command)
	}
	p, err := fugato.ResolveProject(ctx, *path)
	if err != nil {
		return err
	}
	settings, err := fugato.DefaultSettings()
	if err != nil {
		return err
	}
	if command == "run" {
		definition, ok := p.Config.Commands[name]
		if !ok {
			return fmt.Errorf("unknown command %q", name)
		}
		if definition.Mode == "interactive" {
			if *asJSON {
				return errors.New("interactive commands do not support --json")
			}
			var bindings []fugato.PortBinding
			needsPorts := false
			if definition.Host != nil {
				for _, arg := range definition.Host.Argv {
					needsPorts = needsPorts || strings.Contains(arg, "${FUGATO_PORT_")
				}
				for _, value := range definition.Host.Env {
					needsPorts = needsPorts || strings.Contains(value, "${FUGATO_PORT_")
				}
			}
			if needsPorts {
				client := localClient{settings: settings}
				if err := client.request(ctx, http.MethodGet, "/v1/runtimes/"+p.ID+"/ports", nil, &bindings); err != nil {
					return fmt.Errorf("interactive command needs running endpoint bindings: %w", err)
				}
			}
			result, err := fugato.RunInteractive(ctx, p, name, settings.StateDir, bindings, os.Stdin, output, os.Stderr)
			if err != nil {
				return err
			}
			if result.ExitCode != 0 {
				return commandExitError{result.ExitCode}
			}
			return nil
		}
	}
	client := localClient{settings: settings}
	if err := client.ensureDaemon(ctx); err != nil {
		return err
	}
	body := map[string]string{"path": p.Dir, "name": name}
	if command == "run" {
		var result fugato.NamedCommandResult
		if err := client.request(ctx, http.MethodPost, "/v1/projects/run", body, &result); err != nil {
			return err
		}
		if *asJSON {
			if err := json.NewEncoder(output).Encode(result); err != nil {
				return err
			}
		} else {
			fmt.Fprint(output, result.Stdout)
			fmt.Fprint(os.Stderr, result.Stderr)
		}
		if result.ExitCode != 0 {
			return commandExitError{result.ExitCode}
		}
		return nil
	}
	var result fugato.NamedCheckResult
	if err := client.request(ctx, http.MethodPost, "/v1/projects/check", body, &result); err != nil {
		return err
	}
	if *asJSON {
		if err := json.NewEncoder(output).Encode(result); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(output, "%s: %t\n", result.Name, result.OK)
		fmt.Fprint(output, result.Stdout)
		fmt.Fprint(os.Stderr, result.Stderr)
	}
	if !result.OK {
		return errors.New("check failed")
	}
	return nil
}

func openEnvironment(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("open", flag.ContinueOnError)
	path := flags.String("worktree", ".", "project directory")
	name := flags.String("endpoint", "", "named browser endpoint")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	p, err := fugato.ResolveProject(ctx, *path)
	if err != nil {
		return err
	}
	settings, err := fugato.DefaultSettings()
	if err != nil {
		return err
	}
	client := localClient{settings: settings}
	// Opening an endpoint never starts its runtime or daemon.
	var target string
	var bindings []fugato.PortBinding
	if err := client.request(ctx, http.MethodGet, "/v1/runtimes/"+p.ID+"/ports", nil, &bindings); err != nil {
		return err
	}
	var choices []fugato.PortBinding
	for _, binding := range bindings {
		if binding.Browser && binding.URL != "" && (*name == "" || binding.Name == *name) {
			choices = append(choices, binding)
		}
	}
	if len(choices) != 1 {
		return errors.New("choose exactly one available browser endpoint with --endpoint NAME")
	}
	target = choices[0].URL
	if target == "" {
		return errors.New("runtime has no available browser endpoint")
	}
	fmt.Fprintln(output, target)
	opener := "open"
	if runtime.GOOS == "linux" {
		opener = "xdg-open"
	}
	return exec.CommandContext(ctx, opener, target).Run()
}
