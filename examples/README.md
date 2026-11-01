# Examples

Start with **[example_1](example_1/README.md)** to test Fugato manually. The examples
include their configuration; you can run these walkthroughs directly or ask an
AI agent to follow them. An MCP connection is optional.
For your own application, use the [AI agent quickstart](../docs/guides/agent-quickstart.md)
to inspect its launch commands and prepare a configuration first.

| Folder | Application | Purpose |
| --- | --- | --- |
| [example_1](example_1/README.md) | Next.js + NestJS + PostgreSQL todo app, all in Compose | Main manual walkthrough: create/complete/delete todos, restart persistence, worktree isolation. |
| [example_2](example_2/README.md) | Smaller Node.js web + PostgreSQL todo app | A simpler Compose reference and the existing browser verification fixture. |
| [native-app](native-app/README.md) | Host Node.js services and tasks | Separate reference for testing without Docker. |

Use a terminal inside the chosen example directory. For example 1:

```sh
cd examples/example_1
cp -n .env.example .env
fugato doctor
fugato up
fugato check ready
fugato open --endpoint website
```

Run `fugato down` there when finished. PostgreSQL data is retained.
