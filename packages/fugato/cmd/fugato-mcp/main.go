package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
	"github.com/fugatolabs/fugato/packages/fugato/internal/mcpserver"
	"github.com/fugatolabs/fugato/packages/fugato/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch {
		case len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "-h"):
			fmt.Fprintln(os.Stdout, "usage: fugato-mcp [--check | --version]\n--check verifies a read-only connection to the daemon and exits")
			return
		case len(os.Args) == 2 && os.Args[1] == "--version":
			fmt.Fprintf(os.Stdout, "fugato-mcp %s (commit %s)\n", version.Version, version.Commit)
			return
		case len(os.Args) == 2 && os.Args[1] == "--check":
		default:
			fail("usage: fugato-mcp [--check | --version]")
		}
	}
	apiURL := os.Getenv("FUGATO_API_URL")
	tokenFile := os.Getenv("FUGATO_API_TOKEN_FILE")
	if apiURL == "" || tokenFile == "" {
		fail("set FUGATO_API_URL and FUGATO_API_TOKEN_FILE")
	}
	client, err := control.NewFileClient(apiURL, tokenFile)
	if err != nil {
		fail("configure daemon client: %v", err)
	}
	if len(os.Args) == 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := checkConnection(ctx, client, os.Stderr); err != nil {
			fail("MCP daemon connection: %v", err)
		}
		return
	}
	if err := (mcpserver.Server{Service: client}).Run(context.Background(), os.Stdin, os.Stdout); err != nil {
		fail("MCP server: %v", err)
	}
}

func checkConnection(ctx context.Context, client interface {
	List(context.Context) ([]control.Runtime, error)
}, status io.Writer) error {
	if _, err := client.List(ctx); err != nil {
		return err
	}
	_, err := fmt.Fprintln(status, "Fugato MCP daemon connection OK")
	return err
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
