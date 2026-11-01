# Local runtime acceptance tests

Run commands below from the public repository root. Preparation needs Python
3.10+, Git, and a Fugato binary. Native tests also need host Node.js and npm.
Compose tests need Docker Engine and Compose; browser fixtures also need npm.
Tests that build their own binaries use the Go toolchain in
`packages/fugato/go.mod`. Runtime and MCP suites need permission to bind loopback ports.

For the declared-file preparation path without Docker, run:

```sh
python3 scripts/acceptance/prepare.py --binary /absolute/path/to/fugato
```

Add `--mcp-binary /absolute/path/to/fugato-mcp` to exercise the authenticated
MCP operation too. The harness creates disposable source and target Git
worktrees with synthetic placeholder data, then checks dry-run behavior,
recursive copying, exclusions and explicit default-filter exceptions,
private file permissions, preservation of user edits, and symlink and `.git`
protection. It does not use real credentials or require Docker. `--keep-fixture`
retains the synthetic worktree for inspection.

For destructive cleanup isolation, run:

```sh
./scripts/acceptance-destroy.sh /absolute/path/to/fugato
```

This creates its own daemon and two disposable Compose worktrees. It verifies
default data retention, reuse after registration, explicit `--volumes` deletion,
cleanup after a worktree disappears, registry removal, and preservation of the
other runtime, shared image, and unlabelled resources with matching name prefixes.
It never prunes Docker globally or selects resources from existing user stacks.

For the fully containerized Next.js → NestJS → PostgreSQL example, run
`python3 scripts/acceptance/next_nest_postgres.py` (or pass `--binary
/path/to/fugato`). This focused check creates two disposable worktrees and
checks ignored-input preparation, web/API/database requests, todo changes and
input validation, proxy routing, isolated data, repeated Next/Nest source reload,
and stop/restart with retained data and independent environment values.

For native processes without Docker, run
`python3 scripts/acceptance/native.py` (or pass `--binary /path/to/fugato`). It
uses the dependency-free native example in two disposable Git worktrees and
checks read-only discovery, four distinct ports, named previews/checks, task
exit status, interactive stdin, logs, and native child cleanup on daemon exit.
It retains command logs and a result list under its printed temporary directory.

Run `python3 scripts/acceptance/environments.py` for Compose and mixed
environments: API/worker startup with no preview, multiple browser endpoints,
container run/exec tasks, native/container endpoints together, and startup
rollback. It reuses the disposable Compose harness and its scoped cleanup.

Run this from a macOS or Linux developer environment with Python 3.10+, Git,
Docker Engine, Docker Compose, and `npm` available. By default it also needs the
Go toolchain in `packages/fugato/go.mod`:

```sh
python3 scripts/acceptance/compose.py
```

On a Linux guest without Go, copy the repository tree and both Linux binaries
from the same archive into the guest, then run `python3
scripts/acceptance/compose.py --binary /path/to/fugato --mcp-binary
/path/to/fugato-mcp`. If the binaries are siblings, `--mcp-binary` may be omitted.
The runner records their SHA-256 hashes and uses the example
from the copied tree. It does not need the original repository's `.git` metadata;
it creates committed, disposable fixture repositories for the checks.

The sample fixture records elapsed time for starting two fresh worktrees
concurrently and for restarting one stopped worktree with retained data. Its
environment record says whether the Node and PostgreSQL base images were
already present. These timings include build and health wait, and describe the
tested host and cache state; they do not measure a clean-machine image pull
separately.

The default run includes the configured Playwright browser fixture. It needs
the example's pinned npm packages and the image declared in its Compose file;
Docker may download or build images on a cold machine. Use `--skip-browser` for
a faster runtime and Compose check, but that does not satisfy the full matrix.
Use `--artifacts /path/to/empty-directory` to choose where JSON results and logs
are saved; otherwise the script prints a new temporary directory. Review
`summary.json` for machine-readable status. The script exits nonzero on a failed
assertion or cleanup error.
Use `--only ports` (or `sample`, `fixed`, `web`, `diagnostics`) while developing a
fixture group; only the default full run satisfies the acceptance matrix.

By default, the suite builds the current Fugato CLI and MCP server. It uses a dedicated daemon state
directory and available loopback ports, commits disposable fixture repositories,
creates Git worktrees, and cleans only its own Compose project names and image
tags. It checks that pre-existing running containers and volumes remain.
It releases each fixture group's containers and volumes before starting the next
group to reduce memory pressure in a small Linux developer VM.
After a check fails, the suite attempts to stop its daemon and remove its
containers, networks, volumes, and worktrees. Inspect `cleanup_errors` in
`summary.json` if removal fails; logs and JSON results remain for diagnosis.

| Fixture | Live assertions |
| --- | --- |
| Existing web/PostgreSQL example | Two worktrees, distinct previews and data, scoped volumes/networks, stop/restart, explicit/absent config identity, CSS and server watcher, logs, browser verification, Playwright task flow, daemon restart. |
| MCP stdio | Handshake and runtime listing, HTTP verification, denied/allowed command policy, browser clean/fail/fix evidence and screenshot, scoped stop/restart with retained data. |
| Fixed source ports | Two more concurrent worktrees, web and PostgreSQL host-port remapping, unique loopback endpoints, database reachability and preview health. |
| Web-only | Explicit endpoints over `expose`, preview routing, optional database and browser capability. |
| Monorepo | Compose project under a subdirectory, sibling source and build context in the same worktree. |
| Discovery | Multiple services, multiple ports, and portless services need no browser selection; `init` records explicit startup choices. |
| Unsupported settings | Host networking, external mutable volume, duplicate publication and outside-worktree mount report actionable errors before startup. |
| Port range and UDP | Effective remapping to dynamic loopback endpoints, protocol reporting, and no active bindings after stop. |

Run the full matrix on each target platform before release. The GitHub Actions
job below runs on Ubuntu AMD64 against the packaged executables. Each local run
records its own results and timings in the printed artifact directory.

The GitHub Actions matrix in `.github/workflows/acceptance.yml` builds the
Linux AMD64 archive, verifies its checksum, installs both executables into a
temporary `bin` directory, and tests those installed binaries in two independent
Ubuntu jobs: preparation CLI/MCP plus the full Compose/browser/MCP matrix, and
the Next/Nest/PostgreSQL fixture. It does not persist Docker or npm caches;
fixture groups release their own containers and volumes before the next group.
Each job has a 40-minute limit and retains only JSON and text logs for five days
when a run fails. It builds and tests an archive but never publishes it.
