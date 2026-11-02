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

## Install from this checkout

Use the Go toolchain from [`go.mod`](../../packages/fugato/go.mod) and run:

```sh
make install-dev
fugato setup --agent cursor --project /absolute/path/to/your-project
```

`make install-dev` installs both binaries into `~/.local/bin`. Override the
prefix with `make install-dev PREFIX=/your/prefix`. Add its `bin` directory to
PATH before running setup. Setup writes project-scoped agent configuration; it
does not modify global Cursor settings or start the daemon or workloads.

To build and install the optional Cursor extension, use Node.js 22+, npm, and
the `cursor` command:

```sh
make extension-install
```

Configure the extension's user-level `fugato.executablePath` if the editor cannot
find the installed CLI. Non-default daemon state directories and ports also
need matching extension user settings. See the [editor guide](editor-extension.md).
The old source installer has been replaced by these explicit steps; existing
global Cursor MCP entries are left for you to review rather than silently changed.

The daemon is part of the `fugato` binary. The CLI, extension, and generated MCP
launcher can start it on demand; no always-on system service is required.

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

Use the [agent quickstart](agent-quickstart.md) to describe project intent.
Installation and agent connection do not start application workloads.

## Install a local archive

Use the Go toolchain from [`go.mod`](../../packages/fugato/go.mod) to build from
this source checkout and GoReleaser v2.19.0. Assemble the four platform archives:

```sh
make release-snapshot
cd dist
```

Copy the matching `fugato_0.0.1_OS_ARCH.tar.gz` to the target machine, then install both executables into a directory on your `PATH`:

```sh
grep '  fugato_0.0.1_linux_arm64.tar.gz$' checksums.txt | sha256sum -c -
tar -xzf fugato_0.0.1_linux_arm64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 755 fugato_0.0.1_linux_arm64/fugato fugato_0.0.1_linux_arm64/fugato-mcp "$HOME/.local/bin/"
mkdir -p "$HOME/.local/share/fugato/0.0.1"
cp -R fugato_0.0.1_linux_arm64/LICENSE fugato_0.0.1_linux_arm64/NOTICE \
  fugato_0.0.1_linux_arm64/THIRD_PARTY_NOTICES.md fugato_0.0.1_linux_arm64/third_party \
  fugato_0.0.1_linux_arm64/INSTALL.md "$HOME/.local/share/fugato/0.0.1/"
export PATH="$HOME/.local/bin:$PATH"
```

Use the matching archive name for macOS or AMD64. The accompanying
`checksums.txt` records all archive hashes; select the matching filename and
verify it with `sha256sum -c -` on Linux or `shasum -a 256 -c -` on macOS before
extracting. The download installer performs this selection and verification for you.

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

The CLI embeds the complete workflow skill and configures the selected agent:

```sh
fugato setup --agent codex --project .
```

Run from your application's project directory. Choose `codex`, `claude-code`,
`cursor`, `github-copilot` (VS Code), or `gemini-cli`. This installs the skill,
persistent project guidance, and MCP configuration. Add `--dry-run` to inspect
changes or `--bin-dir /path/to/bin` for a custom installation. See
[agent setup](agent-setup.md) for conflict handling and connection checks.
No Node.js installation is required for setup or the MCP launcher.
