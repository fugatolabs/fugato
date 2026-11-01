# Contributing

## Local development

Use the Go toolchain recorded in `packages/fugato/go.mod`. Docker Engine and Compose are needed for Compose workloads and their acceptance tests; the native example uses host Node.js and npm. Build the CLI with `go build -o ../../bin/fugato ./cmd/fugato` from `packages/fugato`. Follow an [example's setup](examples/README.md), then run the binary from that project directory or pass `--worktree` with its path. Install the binary outside the opened workspace when testing the editor extension.

Building the optional Cursor VSIX or using the source-based Cursor installer
requires Node.js 22+ and npm. From `packages/cursor-extension`, run `npm ci`,
`npm audit --audit-level=high`, `npm run check`, `npm test`, and `npm run package`.
The Node.js build requirement does not change the prebuilt VSIX's editor
compatibility or require a separate Node installation for end users.

Before submitting a Go change, run from `packages/fugato`:

```sh
gofmt -w ./cmd ./internal
go test ./...
go test -race ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
```

Tests that bind loopback ports need a host where local listeners are permitted. The Docker acceptance test should create two Git worktrees from the same committed example, start both, verify independent data and routing, stop and restart one, and confirm the other stays healthy. Keep generated binaries, `node_modules`, and test artifacts out of commits.

Run the repeatable Docker check with `scripts/acceptance-two-worktrees.sh /absolute/path/to/fugato` from the repository root. It creates a disposable Git repository and two worktrees, publishes fixed web and database source ports in both, verifies Fugato's four loopback mappings and separate PostgreSQL data, and attempts to remove its test containers and volumes on exit. For browser/MCP fixtures and platform validation, follow the [acceptance instructions](scripts/acceptance/README.md) and retain the results from the revision you tested.

Follow Go's standard package layout and keep interfaces small and close to their callers. Use contexts and bounded I/O for Docker commands. Treat effective Compose configuration, local API authentication, preview Host routing, and resource ownership as security boundaries. Never invoke a shell with project-controlled strings or mount the host Docker socket into the sample workload.

Before changing daemon behavior, read [the lifecycle and resource decisions](docs/architecture/daemon-lifecycle.md). Preserve those invariants and update the record when a contract changes. Add deterministic tests for state interleavings, cancellation, overload, and streaming errors; the race detector alone cannot detect stale persisted state.

Please avoid sending secrets or private repository content in issues and pull requests.

The portable agent skill bundles the canonical compatibility, preparation,
configuration, MCP, and cleanup references. After editing those guides, run
`python3 scripts/sync-skill-references.py` from the repository root and include
the updated `skills/fugato-workflow/references/` files. CI checks that these
installed copies match the maintained documentation. Install the entire skill
directory when testing discovery in an agent.

## License

Unless explicitly stated otherwise, contributions submitted for inclusion in
Fugato are licensed under the [Apache License, Version 2.0](LICENSE).
Only contribute material you have the right to submit under these terms, and
preserve applicable third-party license and attribution notices.
