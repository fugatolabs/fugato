package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/fugato"
)

func printRuntimes(ctx context.Context, output io.Writer, items []fugato.Runtime) error {
	// One budget for the whole listing, with one lookup per repository. Metadata
	// failures retain saved paths and must not hide a registered runtime.
	metadataCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	repositories := make(map[string]string)
	for _, item := range items {
		if _, found := repositories[item.Repository]; !found {
			repositories[item.Repository] = fugato.RepositoryPath(metadataCtx, item.Repository)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	items = slices.Clone(items)
	slices.SortFunc(items, func(a, b fugato.Runtime) int {
		return cmp.Or(
			cmp.Compare(repositories[a.Repository], repositories[b.Repository]),
			cmp.Compare(a.ProjectDir, b.ProjectDir),
			cmp.Compare(a.ID, b.ID),
		)
	})
	w := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tDESIRED\tOBSERVED\tBACKEND\tREPOSITORY\tWORKTREE\tPREVIEW")
	for _, item := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", item.ID, item.Desired, item.Observed, item.Backend, repositories[item.Repository], item.ProjectDir, item.PreviewURL)
	}
	return w.Flush()
}
