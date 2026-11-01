# Daemon lifecycle and resource boundaries

Decision date: 2026-09-28.
Scope: the local Compose/native daemon and its CLI/control API.

This record explains the daemon's concurrency and I/O contracts. Keep it alongside
the code so future contributors and agents can understand why the boundaries
exist. The implementation and regression tests are the executable specification.

## Runtime state belongs to the runtime gate

Lifecycle operations on one runtime serialize through a context-aware gate.
Validate the runtime ID before allocating a gate. Gate bookkeeping must account
for both holders and waiters, and remove unused entries; it must not grow with
every ID ever presented by a client.

Reconciliation may list runtimes to discover IDs, but must reload each record
after acquiring that runtime's gate. A pre-lock snapshot of `desired` is not
authoritative: a concurrent start or stop may already have changed it. Saving
that snapshot can undo a successful command, even when every memory access is
protected and `go test -race` passes.

## Cancellation follows the whole operation

Pass `context.Context` as the first argument through HTTP, service, manager,
registry, and backend calls. Database initialization and queries use the
context-aware `database/sql` APIs. SQLite busy timeouts do not bound waiting for
the Go connection pool.

Waiters must exit on cancellation without taking ownership later. If a backend
operation fails or is cancelled after desired state was persisted, recording
its final failure may use a separate, one-second deadline derived with
`context.WithoutCancel`. A failed Compose `up`, endpoint lookup, or web health
check may leave partially started containers; while the runtime gate is still
held, `Start` also runs a separate, bounded 30-second Compose `down` to remove
those containers and networks. It does not pass `-v`, so named-volume data is
retained for retry. If teardown fails, its error is joined with the original
failure. These exceptions authorize only state recording and teardown, never
continuing or retrying the workload after cancellation.

The daemon owns request and reconciliation lifetimes. Shutdown cancels work,
closes listeners/connections, and joins owned work before releasing registry
resources. An HTTP shutdown timeout alone does not bound other goroutines.
Upgraded connections require explicit shutdown ownership as well.

If a broken component ignores cancellation past the ten-second shutdown budget, return an
error while transferring the registry and instance lock to a cleanup owner that
waits for the remaining work. Do not close the registry under an active worker or
release the lock so a second daemon can overlap it. The executable's exit is the
final process boundary; the library does not call `os.Exit`. An embedding caller
must account for this exceptional retained ownership until the worker exits.

## Bound admitted work, including waiters

Limits are per daemon manager and shared by its API adapters. Admission is
nonblocking: excess work returns a recognizable busy error instead of creating
an unbounded queue of waiting goroutines. The caller can retry deliberately;
the daemon does not silently retry mutations.

| Operation class | Concurrent operations | Pending queue |
| --- | ---: | ---: |
| Runtime lifecycle/inspection | 4 | 0 |
| HTTP/browser verification | 2 | 0 |
| Scoped commands | 4 | 0 |
| Log collection/streams | 8 | 0 |
| Control metadata/Git lookups | 4 | 0 |

Acquire admission before expensive Git/Compose work and before waiting on a
runtime gate. Release it on every exit. Keep log streams separate so long-lived
followers cannot consume lifecycle slots. Avoid acquiring the same gate again
through a nested call. These are conservative defaults, not benchmark claims;
change them with resource measurements and corresponding tests.

HTTP admission is also bounded: API and preview each have 64 active handler
slots and 128 accepted connection slots. These budgets are separate so long-lived
preview traffic cannot consume all control slots. Upgraded connections retain
their connection slot until closed. Excess requests get HTTP 503; excess
connections are closed. The typed control and lifecycle APIs map operation overload to
HTTP 503 as well.

Per-operation output limits complement admission limits. A 12 MiB browser output
cap does not bound aggregate memory if an unlimited number of browser jobs run.
Go memory limits also do not cover the containers launched through Docker.

