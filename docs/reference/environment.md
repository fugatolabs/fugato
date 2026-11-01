# Environment configuration

The configuration contract is `version: 1`. It describes Compose, native
commands, and mixed environments, with optional browser access. Other versions
are rejected. The CLI and daemon own interpretation and execution.
The extension is a configuration and runtime UI over the same contract.

## Supported options

- One or several projects per worktree, each with its own `.fugato.yaml`.
- Compose services, including APIs, workers, and infrastructure without a UI.
- Native persistent processes, finite tasks, and interactive terminal commands.
- Mixed Compose/native projects; command-only libraries and build/test projects.
- Zero, one, or several named endpoints; browser intent is explicit.
- Desktop/mobile development through existing host launch commands. Fugato does
  not supply a simulator, GUI automation, or sandbox for host applications.
- Finite jobs run on demand. Fugato does not schedule recurring jobs.

## Syntax

```yaml
version: 1
compose: compose.yaml # Optional; conventional Compose files may be discovered.
prepare:
  files:
    - path: .env.local
      required: true
    - path: certs
      required: false
  exclude: [certs/backups/**]
  allow: [certs/.cache/**]
start:
  services: [api, worker] # Compose owns their dependency graph.
  commands: [frontend]   # Ordered; tasks finish before the next command starts.
endpoints:
  website:
    command: frontend
    port: 0              # Allocate a native TCP port and provide it to the command.
    protocol: http
    browser: true
  api:
    service: api
    port: 8080           # Container port; assigned host binding is loopback-only.
    protocol: http
    browser: false
commands:
  frontend:
    mode: service
    host:
      cwd: apps/frontend # Relative to config directory, within the worktree.
      argv: [npm, run, dev]
      env:
        PORT: '${FUGATO_PORT_website}'
  test:
    mode: task
    run:
      service: tools     # Fresh Compose container; prerequisites must be started.
      argv: [npm, test]
  console:
    mode: interactive
    exec:
      service: api       # Requires an existing running service.
      argv: [python, -i]
checks:
  api-ready:
    http:
      endpoint: api
      path: /health
  tests:
    command: test
```

Without `.fugato.yaml`, a conventional Compose project starts all services that
have no profile, plus their dependencies, and infers no browser endpoints.
Run `fugato init` to save those choices, then add named endpoints deliberately.
An explicit configuration makes `start` authoritative, including an empty list.
`fugato doctor` shows the effective startup plan, including Compose dependencies
and services left unselected. `fugato up` prints that plan with the runtime state.
An empty plan is valid for a task-only project; `up` then starts no workloads.
Neither a configured endpoint nor a running runtime establishes HTTP readiness;
run a named check to verify the application.

`prepare` declares local inputs to copy from an explicitly selected source
project into a target worktree before startup. Each `files` entry names one
file or directory relative to the project root; directories are copied
recursively. `required` defaults to `true`. Existing destination files are
preserved. A target file that already exists satisfies the declaration even if
the source file is absent. Required missing sources block preparation, while optional missing
sources are reported. Preparation reports paths and statuses, never file
contents. It rejects symlinks at source and destination and cannot follow paths
outside either project. Copied regular files use mode `0600`; files with the
source owner's execute bit use `0700`. Newly created directories use mode
`0700`; other source permission bits are not preserved.

The default exclusions apply to dependency and generated-output directories at
any depth: `node_modules`, `.venv`, `venv`, `__pycache__`, `.next`, `.nuxt`,
`.cache`, `.gradle`, `.mypy_cache`, `.pytest_cache`, `.ruff_cache`, `.tox`, and
`.nox`. `exclude` adds slash-relative glob patterns; `allow` permits an explicit
file or directory to override only those default exclusions. An explicit
required file that also matches `exclude` is a configuration error. Explicit
exclusions win over `allow`. The `.git` component is always prohibited and
cannot be allowed. Globs support `*` and `?` within path components and `**`
as a whole component; excluding a directory also excludes its descendants.
Patterns may end in `/`, which is ignored when matching. For example,
`allow: [certs/.cache/**]` deliberately permits that cache subtree through the
default `.cache` filter; an `allow` pattern for a path that is not default
excluded has no effect.

