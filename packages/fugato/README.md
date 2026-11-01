# Fugato local runtime

This Go module contains the Fugato CLI, local daemon, SQLite registry, Compose adapter, native process runner, and preview proxy. Start with the [installation guide](../../docs/guides/install.md), then try [example 1](../../examples/example_1/README.md) for Compose or the [native example](../../examples/native-app/README.md) without Docker. Projects can combine Compose services and native commands.

To configure your own application with an AI agent, follow the
[agent quickstart](../../docs/guides/agent-quickstart.md).

For development, build from this directory with `go build -o ../../bin/fugato ./cmd/fugato`. Follow the chosen example's setup before running that binary's `up` command; example 1 needs its local `.env` file. The CLI starts its local daemon on demand. `ls`, `open`, `logs`, and `down` use the same daemon. Use `--worktree path` to target a different project directory. The editor extension requires the installed binary to be outside the opened workspace.

By default, the daemon binds its API and preview proxy to loopback ports 46655 and 46656. Set `FUGATO_STATE_DIR`, `FUGATO_API_PORT`, and `FUGATO_PROXY_PORT` to isolate a development instance. The API token, SQLite registry, and daemon log live in the state directory. `down` keeps Compose named volumes and native data directories; it removes containers, so data written only in a container's writable layer is lost.

The current Compose adapter remaps published TCP and UDP host ports for selected services to dynamic loopback ports while preserving container ports. It supports project-scoped networks and named volumes and bind mounts within the Git worktree. It rejects host networking, privileged services, external or explicitly shared volumes and networks, duplicate published container ports, and mounts outside the worktree. See the [install guide](../../docs/guides/install.md) for the first-run workflow.

The MCP stdio entrypoint is `go build -o fugato-mcp ./cmd/fugato-mcp`. Its local API and verification contract are documented in [the MCP reference](../../docs/reference/mcp.md).

The [Go CI workflow](../../.github/workflows/go.yml) uses the exact toolchain in `go.mod` and runs formatting, race tests, vet, bounded parser fuzzing, and `govulncheck`. Run these checks locally before changing the runtime contract.
