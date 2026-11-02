---
name: fugato-workflow
description: Configure, start, verify, and stop application environments in Git worktrees using Fugato. Use when a user asks to run or develop an application with Fugato, including diagnosing setup failures.
license: Apache-2.0
---

# Fugato workflow

Apply the [shared compatibility guide](references/application-compatibility.md), which defines the requirements for both human developers and agents. Read the sections relevant to the application's setup and its current limitations. This skill adds execution guidance, not a second set of application requirements.

Install the whole `fugato-workflow` directory, including `references/`. The bundled references are maintained copies of the product documentation, so the skill works outside the Fugato checkout. Install the Fugato binaries first; the agent setup helper can then install this skill, persistent project guidance, and MCP registration together.

## Identify and prepare the project

- Establish the exact target worktree and project directory. For an existing environment, match its path before operating on a runtime ID. Never infer the project from the MCP server's working directory.
- If creating a worktree is in scope, use the requested Git state. Explain when relevant that uncommitted edits and ignored inputs are not copied. Preserve unrelated checkouts and running environments.
- Inspect Compose, manifests, environment templates, scripts, and existing `.fugato.yaml` without executing discovery inputs. For a monorepo, select the intended project and startup roots; do not start every service merely because it exists.
- Use `fugato schema`, `fugato example compose|native|mixed`, and the [configuration reference](references/environment.md) to prepare only supported configuration. Preserve existing user choices. `fugato init` scaffolds a missing file; it does not supply missing credentials or make the application ready.
- Inventory required local inputs without printing their contents. Declare only explicit project-relative files or directories under `prepare.files`, then use `fugato prepare --worktree TARGET --from SOURCE` or MCP `prepare_worktree` with explicit project paths. Read the [preparation guide](references/preparation.md) for filters and supported inputs. Review a `--dry-run --json` report first when useful. Existing destinations are preserved; required missing inputs block preparation, and optional missing inputs are reported. A source must be explicitly selected from the same repository. Do not copy an entire ignored tree, expose file values, or rewrite arbitrary dotenv values.
- Check internal/container versus host/browser addresses, shared external state, and unsupported features using the guide. A limitation such as Compose secrets is a compatibility blocker, not permission to weaken credential handling. Report the supported option or the specific unmet requirement.

## Start and verify

1. Run `fugato doctor --worktree <project> --json` and interpret its findings. Static readiness is not application readiness.
2. Start the selected environment with `fugato up --worktree <project>` when authorized. Inspect the resulting startup plan and endpoints. For an already registered runtime, MCP may start that explicit runtime instead.
3. Run the applicable named CLI check or MCP verification. Open the returned named preview when useful. “Browser: unconfigured” concerns an automated browser runner, not ordinary browser opening.
4. Verify the requested behavior. Use the guide's two-worktree acceptance gate when establishing concurrent-worktree compatibility; a routine small source edit may need only a focused check. Confirm automatic reload with an actual edit and response, not merely a watcher startup message.

Use the [MCP reference](references/mcp.md) and the server's advertised tool schemas. Current MCP tools include `list_runtimes`, `get_runtime`, `start_runtime`, `stop_runtime`, `get_preview_url`, `get_logs`, `prepare_worktree`, `run_command`, `verify_http`, and `verify_browser`. `prepare_worktree` accepts an unregistered target through explicit `path` and `from` values; other runtime mutations require an existing runtime ID. If the session lacks shell access and the runtime is unregistered, use MCP preparation if available, then explain the CLI registration step for startup.

MCP `run_command` has a separate user-owned exact-argv policy and a maximum requested duration of 30 seconds. Do not create or relax that policy merely to make a command pass. A configured CLI task is a different interface with its own contract; a denied MCP command is not permission to bypass it through another tool. Use the authorized execution path and report the limitation when none is available.

## Diagnose and finish

- Inspect bounded logs and the failing check. Report the actual failure and next action; do not repeatedly restart an unchanged failing environment or infer success from a running process.
- Use current endpoint URLs/bindings. Native host processes must honor configured port/data variables; containers keep their internal service ports. Dependency changes need the project's documented install/rebuild path.
- Use `fugato down --worktree <project>` or `stop_runtime` for ordinary shutdown. Both retain named volumes and native project data; container writable layers are not preserved. Neither is a full disk cleanup operation. Do not prune global Docker resources as a worktree cleanup shortcut.
- For an explicit environment cleanup request, use `fugato destroy --worktree <project>` or select a stale runtime with `fugato ls` and `fugato destroy --runtime <id>`. Ordinary destroy retains named volumes and native data; add `--volumes` only when the user requests deletion of that environment's persistent data. Read the [resource and cleanup guide](references/local-resources.md) before deletion. Never remove shared images, prune Docker globally, or delete the source/worktree as a cleanup shortcut.
- Report the target worktree/runtime, what started, preview URL if any, checks actually passed, remaining limitations, and whether the environment was left running. Keep secret values out of the response.