All sections except `version` are optional. An empty `start` is valid for a
command-only project. Omitting endpoints or using `{}` adds no named endpoints;
no browser preview or HTTP readiness probe is inferred from service ports. Existing Compose
`ports` declarations still receive dynamic loopback bindings and appear in
`fugato ports`; native commands receive no allocated ports without endpoints.
Unknown fields, duplicate names, broken references, and invalid combinations
are errors. The old `web` and `database` fields are rejected, as is `version: 2`.
Startup selection and named endpoints express those choices explicitly.

`start.services` selects Compose roots plus their dependencies. `start.commands`
selects task/service commands in order after Compose startup. Interactive commands
must be invoked deliberately. A failed startup rolls back resources started by
that attempt and retains project data. Named checks are explicitly invoked; a
running process is not proof that an application is ready or a check passed.

Each command has `mode: service|task|interactive` and exactly one executor:
`host`, `run`, or `exec`. `service` commands use the host executor; persistent
containers belong in Compose. `run` creates a temporary Compose container with
`--no-deps`; declare prerequisites in `start.services` and start the environment
first. `exec` runs in an existing container; noninteractive exec requires its
`timeout` executable to bound execution. Noninteractive command requests have a five-minute limit;
in-container cancellation is bounded by that timeout rather than guaranteed
immediate termination. `argv` is an argument array, never an implicit
shell command. Host `cwd` defaults to the project directory. Host `env` overlays
the inherited environment. No manifest or documentation command runs during
discovery or selection.

Each endpoint has exactly one target (`service` or `command`), a port, and
`protocol: http|https|tcp|udp`. `browser` defaults to false and is permitted only
for HTTP(S). Command targets must be persistent host commands; native dynamic
ports use TCP (`http`, `https`, or `tcp`). Compose target ports are 1–65535;
native ports may also be 0 to request an allocation. Fixed native ports are
checked for collisions and cannot promise concurrent-worktree operation.

HTTP browser endpoints use stable
`http://<endpoint>.<runtime-id>.localhost:<proxy-port>/` URLs. Their names must
be lowercase DNS labels: 1–63 letters/digits/hyphens, with a letter or digit at
each end. HTTPS endpoints use their direct loopback binding and the application's
TLS configuration; their port may change after restart. Fugato does not terminate
TLS. HTTP browser previews always use their explicit endpoint name.

Native commands receive `FUGATO_RUNTIME_ID`, `FUGATO_DATA_DIR`, and
`FUGATO_PORT_<endpoint-name>` for allocated endpoints in the environment,
including Compose services. For example, a host frontend can set
`API_URL: 'http://127.0.0.1:${FUGATO_PORT_api}'`. A task or interactive command
referencing an endpoint requires that endpoint's environment to be running.
Supported variables are
expanded in host argv/env values without shell evaluation. Apps must honor the
chosen port and use the provided data directory where isolation is needed.
Fugato cannot isolate arbitrary paths or external services accessed by a host
process. Allocating a port before launch has a bind race; startup reports failure
rather than silently retrying or claiming ownership of another listener.

Fugato manages native process groups, bounded logs, cancellation, exit status,
and cleanup. Host workloads have the user's permissions. Stopping preserves
retained data. The process owner must stop native processes before daemon exit;
restarting the daemon must not signal a stale PID or report an orphan as healthy.

Browser verification remains an optional separate runner (`browser.service`
and `browser.profile`), not the `browser` flag on an endpoint. Multiple browser
endpoints require explicit selection by name; there is no arbitrary default.
The existing Compose browser runner can verify named Compose HTTP endpoints.
Native loopback endpoints support HTTP/task checks and browser opening; automated
browser checks for native apps require a separate runner and are unavailable.

## CLI and extension