Local container CPU and memory limits belong in the project's Compose model.
Fugato's port override preserves `cpus`, `mem_limit`, and Compose deploy resource
limits; it does not add implicit caps that could make an otherwise valid app fail
to start. `doctor` reports missing per-service CPU or memory limits as an
informational finding, without blocking `up`. Revisit a Fugato-wide running
environment budget after representative workload measurements. For the local
Docker path, there is no host-wide hard cap or automatic volume eviction.
Developers can inspect actual container use and explicitly remove retained
volumes as described in the [local resource guide](../guides/local-resources.md).
Hosted environments require enforced quotas and retention rules before a pilot.

## HTTP and streams have different deadline policies

Unary control calls need finite deadlines; request bodies, connection setup,
upstream response headers, and blocked writes need explicit bounds. Apply idle
or phase deadlines to long-lived streams rather than a short total timeout that
would routinely terminate healthy logs or WebSockets.

| Phase | Budget |
| --- | --- |
| Control unary request / CLI unary call | 6 minutes |
| Legacy CLI finite logs | 1 minute |
| Typed control client | 70 seconds (existing client budget) |
| MCP tool call | 65 seconds |
| Incoming headers | 5 seconds |
| Incoming request body | 10 seconds |
| Connection dial | 5 seconds |
| Preview upstream response headers | 30 seconds |
| Blocked stream write/flush | 15 seconds, renewed per write |
| Idle HTTP keep-alive | 1 minute |
| Control Git metadata lookup | 5 seconds |

Earlier caller deadlines still win. Quiet follow logs and upgraded connections
have no total lifetime limit; a body-read deadline must not be applied to their
background connection read. The preview body budget also constrains slow uploads;
supporting long streaming uploads would require an explicit separate policy.
The typed client's shorter budget, and the MCP adapter's 65-second tool budget,
may cancel a slow start before the daemon's maximum startup budget; callers must
inspect runtime state afterward.

HTTP success must not conceal a failed log command. Return errors before
committing success where possible; once bytes have been sent, use a documented
terminal status that the CLI checks. An empty response and EOF alone cannot
prove successful log collection.

The CLI logs endpoint declares the `Fugato-Log-Status` HTTP trailer. On EOF,
`ok` means clean completion and `error` means backend failure; a missing trailer
is not success. Before streaming starts, failures use a non-2xx response. For a
quiet follow stream, starting the Docker process opens the response; a later
Docker failure is reported through the trailer. This acknowledges process start,
not Docker readiness. CLI cancellation by the user remains a normal exit.
Update/restart the daemon together with its CLI when adopting this contract.

## Validate every container-creation path

Browser verification launches a new Compose service and therefore validates the
current effective Compose model before running it. A previous `up` validation
does not authorize files that have changed since startup. Regression tests must
exercise the browser entry point with privileged settings and unsafe bind mounts,
and prove no `compose run` happens.

Validate explicitly configured endpoint container ports against the source Compose
model before generating Fugato’s port override. Checking only the resulting
model could hide a mismatch with ports already declared by the application. An
explicit endpoint port is allowed when the service declares no ports or expose
entries. Compose fixture tests cover both cases and mismatch rejection.

## Editor metadata and verification evidence

The local editor extension reads repository worktrees and resolves project
identity through authenticated, read-only daemon endpoints. These operations
share the four-slot metadata admission gate and a five-second Git deadline.
Worktree discovery accepts only absolute directory paths, invokes Git without
a shell, caps the command output at 1 MiB and the returned list at 200 entries,
and marks list truncation explicitly. A project-resolution result does not
authorize Compose execution. First registration may include an expected runtime
ID; the manager compares it with the freshly resolved identity before taking
a runtime gate or changing state.

Typed runtime reads retain registry state when Git commit metadata is missing.
They leave `commit` empty and return a separate `metadata_error`; lifecycle
`last_error` remains untouched. Start and verification still revalidate the
project and its effective Compose model where required.

Latest completed HTTP and browser reports are shared by all daemon clients in
one manager-owned memory cache. Operation errors do not replace a completed
report. The cache retains at most one report per kind for 100 runtimes, limits
each serialized report to 512 KiB, and evicts least recently completed runtime
entries above 16 MiB total. Reports disappear on daemon restart. Persisted PNG
artifacts are separate files under `state/artifacts/ID`, with no automatic expiry.

