package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
	"github.com/fugatolabs/fugato/packages/fugato/internal/fugato"
)

func destroyArgs(ctx context.Context, args []string) (string, bool, error) {
	flags := flag.NewFlagSet("destroy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("worktree", ".", "path to a Git worktree project")
	id := flags.String("runtime", "", "registered runtime ID (also works after worktree deletion)")
	volumes := flags.Bool("volumes", false, "delete runtime-owned named volumes and native data")
	if err := flags.Parse(args); err != nil {
		return "", false, err
	}
	if len(flags.Args()) != 0 {
		return "", false, errors.New("usage: fugato destroy [--worktree PATH | --runtime ID] [--volumes]")
	}
	selectedPath, selectedID := false, false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "worktree" {
			selectedPath = true
		}
		if f.Name == "runtime" {
			selectedID = true
		}
	})
	if selectedPath && selectedID {
		return "", false, errors.New("choose --worktree or --runtime, not both")
	}
	if selectedID {
		if err := control.ValidateID(*id); err != nil {
			return "", false, err
		}
		return *id, *volumes, nil
	}
	project, err := fugato.DiscoverProject(ctx, *path, "")
	if err != nil {
		return "", false, err
	}
	return project.ID, *volumes, nil
}

func destroyRuntime(ctx context.Context, args []string, output io.Writer) error {
	id, volumes, err := destroyArgs(ctx, args)
	if err != nil {
		return err
	}
	settings, err := fugato.DefaultSettings()
	if err != nil {
		return err
	}
	client := localClient{settings: settings}
	if err := client.ensureDaemon(ctx); err != nil {
		return err
	}
	var result fugato.DestroyResult
	if err := client.request(ctx, http.MethodPost, "/v1/runtimes/"+id+"/destroy", map[string]bool{"volumes": volumes}, &result); err != nil {
		return err
	}
	policy := "data retained"
	if result.Volumes {
		policy = "runtime data deleted"
	}
	_, err = fmt.Fprintf(output, "%s  destroyed (%s)\n", result.ID, policy)
	return err
}
