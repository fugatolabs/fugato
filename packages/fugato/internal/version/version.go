// Package version identifies Fugato builds across the CLI and MCP server.
package version

// Version is the release version; release packaging checks it against the tag.
const Version = "0.0.1"

// Commit is injected by release packaging with the source Git commit.
var Commit = "unknown"

// String returns the release version with source revision metadata when available.
func String() string {
	if Commit == "unknown" || Commit == "" {
		return Version
	}
	return Version + "+" + Commit
}