Project discovery remains read-only and bounded to five seconds, 10,000 scanned
entries, and 100 candidates. It excludes generated/downloaded editor content
including `.vscode-test`, and honors Git's standard ignore exclusions. Discovery
disables Git's filesystem monitor hook while reading exclusions. Its results
belong to explicit environment setup; they are not registered runtimes
or worktree navigation entries.

Project discovery preserves the `checks` name list and adds optional sorted
`check_details` entries with `name`, `requires_running`, and an optional `reason`.
The prerequisite follows existing execution contracts: HTTP checks need a live
endpoint, Compose exec tasks need a running service, and native task arguments
or environment values referencing `${FUGATO_PORT_*}` need live endpoint bindings.
Ordinary native tasks and Compose run tasks have no inferred running prerequisite.
This is configuration metadata only; it neither proves readiness nor infers
application dependencies, and it does not start workloads or retry checks.

## Editor worktree lifecycle

Decision date: 2026-10-22. Authenticated POST
`/v1/repositories/worktrees/create` creates a checkout from an existing local
branch, or creates a new branch from an explicitly chosen commit/ref (default
`HEAD`). POST `/v1/repositories/worktrees/remove` removes only linked worktrees;
it retains the branch. These calls share metadata admission and have a 30-second
whole-operation deadline. Individual metadata discovery retains its five-second
Git budget. Git receives separate argv, bounded stdout/stderr, disabled
checkout hooks and disabled filesystem monitors; worktree creation does not
start or register an environment. Git mutation commands own a fresh process
group; cancellation kills that group so ordinary checkout-filter children cannot
keep working or hold inherited output pipes open. Pipe draining has a two-second
`WaitDelay` fallback. Successful commands never signal their former process group;
helpers that deliberately detach from the group are outside this ownership boundary.
A cancelled mutation may have partially created a checkout or branch; callers
inspect repository state before deliberately retrying, without automatic rollback.

Environment registration and removal share a context-aware registration gate.
Registration first resolves identity and acquires the runtime gate. Existing
records bypass registration admission because removal already refuses them.
For a new record, registration acquires the shared gate, resolves identity again
to detect a checkout removed while waiting, and holds registration through the
first registry save. It releases this gate before starting workloads; the runtime
gate still owns startup. A waiter behind a long runtime operation therefore
cannot hold the shared gate and block unrelated worktrees.
Removal holds registration through its registry check and Git mutation, refusing
any worktree with registered environments. This prevents removal from racing the
first save of an environment whose project identity has already been resolved.
Waiters retain their operation admission slot and exit when cancelled.

Removal protects the primary checkout, validates repository membership, and
refuses tracked modifications, untracked files, and ignored files that Git might
otherwise discard. It never passes a force flag, so Git's lock and submodule
protections remain effective. A missing linked worktree remains removable after
its registered environments have been explicitly destroyed.
Dirty-checkout refusals include a bounded list of porcelain-v1 paths, labelled
changed, untracked, or ignored, and preservation guidance. Only paths are exposed,
never file contents. The editor offers opening the folder for review; it does not
discard, stash, or retry removal automatically.
Git's `prunable` marker can also mean a remaining directory lost its `.git`
file. Removal checks that the directory itself is absent before taking the
missing-checkout path. If files remain, it refuses with an explicit instruction
to preserve or move them first; it never forces deletion, repairs Git metadata,
or prunes other worktree registrations. Regression coverage distinguishes a
missing directory from a missing `.git` marker and preserves the branch in both.

## Read-only editor service inventory

Decision date: 2026-10-21. Authenticated GET `/v1/runtimes/{id}/services`
reports observed Compose/native services and their ports. It uses lifecycle
admission and the runtime gate, revalidates current project identity, and refuses
stopped runtimes. The whole request has a 15-second budget; Docker inspection has
a nested ten-second budget, at most 256 containers and 1 MiB of command output.
Caller cancellation propagates through the gate and subprocesses.

