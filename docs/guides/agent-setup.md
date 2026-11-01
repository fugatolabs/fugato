# Connect your agent

Use the workflow skill and MCP together. The skill describes project setup,
preparation, verification, and cleanup. MCP gives your agent tools to inspect
and control registered runtimes. The release archive includes both `fugato`
and `fugato-mcp`, the setup helper, and the complete skill with references.

## Set up a project

Install the binaries and put them on PATH. Use Node.js 22.20 or later for the
setup helper. From your application's project directory, choose one profile:

```sh
node "$HOME/.local/share/fugato/0.0.1/setup-agent.mjs" --agent codex --project .
```

Replace `codex` with `claude-code`, `cursor`, `github-copilot` (VS Code), or
`gemini-cli`. Run again for each agent you use. Add `--dry-run` to inspect the
files that would change. Add `--bin-dir /path/to/bin` for binaries outside PATH.

For an unpacked release, run `node /path/to/release/setup-agent.mjs` with the
same options. From source, use `node /path/to/fugato/scripts/setup-agent.mjs`.
For a Homebrew installation containing the setup files, the helper is at
`$(brew --prefix fugato)/share/fugato/setup-agent.mjs`.

The helper performs three steps:

1. Copies the complete skill into a directory the selected agent discovers.
2. Adds persistent project instructions that point to the skill for Fugato work.
3. Adds a project MCP entry and `.fugato-agent/mcp-launch.sh`. The launcher starts
   the local daemon on demand and then executes the MCP binary over stdio.

It does not start application workloads or change global agent settings.
Existing unrelated settings are preserved. Conflicting Fugato settings, edited
skill files, malformed JSON configuration, or symlink targets stop setup before
writes. JSON configuration must be strict JSON; JSONC comments and trailing
commas need manual review/conversion. The helper does not repair existing TOML.

| Agent | Skill directory | Persistent guidance | MCP configuration |
| --- | --- | --- | --- |
| Codex | `.agents/skills/fugato-workflow` | `AGENTS.md` | `.codex/config.toml` |
| Claude Code | `.claude/skills/fugato-workflow` | `CLAUDE.md` | `.mcp.json` |
| Cursor | `.agents/skills/fugato-workflow` | `.cursor/rules/fugato.mdc` (`alwaysApply`) | `.cursor/mcp.json` |
| VS Code Copilot | `.agents/skills/fugato-workflow` | `.github/copilot-instructions.md` | `.vscode/mcp.json` |
| Gemini CLI | `.agents/skills/fugato-workflow` | `GEMINI.md` | `.gemini/settings.json` |

These files make the workflow available across sessions in that project.
They are project instructions, not a universal executable startup hook.
Agent trust settings, tool permissions, disabled skills, and managed policies
still apply; installation cannot guarantee a model follows every instruction.

MCP settings and the launcher contain absolute machine-specific paths, not
token values. Review those files before sharing them. Re-run setup for a new
worktree, machine, or moved checkout; it will report conflicts that need review.
The skill and instruction files can be committed for teammates. The launcher
uses the state directory and API/proxy ports from the setup environment, falling
back to Fugato's OS defaults. Configure those environment variables before
running setup if you use a non-default daemon.

## Verify the connection

```sh
# Starts the daemon if needed; does not start application workloads.
sh .fugato-agent/mcp-launch.sh --check
```

Restart the agent in this project and review its MCP trust prompt. Codex requires
a trusted project for project MCP configuration. Verify `fugato-workflow` appears
in the agent's skills and the `fugato` server exposes runtime tools. Ask it to
read the skill and list existing Fugato runtimes before starting workloads.
Agent installations without local filesystem/process access need a different
deployment arrangement; local binaries do not run inside a hosted chat service.

Use the [setup prompt](agent-quickstart.md) for your first environment. The CLI
or editor registers a project before MCP starts its runtime. Named tasks and
checks use the CLI/editor. MCP provides runtime control, logs, HTTP verification,
and supported browser verification; see the [MCP reference](../reference/mcp.md).

## Other agents and skill-only installation

For agents supported by the [skills CLI](https://github.com/vercel-labs/skills),
run this from your application's root and select your agent:

```sh
npx skills add fugatolabs/fugato --skill fugato-workflow --copy
npx skills list
```

This installs the skill and references; it does not register MCP or install the
persistent instruction block above. Use the selected agent's MCP documentation
with Fugato's [stdio settings](../reference/mcp.md). The setup helper explicitly
supports the five profiles listed above; other clients need their own adapter.

## Integration references

Profile formats were checked against official documentation on 23 October 2026:
[Codex skills](https://learn.chatgpt.com/docs/build-skills),
[Codex MCP](https://learn.chatgpt.com/docs/extend/mcp?surface=cli),
[Claude skills](https://code.claude.com/docs/en/skills),
[Claude MCP](https://code.claude.com/docs/en/mcp),
[Cursor skills](https://cursor.com/docs/skills),
[Cursor rules](https://cursor.com/docs/rules),
[VS Code skills](https://code.visualstudio.com/docs/agent-customization/agent-skills),
[VS Code MCP](https://code.visualstudio.com/docs/agent-customization/mcp-servers),
[Gemini skills](https://geminicli.com/docs/cli/skills/), and
[Gemini MCP](https://geminicli.com/docs/tools/mcp-server/).
