# Runtime acceptance tests

Run the same tests locally and in CI through `make acceptance`. Install the test
runner into a virtual environment with `python3 -m venv .venv` and
`.venv/bin/python -m pip install -r requirements-test.txt`, then pass
`PYTHON=.venv/bin/python` to Make. The runner needs Python 3.10+, Git, and the
Go toolchain in `packages/fugato/go.mod` when building its own executables.
Native cases also need Node.js and npm. Container cases need Docker Engine and
Compose; the browser case also installs the example's locked npm dependencies.
All runtime/MCP cases require permission to bind loopback ports.

```sh
make acceptance PYTHON=.venv/bin/python SUITE='native or prepare'
make acceptance PYTHON=.venv/bin/python SUITE='compose or environments or destroy'
make acceptance PYTHON=.venv/bin/python SUITE=fullstack
```

The equivalent direct runner command is:

```sh
.venv/bin/python -m pytest tests/acceptance -m 'native or prepare'
```

To verify an installed release rather than a source build, pass both executables:

```sh
.venv/bin/python -m pytest tests/acceptance \
  --binary /path/to/fugato --mcp-binary /path/to/fugato-mcp \
  --artifacts /path/to/evidence
```

If `--mcp-binary` is omitted, MCP cases use the CLI's sibling `fugato-mcp`.
Without `--binary`, a session fixture builds both executables once. Tests copy
those executables into their isolated artifact directories and record SHA-256
hashes. They also work with a source tree copied without its original `.git`
metadata: fixture repositories and worktrees are committed independently.

| Selection (`-m`) | Live coverage |
| --- | --- |
| `native` | Read-only discovery without daemon startup; two native worktrees, four distinct ports, named previews/checks, task exit status, interactive stdin, logs, isolated retained data, scoped stop/restart, native child cleanup. No Docker. |
| `prepare` | Synthetic ignored inputs, blocked preflight, dry run, recursive copying, default/project exclusions and explicit exceptions, private permissions, preservation of edits, protected Git metadata, symlink rejection, authenticated MCP preparation and unauthenticated-write rejection. No Docker. |
| `compose` | Two-worktree previews, database/volume/network isolation, retained-data restart, explicit/absent configuration identity, CSS/server reload, logs, browser clean/fail/fix checks, Playwright todo flow, MCP policy and lifecycle, daemon reconciliation, fixed ports, monorepo paths, startup discovery, unsupported Compose settings, ranges and UDP. |
| `environments` | API/worker startup without browser selection, multiple endpoints, container run/exec tasks, native-to-container requests and failed-startup rollback. |
| `destroy` | Default retained-data reuse, explicit volume deletion, deleted-worktree cleanup by runtime ID, registry removal, preservation of another runtime, shared images and unlabelled resources with matching prefixes. |
| `fullstack` | Next.js → NestJS → PostgreSQL in two worktrees, independent ignored dotenv preparation, CRUD and input validation, proxy routing, isolated resources, repeated source reload and scoped stop/restart. |

`compose`, `environments`, `destroy` and `fullstack` also carry the `docker`
marker. `prepare` and the Compose sample case carry `mcp`. Use `-k fixed_ports`
(or another test name) to select a single case while developing. Omitting `-m`
runs every scenario. `--skip-browser` omits the Playwright work from the Compose
sample; a run with that flag does not satisfy full release acceptance.

The separate two-worktree shell check was retired after its fixed web/database
port, per-service LAN warning, data-isolation, stopped-port and restart-retention
assertions were incorporated into `test_compose.py`. Destruction is now a Python
test using the same lifecycle and cleanup fixture as the other container cases.

`harness.py` owns executable staging, dedicated daemon state, dynamic API/proxy
ports, command/MCP logs, disposable Git worktrees and resource cleanup.
`fixtures.py` contains synthetic project definitions. Each test starts with an
independent fixture, so pytest selections do not depend on test order. Teardown
runs after both success and failure. Container cleanup selects the fixture's
exact Compose ownership labels and image tags, including when its worktree has
been deleted. It never prunes Docker globally. The destroy case removes only
the exact two unlabelled resources that it explicitly created.

The artifact parent gets a unique subdirectory for each test, containing
`environment.json`, `results.json`, `summary.json`, `commands.log` and, when a
daemon started, `daemon.log`. These directories remain after the run; the runner
prints their locations. `summary.json` reports assertion failures, skipped browser
checks and cleanup errors. Cleanup errors fail the test. Review logs before
sharing them. CI should retain only bounded JSON/text evidence on failures.

The Compose sample records elapsed time for starting two fresh worktrees and
restarting retained data, together with the host/toolchain and whether base
images were already present. These timings include build and health waits;
they are not independent clean-machine image-pull benchmarks.

CI runs these selectors against packaged executables and gates release creation
on their results. A passing Ubuntu run describes that runner; verify other
target platforms before claiming their runtime acceptance coverage.