Compose inspection selects containers by exact project label and verifies each
returned container ID, service label, and working directory within the worktree.
It only lists/inspects containers; it does not evaluate Compose or run scripts.
One-off task containers are omitted. Internal ports come from exposed-port and
network metadata with host port zero and no URL. Named endpoint metadata is
joined to observed published ports; existing preview URL validation remains in
effect. Native services come only from in-memory owners; exited processes do not
report live bindings. Process state and health are separate observations.

The editor polls inventory only for its selected running environment and
suppresses unchanged view events. Service inspection errors stay in that view
without being reported as a daemon outage. Older daemons fall back to the existing
published-port API with an explicit update hint.

The existing logs request accepts an optional service name and `service_kind`
(`native` or `compose`). A shared name in a mixed environment requires the kind;
an ambiguous unqualified selection fails explicitly. Filtering retains
log admission, runtime identity checks, the 20-second collection deadline, and
the 64 KiB response limit. Compose receives the validated service as a separate
argument after `--`. Native owners retain at most 64 KiB per process alongside
the existing 256 KiB aggregate tail; these buffers exist only in daemon memory.

## Deliberate scope

The current control transport is authenticated loopback HTTP. UDS with peer
credentials, supervisor packaging, readiness/watchdog integration, platform path
migration, telemetry, and remote-host protocols are separate architectural work.
This lifecycle fix does not claim those capabilities exist.

Keep the foreground daemon entry point. Go's supported `os/exec` launch with
`Setsid` is not a raw fork continuing the Go runtime, although detached startup
still needs a future supervision/log-retention policy. Do not add a remote
queue, generic circuit breaker, automatic update system, or mutation retries
without a concrete delivery and trust model.

## MCP token changes

Decision date: 2026-10-01. The MCP adapter is a long-lived process, while a
local state reset can replace the daemon and its token file. The MCP client
reads the configured, user-only regular token file for each daemon request and
rejects symlinks and unsafe replacements. This lets the next request use a new
token without restarting the MCP process. An unauthorized response reports the
API URL/token-file mismatch and is returned to the caller; mutations are never
retried automatically. The installer runs a bounded read-only daemon request
through the installed MCP binary to verify the pairing.

## Environments and native ownership

Decision date: 2026-09-29. The version 1 environment contract has explicit Compose roots,
ordered native service/task commands, and zero or several named endpoints.
Discovering or selecting a worktree does not start a daemon or execute project
scripts. CLI project/worktree discovery and doctor are read-only, bounded entry
points. The config parser is shared by discovery, doctor and runtime operations.

The daemon's EnvironmentBackend owns native process groups in memory. Native
PIDs are never persisted or restored. A restarted daemon reports missing native
owners rather than signalling a recycled PID or adopting a foreign listener.
On macOS and Linux the child owner uses waitid(WNOWAIT) to observe leader exit
without reaping it. It kills remaining group members before Cmd.Wait reaps the
leader, so leader exit cannot strand children and the group ID remains reserved
through cleanup. Stop uses the captured startup project after independently
rechecking canonical worktree/repository identity, so editing or corrupting the
configuration cannot strand an owned native process. After daemon restart the
captured config is unavailable; Compose recovery still requires valid current
configuration. Stop sends SIGTERM, escalates to SIGKILL after one second, and
joins the owner. Daemon shutdown joins requests/reconciliation, stops native
owners, and only then releases registry and instance-lock ownership.

Each native environment retains at most 256 KiB of aggregate log tail, plus
64 KiB per process for service-filtered logs. Starting a new environment is
refused when 100 environment owners are already retained in memory. This check
is in startup; Compose reconciliation can also restore owner entries and does
not apply that limit. Finite commands have a five-minute
budget and separate 64 KiB stdout/stderr caps. Native argv is executed directly;
only validated ${FUGATO_*} placeholders expand, without shell evaluation.
Host executable lookup honors the configured cwd and PATH. Any command can
reference an available named endpoint port; Compose bindings are collected before
native startup. Stopped/exited native owners do not supply stale port variables.
Interactive commands that reference endpoint ports fetch current daemon bindings
read-only; standalone interactive commands do not start a daemon.
Interactive host commands share the invoking terminal's process group and run
in the CLI foreground. They are not daemon background jobs.

