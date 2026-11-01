# Configure a worktree with the CLI

The installed `fugato` binary carries the version 1 configuration schema and
native, Compose, and mixed examples. These read-only commands work anywhere and
do not start a daemon:

```sh
fugato schema > fugato.schema.json
fugato example native
fugato example compose
fugato example mixed
fugato help doctor
```

Use an example as a guide for an existing project. Match Compose service names,
container ports, host paths, and argv to the repository. `fugato init --worktree
PATH` creates `.fugato.yaml` once. On a native-only project it writes commented
guidance with no selected startup work; uncomment and edit real commands before
starting them. Existing configuration is never overwritten.

Check a project before starting it:

```sh
fugato doctor --worktree PATH
fugato doctor --worktree PATH --json
```

The startup plan lists selected Compose roots and commands in startup order,
Compose dependencies, and services and commands left unselected. A command
definition alone does not select it for `up`; add persistent service commands
to `start.commands`. Empty startup is valid for task-only projects, and `up`
reports that it started no workloads. Doctor checks configuration and host
prerequisites without running named checks. Runtime state and application
readiness are separate: use `fugato check NAME` for a configured check.

See the [environment reference](../reference/environment.md) for each field and
the [project reference](../reference/project.md) for discovery and port behavior.

## Prepare a worktree

Copy declared local inputs from an existing project in the same Git repository
to an existing target project directory:

```sh
fugato prepare --worktree /path/to/target/project \
  --from /path/to/source/project
fugato prepare --worktree /path/to/target/project \
  --from /path/to/source/project --dry-run --json
```

This command does not require a daemon or registered runtime. It uses only
`prepare.files` in the target project's `.fugato.yaml`. The source is required;
`--worktree` defaults to the current directory. Use explicit paths when an agent
operates across checkouts. Both projects must belong to the same repository. A dry run reports
the planned paths and statuses without copying. Preparation preserves existing
destination inputs, refuses symlinks, and does not print file values, rewrite
dotenv values, start Docker, or install dependencies. See the
[preparation guide](preparation.md) for a deterministic manual check.

## Stop or destroy a runtime

`fugato down --worktree PATH` stops the runtime and retains its registration and
data. To remove its containers, networks, and registration, run:

```sh
fugato destroy --worktree PATH
```

Add `--volumes` only when the runtime's named volumes and native
`FUGATO_DATA_DIR` should be permanently deleted. For missing worktrees or broken
configuration, select the saved runtime directly:

```sh
fugato ls
fugato destroy --runtime RUNTIME_ID --volumes
```

`--worktree` defaults to the current project and cannot be combined with
`--runtime`. Cleanup verifies saved identity and Docker ownership labels; it
never deletes images, build cache, source worktrees, or arbitrary host files.
It keeps the registry entry on failure and reports the error. See the
[resource guide](local-resources.md) for retained-data behavior and boundaries.
The destructive command is available through the CLI and authenticated lifecycle
HTTP API; the MCP tool set does not expose it.