- `init [--service NAME ...] [--compose FILE]`: create the initial configuration
  without overwriting an existing file. Native-only projects receive a safe
  commented template with no selected work; Compose projects select active
  services or the explicit service list.
- `schema`: print the installed CLI's version-matched JSON schema.
- `example native|compose|mixed`: print complete illustrative YAML to adapt to
  existing project commands and services; this does not write a file.
- `doctor --json`: validate setup and report selected and unselected startup work
  without starting workloads. A valid empty selection has an informational finding.
- `prepare --worktree TARGET --from SOURCE [--dry-run] [--json]`: prepare declared
  files for an existing target project from an explicit source project.
- `projects --worktree PATH --json`: bounded read-only project/candidate discovery.
- `create --worktree PATH`: validate and register a stopped environment without
  starting workloads. Copying declared local files remains an explicit `prepare`
  operation with a source project.
- `up`, `down`, `logs`, `ports`: operate on the selected environment.
- `open --endpoint NAME`: open a declared browser endpoint; selection required
  when several exist.
- `run NAME`: execute a named task or interactive command; preserve task exit status.
- `check NAME`: run a named HTTP or command check and report its actual result.

The existing `--worktree` option accepts a directory inside the desired project.
Bare package manifests are discovery candidates requiring configuration; they
are not automatically runnable projects. Documentation and package scripts are
inputs for an agent to propose configuration, not runtime authority.

The editor sidebar lists worktrees by branch, path, and environment status.
Worktrees with several registered environments expose their environments beneath
the worktree. Selecting either opens one reusable management tab in the editor;
the worktree restores its remembered environment or selects its only environment.
The management page keeps Start/Stop, Restart, Logs, and Configuration accessible,
with Overview, Logs, and Configuration sections. Logs can select a service when
the daemon advertises service log support. Destructive environment and worktree
actions have separate, explicit names.

Create environment opens a focused setup flow with configured projects first,
a directory picker, and optional advanced manifest candidates. Review and edit
configuration before creating; the resulting environment remains stopped until
Start. Discovery candidates never become sidebar environments automatically.
Worktree creation uses an existing or new local branch. Worktree removal protects
the primary checkout and refuses registered environments or local file changes,
including untracked and ignored files. Destroy environment retains application
data by default and does not remove the worktree.

No selection starts containers, commands, or a daemon. An explicit Start Daemon
action remains available. Configuration edits and external edits use the same
validator and refresh path.

Discovery skips symlinks, dependency/VCS/build output, and downloaded editor test
content such as `.vscode-test`. It respects standard Git ignore rules, including
nested `.gitignore` files and repository/global exclusions; tracked projects
remain discoverable. Entry/project/time limits still apply. It reports truncation
and per-candidate errors. Malformed project configuration remains visible and
editable in setup.

## Prompt for the user's agent

> Configure this repository for the installed Fugato schema. Inspect existing
> `.fugato.yaml` files, manifests/scripts, Compose definitions, environment
> templates, and local development documentation. Identify independent projects,
> persistent services, tasks, interactive commands, prerequisites, and endpoints.
> Reuse existing launch commands and infrastructure definitions. Write the
> smallest supported configuration; preserve existing choices. Browser intent
> must come from evidence of a UI, never an open port alone. Ask only about
> unresolved project/startup/browser choices. Use supported native port/data
> variables where the application accepts them; report isolation limitations and
> unsupported capabilities. Do not embed secrets, install dependencies, or start
> workloads as part of discovery. Run `fugato doctor --json`, fix configuration
> errors, and summarize startup, access, available checks, and remaining
> prerequisites. Static validation does not establish runtime success.

## Acceptance

Reject obsolete fields and unsupported versions. Exercise no-browser Compose, multiple previews,
no-Compose commands, mixed startup/rollback, native stop/cancellation/process exit,
command exit codes, interactive terminal attachment, invalid/unsafe configuration,
project discovery bounds, and extension selection with no startup side effects.
Run Go tests/race/vet/vulnerability checks and extension compile/unit/host tests.
Record any environment-limited integration checks explicitly.