Start remains serialized by runtime gate. On startup failure, owned native
children are stopped and Compose teardown uses a separate bounded context;
data directories and named volumes are retained. Native port reservations are
released immediately before command launch; the bind race remains documented,
and apps must honor provided ports/data paths. Host execution is not a sandbox.

Named tasks/checks acquire command/verification admission and runtime gates.
They resolve the current config and identity on every invocation. A named command
only executes saved argv; arbitrary agent exec continues to require its existing
allowlist. Every Compose run/exec revalidates the effective model. Named run uses
--no-deps, so prerequisites must be started explicitly. Named noninteractive exec
requires the container's timeout executable, limiting a disconnected process to
300 seconds; killing the Docker client alone cannot guarantee immediate remote
process termination. Temporary run containers receive a unique owned name and
are forcibly removed after cancellation. HTTP checks have a ten-second budget,
disable redirects and target a declared loopback binding. Process liveness is
not HTTP readiness; startup does not infer or run named checks.

HTTP browser endpoints use <endpoint>.<runtime-id>.localhost preview hosts.
The proxy resolves an exact declared running browser endpoint; endpoint names
must be lowercase DNS labels. API URLs and HTTPS endpoints remain direct
loopback bindings. Runtime preview_url exists only for a single browser endpoint.
Compose-only environments reconstruct endpoint metadata during reconciliation
without adopting native process identities. Only named endpoint preview routes are supported.

The unsupported prerelease single-endpoint registry shape is rejected before
schema mutation. A fresh FUGATO_STATE_DIR is required; the old registry and
retained data are never automatically migrated or deleted. This internal storage
check is independent of config version: 1, API /v1, and doctor schema_version: 1.

Persisted observed=running is not live readiness after daemon restart. Until
inspection restores that runtime's in-memory endpoint snapshot, reads expose
observed=unknown and no preview URL. Snapshot readiness is separate from endpoint
count so a valid worker-only environment can become running. Reconciliation
publishes the complete snapshot before metadata can report running; named preview
routing is therefore available when a caller observes the restored running state.
A blocked-inspection regression test covers this ordering.

## CLI startup plan and installed configuration reference

Decision date: 2026-10-01. Doctor's version 1 JSON report adds a `startup`
object when the effective plan can be resolved. It lists selected Compose roots,
their dependency closure, available Compose services outside that closure,
ordered selected commands, and other named commands. This is configuration
evidence, separate from persisted desired state, observed process state, and
named check results. An empty selection remains valid for task-only projects;
doctor reports it as information and `up` says that no workloads were selected.
If Compose inspection fails, doctor reports a blocking finding and omits the
incomplete plan. `up` resolves the plan before starting the daemon or workloads.

The CLI binary bundles the version 1 JSON schema and native, Compose, and mixed
configuration examples. `fugato schema` and `fugato example` only print to
stdout; they do not discover projects, start a daemon, write configuration, or
execute project commands. A regression test keeps the bundled schema byte-for-
byte equal to `docs/reference/fugato.schema.json`. Native `init` leaves startup
empty and supplies commented guidance until real project commands are chosen.

## Explicit environment creation

Decision date: 2026-10-22. Authenticated POST `/v1/runtimes/create` accepts an
absolute project `path` and optional `expected_runtime_id`. The CLI exposes the
same operation as `fugato create --worktree PATH`. Creation uses lifecycle
admission, canonical project resolution, the expected identity check, and the
runtime gate before registering `desired: stopped`, `observed: stopped`.
Compose projects pass the existing source and effective-model validation before
registration. Validation invokes only Compose configuration reads; creation does
not start containers, run native commands, allocate endpoint ports, or run checks.
Cancelled and invalid creations leave no new registration.
The HTTP success response uses the existing versioned control runtime DTO, like
inspection, start, and stop. It includes `commit` as a string even when Git
metadata is unavailable, with `metadata_error` describing that separate failure.

Repeated creation returns the existing registration without changing its desired
state, observed state, or failure evidence. It never stops an existing running
environment. Start remains a separate explicit operation and revalidates current
configuration; `up` retains its existing create-and-start compatibility behavior.
The registration guard serializes project resolution/registration with worktree
removal, preventing a removed worktree from acquiring a late registration. Start
releases that guard after its first registration save, before launching workloads.

