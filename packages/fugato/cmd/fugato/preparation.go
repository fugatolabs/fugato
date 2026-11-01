package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/fugatolabs/fugato/packages/fugato/internal/fugato"
)

func prepareWorktree(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("prepare", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	target := flags.String("worktree", ".", "target project directory")
	source := flags.String("from", "", "source project directory")
	dryRun := flags.Bool("dry-run", false, "report without writing files")
	jsonOutput := flags.Bool("json", false, "write a structured report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if strings.TrimSpace(*source) == "" {
		return errors.New("prepare requires --from SOURCE_PROJECT_DIR")
	}
	report, prepareErr := fugato.PrepareFiles(ctx, *target, *source, *dryRun)
	if *jsonOutput {
		if err := json.NewEncoder(output).Encode(report); err != nil {
			return err
		}
	} else {
		for _, entry := range report.Entries {
			line := fmt.Sprintf("%s  %s", entry.Path, entry.Status)
			if entry.Reason != "" {
				line += "  " + entry.Reason
			}
			if _, err := fmt.Fprintln(output, line); err != nil {
				return err
			}
		}
	}
	if prepareErr != nil {
		return fmt.Errorf("preparation failed: %w", prepareErr)
	}
	return nil
}
