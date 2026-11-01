# Fugato

Fugato runs development environments for projects inside Git worktrees. Your
coding agent, terminal, and editor can use the same local engine to start
services, inspect logs and previews, and verify changes. Projects can use Docker
Compose, native processes, or both.

The local engine is free and licensed under [Apache 2.0](LICENSE). Get the source
and release binaries at [fugatolabs/fugato](https://github.com/fugatolabs/fugato).

## Get started

1. **[Install Fugato](docs/guides/install.md).** The guide starts with release
   binaries and the Fugato Homebrew tap, then covers source-checkout and local
   archive installation.
2. **[Connect your agent](docs/guides/agent-setup.md).** Install the workflow
   skill, persistent project instructions, and MCP configuration.
3. **[Open the agent quickstart](docs/guides/agent-quickstart.md).** Give your
   agent the setup prompt in your application's worktree. It inspects the app,
   prepares configuration, starts the selected workloads, and checks the result.
   You can also follow the [manual example](examples/example_1/README.md#start-a-feature-worktree).

An agent with terminal access can use the CLI directly. MCP and the optional
[VS Code / Cursor extension](docs/guides/editor-extension.md) have separate
connection setup.

## Install for your agent

Use the workflow skill and bundled MCP server together. After installing the
[binaries](docs/guides/install.md), run the setup helper in your application's
project directory (Node.js 22.20+):

```sh
node "$HOME/.local/share/fugato/0.0.1/setup-agent.mjs" --agent codex --project .
```

Choose `codex`, `claude-code`, `cursor`, `github-copilot` (VS Code), or
`gemini-cli`. The helper installs the complete skill, adds persistent project
guidance, and configures the local MCP launcher. It preserves unrelated settings
and stops on conflicts. See [agent setup](docs/guides/agent-setup.md) for the
files each profile uses, source/archive paths, and verification.

For skill-only installation and additional agents, use the
[skills CLI](https://github.com/vercel-labs/skills) and select your agent:

```sh
npx skills add fugatolabs/fugato --skill fugato-workflow --copy
npx skills list
```

Use `--agent NAME` to select an agent directly or `--global` for a user-wide
skill. The version-pinned skill source is
`https://github.com/fugatolabs/fugato/tree/v0.0.1/skills/fugato-workflow`.
A local checkout's `skills/fugato-workflow` directory also works as the source.
The skills CLI installs instructions and references; MCP and persistent project
guidance need separate setup unless you use Fugato's combined helper.

## How it works

Fugato resolves a configured project inside the selected worktree. A worktree
can contain several projects, so choose the intended project directory.

- `fugato doctor` checks configuration and prerequisites without starting workloads.
- `fugato up` starts the daemon if needed and starts the selected workloads.
- `fugato ports` reports current loopback bindings.
- `fugato run NAME` runs a configured task or interactive command.
- `fugato check NAME` runs a named task or HTTP check.
- `fugato down` stops workloads, retaining named volumes and native project data.

Without `.fugato.yaml`, conventional Compose projects start their unprofiled
services and dependencies, but infer no browser endpoints. Native projects need
explicit configuration. `fugato init` creates a scaffold without replacing an
existing file; edit its startup commands and named endpoints for your application.

Use the [environment configuration](docs/reference/environment.md) to select
services and commands, declare endpoints, and define checks. Automated browser
verification requires a Compose endpoint and `browser.service`. Native
applications must honor their configured ports and data paths. External
databases, caches, and other shared services need explicit separation if parallel
worktrees must not share state.

The [application compatibility guide](docs/guides/application-compatibility.md)
explains these requirements. The [preparation guide](docs/guides/preparation.md)
covers declared local inputs for fresh worktrees.

In the editor extension, selecting a worktree opens its details without starting
workloads. Check setup, choose a project, register its environment, and start it
explicitly. Registering an environment does not copy ignored inputs.

## Examples and source development

The [examples index](examples/README.md) contains:

- [Next.js + NestJS + PostgreSQL](examples/example_1/README.md): a manual two-worktree walkthrough.
- [Node.js + PostgreSQL](examples/example_2/README.md): a smaller Compose reference.
- [Native app](examples/native-app/README.md): package scripts, two previews, a worker, tasks, and an interactive terminal.

For a complete local Cursor setup from an authorized source checkout, run
`./scripts/install-local.sh --project /absolute/path/to/your-project`.
This builds the binaries and extension and configures Cursor MCP. See the
[installation guide](docs/guides/install.md) for prerequisites and options.
For building and contributing, start with [CONTRIBUTING.md](CONTRIBUTING.md).

Stopping an environment does not preserve a container's writable layer.
See [resources and cleanup](docs/guides/local-resources.md) before removing data.

## Repository layout

- `packages/fugato/`: local runtime, CLI, daemon, MCP server, and browser verifier.
- `packages/cursor-extension/`: VS Code and Cursor integration.
- `examples/`: runnable application examples.
- `docs/guides/`: installation, agent setup, and usage guides.
- `docs/reference/`: configuration and interface contracts.
- `skills/fugato-workflow/`: portable agent instructions and bundled references.

## License

Copyright 2026 Fugato contributors.

Fugato is licensed under the [Apache License, Version 2.0](LICENSE).
Third-party dependencies retain their respective licenses; see
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
