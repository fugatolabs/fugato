# Fugato downloads and distribution

Fugato gives each Git worktree a local development environment. Run existing
native commands, Docker Compose services, or both; keep preview ports and
managed data separate while switching between branches. Use the CLI directly,
connect an agent through local stdio MCP, or add the optional Cursor extension.

Version 0.0.1 is for local development on macOS and Linux. Configuration and
saved state may change between releases. Native applications must use the assigned port and data
variables to obtain isolation. Docker is needed only for Compose workloads.

## Get Fugato

Install v0.0.1 from [fugatolabs/fugato](https://github.com/fugatolabs/fugato)
with the one-line installer, download a release archive, or use the Fugato
Homebrew tap. See the [installation guide](guides/install.md) for commands
and source-build instructions.

| System | CPU | Release archive |
| --- | --- | --- |
| macOS | Apple Silicon (ARM64) | `fugato_0.0.1_darwin_arm64.tar.gz` |
| macOS | Intel (AMD64) | `fugato_0.0.1_darwin_amd64.tar.gz` |
| Linux | ARM64 | `fugato_0.0.1_linux_arm64.tar.gz` |
| Linux | AMD64 | `fugato_0.0.1_linux_amd64.tar.gz` |

The [GitHub releases page](https://github.com/fugatolabs/fugato/releases)
provides these archives, the installer, checksum manifest, and optional VSIX.

Each archive contains `fugato` (including its local daemon), `fugato-mcp`,
the workflow skill, installation instructions, and complete license notices.
Agent setup is built into the CLI as `fugato setup`. No Go installation is
required on the target machine. These are build targets; cross-compilation by
itself does not establish runtime compatibility on every target.

The [download installer](https://github.com/fugatolabs/fugato/releases/download/v0.0.1/install.sh)
selects your platform, checks the archive against
[checksums.txt](https://github.com/fugatolabs/fugato/releases/download/v0.0.1/checksums.txt),
and installs both executables into `~/.local/bin`. Instructions and licenses are
retained under `~/.local/share/fugato/0.0.1`. The installer does not start
workloads or modify editor configuration. See the [installation guide](guides/install.md)
for commands and manual installation.

The optional [Cursor VSIX](https://github.com/fugatolabs/fugato/releases/download/v0.0.1/fugato-0.0.1.vsix)
provides a worktree list, runtime controls, logs, and preview links. Install the
CLI first and follow the [editor guide](guides/editor-extension.md). There is
no Marketplace or Open VSX publication in this release path. The local extension
identifier is `fugato-local.fugato`; it does not imply a registered Marketplace
publisher. Packages are unsigned; macOS binaries are not notarized.

## Start a project

After installation, open the project in your AI coding agent and follow the
[agent quickstart](guides/agent-quickstart.md). It provides a prompt to inspect
the existing project, prepare `.fugato.yaml`, and validate it with `fugato doctor`
before starting selected workloads. Use `fugato down` to stop them and retain
named-volume and native project data. For manual setup, see [configuration](guides/cli.md),
[project preparation](guides/preparation.md), [MCP](reference/mcp.md), and
[cleanup](guides/local-resources.md).

Fugato is licensed under [Apache-2.0](../LICENSE). The
[third-party inventory](../THIRD_PARTY_NOTICES.md) records the specific dependency
versions and the license files shipped with the binaries.

## Maintainer release path

Install the pinned Python test dependencies with `make test-deps`, and use
`make check` and `make vulnerabilities` before releasing. Install GoReleaser
v2.19.0, then assemble local snapshots without publishing:

```sh
WITH_EXTENSION=1 make release-snapshot
```

GoReleaser owns the four-platform build matrix, archive layout and checksum
manifest. Its checked-in configuration calls `make release-prepare` to verify
generated skill/license files, match runtime and extension versions, pin
`install.sh`, and export `docs.json`. Set `WITH_EXTENSION=1` to package the
optional VSIX; Node.js 22+ and npm are needed only for that extension build.
Local snapshots may contain uncommitted changes and are not release evidence.
Inspect `dist/` for archives and staged extras under `.release/`.

The documentation bundle contains CLI help, configuration schema, examples,
and guides from the same source. The website verifies release documentation's
version and checksum. Use `make notices` after changing dependencies/toolchains,
and `make skill` after changing canonical skill documentation.

CI runs the same commands. The release workflow assembles the assets, installs
and tests those exact archives through the acceptance suite, then creates a
**draft prerelease** only for a matching version tag. A maintainer reviews the
results and explicitly publishes the draft. A manual workflow run produces
reviewable artifacts without publishing a release. No task here changes
repository visibility or deploys the website.

## Publish the Homebrew tap

The tap repository is `fugatolabs/homebrew-tap`, addressed by users as
`fugatolabs/tap`. This separate repository contains `Formula/fugato.rb` and
uses the same versioned GitHub binaries as the download installer.

### Launch setup

The source repository and tap must both be public for anonymous Homebrew
downloads. A private source repository's release assets are also private.
Creating the formula does not make them publicly accessible.

1. Create `fugatolabs/homebrew-tap` as a public repository with an initial README:

   ```sh
   gh repo create fugatolabs/homebrew-tap --public --add-readme \
     --description 'Homebrew formulae for Fugato'
   ```

2. Add a fine-grained GitHub token as the `HOMEBREW_TAP_TOKEN` Actions secret in
   `fugatolabs/fugato`. Limit its repository access to `fugatolabs/homebrew-tap`
   and grant **Contents: read and write**. The release workflow's built-in token
   cannot write to a separate repository. Do not put tokens in formulae or files.
3. Merge the Homebrew workflow into the source repository's default branch
   before creating the launch tag, so the tagged commit includes the workflow.
   Build and review the launch assets from that clean commit, make the
   source repository public when ready, then publish the draft GitHub release.
   A published prerelease is supported too.

The [Homebrew workflow](../.github/workflows/homebrew.yml) downloads the published
archives, checks their SHA-256 hashes, and generates the formula. It installs
the formula from a temporary tap on macOS ARM64, macOS Intel, Linux ARM64, and
Linux AMD64, runs `brew test` and `brew audit --strict`, and checks the bundled
skill, native setup command, and notices. Only after all four installations pass does
it commit `Formula/fugato.rb` to the public tap. Failed checks leave the tap
unchanged. Generator regression tests also run on relevant pull requests.

To retry publication after fixing credentials or tap setup:

```sh
gh workflow run homebrew.yml --repo fugatolabs/fugato -f tag=v0.0.1
```

Check that the workflow passed and run the advertised install command on a
clean machine before opening the launch event. The first successful tap update
enables `brew install fugatolabs/tap/fugato`; later releases use the same process.
Keep release archives immutable so existing formula checksums remain valid.

### Manual fallback

1. Publish and verify the GitHub release assets first. Keep the four archives
   and their `checksums.txt` together in a local directory.
2. Generate a formula from those exact archives:

   ```sh
   python3 tools/release/homebrew.py /tmp/fugato-0.0.1 --version 0.0.1
   ```

   The script validates all four archive hashes against the manifest, then
   prints a formula. Redirect its output to `Formula/fugato.rb` inside the tap
   checkout. It does not upload files or substitute invented checksums.
3. Review the formula, then test installation and `brew test` through the local
   tap on the macOS and Linux targets before advertising support. Check both
   executable versions and that license notices are retained. Run Homebrew's
   formula style and audit checks as described in its maintainer documentation.
4. Publish the tap repository after review. Verify
   `brew install fugatolabs/tap/fugato` from a clean machine before publishing
   the website. User-facing installation copy describes the released version.

The formula installs both binaries, the skill and notices; setup is embedded in the CLI. Docker and project-specific
tools remain separate requirements; it does not register MCP or an editor
extension. New releases require an updated formula generated from the newly
published assets. Users can then update through `brew upgrade`.

See Homebrew's [tap guide](https://docs.brew.sh/How-to-Create-and-Maintain-a-Tap)
and [formula cookbook](https://docs.brew.sh/Formula-Cookbook) for the maintained
publication and testing process.