File preparation retains its separate source/target authority below. Creation
never guesses a source checkout or copies files automatically. Required local
files must be prepared explicitly before creating the environment.

## Explicit worktree preparation boundary

Worktree preparation is a filesystem operation that can run before runtime
registration. The authenticated local control API exposes it separately from
runtime-ID operations; the adapter rejects browser-origin requests. Source and
target project directories are explicit request fields and must resolve inside
the same Git repository. The target's `prepare.files` declaration is the only
copy authority. Symlinks and `.git` paths are prohibited, existing destinations
are preserved, and reports contain paths/statuses without file contents.

Preparation shares the bounded four-slot metadata admission gate, has a
60-second operation deadline, retains caller cancellation, and does not retry.
Dry-run and copy use the same bounded planning rules. A failed preflight returns
its structured report; failures after copying begins report completed and
blocked entries so callers can inspect partial progress. Copied regular files
are `0600`, with the source owner's execute bit preserved as `0700`; newly
created directories are `0700`. Other source permission bits are discarded.

## Explicit runtime destruction

Editor clients check the authenticated `/v1/info` feature map before dispatching
new lifecycle operations: `create_environment`, `destroy_environment`, and
`worktree_lifecycle`. Missing capabilities require an explicit daemon update;
clients do not probe support with a mutation or retry a failed mutation. The
feature map belongs to the running daemon, so replacing the CLI binary alone
does not update it. Existing service inspection and filtered logs use the
`service_inventory` and `service_logs` capabilities in the same way.

Decision date: 2026-10-21. `destroy` is admitted through the lifecycle budget and
holds the runtime gate until cleanup and registry deletion complete. HTTP and
browser verification hold that same gate from target resolution through final
report recording, preventing a browser worker or completed report from appearing
after destruction. Verification admission is acquired before waiting on the gate.
It first
checks the saved runtime ID hash, absolute paths, and `fugato-ID` Compose project
identity. Cleanup uses saved identity so deleted worktrees and changed/broken
configuration do not strand resources. It never runs project scripts or loads
Compose configuration. The explicit `--runtime` selector avoids local discovery.

Docker selection uses the exact Compose project label. Every selected container,
network, and requested named volume is inspected before any Docker mutation, then
rechecked immediately before deletion. Container labels must identify a service
and a working directory within the saved worktree; Compose resource names must
retain the validated runtime prefix and their network/volume ownership labels.
The working-directory boundary allows Compose files below the project directory.
Networks and volumes are removed without force, so Docker refuses resources
still in use. Container removal never uses `--volumes`; only explicit
`--volumes` requests remove named volumes. No image or pruning operation exists.
Docker output, resource counts, subprocess lifetime, and cancellation are bounded.

Native cleanup signals only in-memory captured process owners and joins them.
After restart it does not adopt or signal persisted PIDs. Native-only registry
records need no Docker; a runtime that has ever used Compose keeps that backend
marker when a later startup config becomes native-only, preserving knowledge of
retained Docker resources. `--volumes` also removes only `state/data/ID`, through
pinned `os.Root` handles. State/data/runtime symlink replacements are refused;
symlinks inside a runtime directory are removed without following their targets.
Host-command data elsewhere is outside this cleanup boundary.

Destroy persists stopped desired state before mutation. A failure or cancellation
retains the record and error, does not resurrect workloads, and is never retried
automatically. Success removes the registry entry, backend owner/endpoint snapshot,
and in-memory verification reports while still holding the gate. Reconciliation
reloads under the same gate, so an earlier snapshot cannot recreate the record.
Default destruction retains named volumes/native data; because registration is
forgotten, later deletion requires re-registering that same project identity.
Persisted browser artifacts, images, and build cache remain after destruction;
browser artifacts have no automatic expiry. The authenticated lifecycle API accepts POST
`/v1/runtimes/{id}/destroy` with `{ "volumes": false }` (or explicit true); this
release does not add an MCP destructive tool.
