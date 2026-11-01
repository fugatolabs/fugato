# Get started with your AI agent

Install the [Fugato binaries](install.md), then [connect your agent](agent-setup.md).
We recommend the workflow skill and MCP together: the skill teaches the process,
while MCP supplies tools for runtime inspection, lifecycle, logs, and verification.
The setup helper installs both the skill and persistent project guidance and
registers the bundled MCP server for your selected agent.

Open your application's Git worktree in a local coding agent that can inspect
files, edit configuration, and run terminal commands.

## Set up the first environment

Paste this prompt into your agent in the worktree you want to run. It authorizes
configuration and startup of that project; use code you trust.

```text
Set up this application's current Git worktree with Fugato.

Read the installed fugato-workflow skill and its relevant references first. Confirm that the Fugato MCP server is available; report a missing connection before proceeding. Use MCP for supported runtime inspection, logs, and verification, and the CLI for configuration, registration, and named tasks/checks.

Identify the exact project directory. Read its development docs, manifests, Compose files, scripts, and existing .fugato.yaml. Preserve existing configuration and unrelated changes. Ask me if the project or startup services are ambiguous.

Check that the Fugato CLI is installed. Use fugato schema and the relevant fugato example (compose, native, or mixed) to configure the selected services, native commands, named endpoints, and useful checks. Reuse the app's real launch commands. Do not invent credentials or print secret values.

Identify required ignored inputs. If files need preparing from another worktree, ask me for the source and use Fugato's declared preparation workflow. Keep environment data and ports separate where the application supports it; report any conflicts or shared external state.

Run fugato doctor for this project. Fix configuration errors, then start the selected workloads with fugato up. Run the checks the project defines and verify a relevant HTTP endpoint when available. A running process alone does not prove readiness.

Report the project path, runtime ID, actual preview URLs, checks passed or failed, and anything still missing. Leave the environment running for me. Do not delete persistent data or relax an execution policy.
```

Expect the agent to ask about ambiguous startup choices or missing inputs.
Project configuration must use real commands, services, and endpoints. The
[application compatibility guide](application-compatibility.md) explains
requirements for ports, data, ignored inputs, and parallel worktrees.

## Skill discovery and MCP connection

Use the [agent setup helper](agent-setup.md) for Codex, Claude Code, Cursor,
VS Code Copilot, or Gemini CLI. It installs the skill in the agent's discovered
directory, adds a project instruction to use it for Fugato work, and registers
the stdio MCP launcher. Restart your agent and check its skill and MCP lists.

Other agents can install the skill through the skills CLI and use their own
MCP configuration format; see the [setup reference](agent-setup.md#other-agents-and-skill-only-installation).
The [MCP reference](../reference/mcp.md) explains connection requirements.
Project instructions help skill discovery across sessions but do not override
user scope, tool permissions, or managed agent policies.

The CLI/editor registers an environment before MCP starts its runtime. MCP
preparation can operate on an existing worktree before registration. Named
tasks and checks use the CLI/editor; MCP's container command tool has a separate
execution policy. Automated browser verification requires a Compose endpoint
and configured browser runner; native endpoints support browser opening and
HTTP verification.

The [VS Code and Cursor guide](editor-extension.md) covers the separate,
optional editor extension.

## Check the result and continue

The agent should report the selected project path, runtime ID, current endpoint
URLs, and checks actually run. A running process alone does not establish that
the application is ready. If a check fails, ask for the failure and the next
action, not an unverified success claim.

For the next change, ask the agent to use the Fugato workflow, make your requested
edit, and rerun the relevant configured check. Use the two-worktree acceptance
steps in the compatibility guide when establishing parallel-worktree support.

To stop an environment, ask the agent to stop that project's Fugato runtime.
Ordinary shutdown retains named volumes and native data; container writable
layers are not preserved. Persistent data deletion requires an explicit request.
See [resources and cleanup](local-resources.md).
