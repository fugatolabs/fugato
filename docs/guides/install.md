# Install Fugato

Fugato runs separate development environments for Git worktrees using native commands, Docker Compose, or both.

For editor-based worktree management and browser previews, see the [VS Code and Cursor extension guide](editor-extension.md).

## Prerequisites

- macOS or Linux and Git, plus the tools required by your project.
- Docker Engine with Docker Compose for container workloads. Native-only projects do not require Docker. Run `docker info` and `docker compose version` when using Compose.
- No Go or Node.js installation is needed to run the packaged binaries.

## Download a release

Download version 0.0.1 for macOS/Linux on AMD64 or ARM64 from the
[GitHub releases page](https://github.com/fugatolabs/fugato/releases).
Install with one command; the installer selects your platform and verifies
the archive's SHA-256 checksum:

```sh
curl -fsSL --proto '=https' --tlsv1.2 https://github.com/fugatolabs/fugato/releases/download/v0.0.1/install.sh | sh -s -- --version 0.0.1
```

Add the binaries to your current shell's PATH and check the installation:

```sh
export PATH="$HOME/.local/bin:$PATH"
fugato --version
fugato-mcp --version
```

<details>
<summary>Inspect the installer before running it</summary>

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/fugatolabs/fugato/releases/download/v0.0.1/install.sh -o install-fugato.sh
less install-fugato.sh
sh install-fugato.sh --version 0.0.1
```

</details>

The installer verifies the matching archive with the release's SHA-256 manifest.
It installs both binaries in `~/.local/bin` and retains the agent setup helper,
workflow skill, instructions, and license notices in `~/.local/share/fugato/0.0.1`. Use `--bin-dir DIR` and
`--share-dir DIR` to choose other locations. It does not start a daemon or change
Cursor settings. Download checksums detect corruption; the binaries and checksum
manifest are provided by the same release publisher. Assets are unsigned and
macOS binaries are not notarized.

The `export` command updates the current shell. Add the same PATH entry to your
shell startup file if needed for new terminals, and restart your editor to pick
up the changed environment. Version 0.0.1 does not promise compatibility of
configuration or saved state across future releases.

## Homebrew

Install through the Fugato tap:

```sh
brew install fugatolabs/tap/fugato
fugato --version
fugato-mcp --version
```

The formula installs both binaries, the workflow skill, the agent setup helper,
and license notices. It does not install project dependencies, start workloads, or
configure your editor or agent. Maintainers can follow the
[Homebrew publication steps](../distribution.md#publish-the-homebrew-tap).

## Connect your agent or editor

After installation, [connect your agent](agent-setup.md) with the skill and MCP,
then use the [agent quickstart](agent-quickstart.md) to configure the project.
An agent with terminal access can also use the CLI directly.

To install the optional extension, download `fugato-0.0.1.vsix` from the same
release and run `cursor --install-extension /absolute/path/fugato-0.0.1.vsix`, or
use Cursor's **Extensions: Install from VSIX** command. Set
`fugato.executablePath` to the absolute installed CLI path if Cursor does not
inherit your shell PATH. See the [editor guide](editor-extension.md) and
[MCP connection instructions](../reference/mcp.md). The MCP executable needs a
running daemon and its local connection settings; CLI installation alone does
not register it with an agent.

## Set up this checkout for Cursor

On a macOS or Linux machine with the Go toolchain from
[`go.mod`](../../packages/fugato/go.mod), Node.js 22+, npm, and the `cursor` command,
run this from the source checkout:

```sh
./scripts/install-local.sh --project /absolute/path/to/your-project
```

The script builds `fugato` and `fugato-mcp` into `~/.local/bin`, installs the
Cursor VSIX, sets the extension's user-level connection settings, and registers
the Fugato MCP server in `~/.cursor/mcp.json` without removing other servers.
Its MCP launcher starts the daemon on demand before opening the stdio server.
The script also starts the daemon once and prints a project-setup prompt to paste
into a local agent. The prompt asks the agent to inspect manifests, Compose, and development documentation,
prepare configuration, and validate it with `doctor`.
Run the script again to update the local binaries and extension from this checkout.

The daemon is part of the `fugato` binary. It does not need a Homebrew service
or a macOS LaunchAgent: the CLI, Cursor extension, and installed MCP launcher
can start it when needed. Use `--skip-extension` if you only want the CLI and
MCP registration, or `--no-start` if you do not want the installer to start the
daemon immediately. Run `./scripts/install-local.sh --help` for other options.

`fugato init` writes the initial `version: 1` configuration without overwriting
an existing file. Use repeatable `--service NAME` flags to select Compose startup
services, or edit the scaffold to declare native commands and named endpoints.
See the [environment contract](../reference/environment.md) for the syntax.
The [CLI configuration guide](cli.md) shows the bundled schema and examples.
For a runnable Compose walkthrough, use the
[Next.js, NestJS, and PostgreSQL example](../../examples/example_1/README.md).

To ask an agent to configure a project, paste a request like this after installing
the CLI:

> Inspect this worktree's existing Compose files, scripts, and development docs.
> Use `fugato schema` and `fugato example compose` (or `native`/`mixed` when they
> fit) to prepare `.fugato.yaml` using existing service names and commands.
> Make startup selection explicit, keep endpoint and browser intent grounded in
> the app, and run `fugato doctor --json` to fix configuration errors. Report the
> selected startup plan and remaining prerequisites. If I requested runtime
> verification, run `fugato up` and then a configured `fugato check NAME`.

The installer prints a setup prompt for manual use; installation does not make
an agent infer project intent or run workloads.

## Install a local archive

Use the Go toolchain from [`go.mod`](../../packages/fugato/go.mod) to build from
this source checkout. For example, create a Linux ARM64 archive:

```sh
./scripts/package-local.sh linux arm64 /tmp/fugato-packages
```

Copy the matching `fugato_0.0.1_OS_ARCH.tar.gz` to the target machine, then install both executables into a directory on your `PATH`:

```sh
sha256sum -c fugato_0.0.1_linux_arm64.tar.gz.sha256
tar -xzf fugato_0.0.1_linux_arm64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 755 fugato_0.0.1_linux_arm64/fugato fugato_0.0.1_linux_arm64/fugato-mcp "$HOME/.local/bin/"
mkdir -p "$HOME/.local/share/fugato/0.0.1"
cp -R fugato_0.0.1_linux_arm64/LICENSE fugato_0.0.1_linux_arm64/NOTICE \
  fugato_0.0.1_linux_arm64/THIRD_PARTY_NOTICES.md fugato_0.0.1_linux_arm64/third_party \
  fugato_0.0.1_linux_arm64/INSTALL.md "$HOME/.local/share/fugato/0.0.1/"
export PATH="$HOME/.local/bin:$PATH"
```

Use the matching archive name for macOS or AMD64. The accompanying `.sha256` file records the archive checksum; run `sha256sum -c FILE.sha256` on Linux or `shasum -a 256 -c FILE.sha256` on macOS before extracting it.

## Optional CLI walkthrough

For guided project setup, start with the [agent quickstart](agent-quickstart.md).
The equivalent manual lifecycle is below; prepare `.fugato.yaml` first when the
project needs explicit startup commands, endpoints, or checks.

From the project's directory inside a Git worktree:

```sh
fugato doctor
fugato up
fugato ports
fugato down
```

`doctor` validates the selected environment without starting workloads. Without a Fugato config, Compose projects select unprofiled services and dependencies but infer no browser endpoints. An explicit config controls startup, commands, endpoints, and checks. Browser verification is optional and requires `browser.service`.

`up` starts the configured workloads and reports the startup plan and available
previews. When startup is empty, it reports that no workloads were selected.
`ports` shows current loopback bindings; container ports stay unchanged. `down`
stops workloads while retaining named-volume and native project data. See the
[Compose walkthrough](../../examples/example_1/README.md) for a complete
web, API, and database project.

Fugato preserves resource limits declared for each service in Compose. For local Docker, set `mem_limit` and `cpus` on a service when you need caps, for example:

```yaml
services:
  web:
    mem_limit: 512m
    cpus: 1.5
```

`doctor` reports services with no declared memory or CPU limit as information. It does not block `up` or impose a default cap.

See [local resource use and cleanup](local-resources.md) to inspect running
containers and retained volume data.

Each archive includes Fugato's Apache-2.0 `LICENSE`, attribution in `NOTICE`,
and a concrete dependency inventory in `THIRD_PARTY_NOTICES.md` with full
upstream license texts in `third_party/`. Preserve them when redistributing.
The [distribution guide](../distribution.md) describes release assembly and the
remaining publication steps.

## Updating a development build

An obsolete prerelease registry is rejected rather than migrated. If the daemon
reports that error, choose a fresh `FUGATO_STATE_DIR` and retain the old directory
and project data. All new project configuration uses the expanded `version: 1`
schema.

## Connect your coding agent

Release archives include `setup-agent.mjs` and the complete `skills/fugato-workflow`
directory alongside both binaries. The release installer copies these support
files to `~/.local/share/fugato/0.0.1` by default. Use Node.js 22.20+ to run:

```sh
node "$HOME/.local/share/fugato/0.0.1/setup-agent.mjs" --agent codex --project .
```

Run from your application's project directory. Choose `codex`, `claude-code`,
`cursor`, `github-copilot` (VS Code), or `gemini-cli`. This installs the skill,
persistent project guidance, and MCP configuration. See [agent setup](agent-setup.md)
for source/archive installation, custom paths, and connection checks. The
binaries and MCP launcher do not require Node.js after setup.
