# Use Fugato in VS Code and Cursor

The Fugato extension's **Worktrees** sidebar groups open repositories and their
Git worktrees, labeled by branch, path, and environment status. Each worktree
normally has one environment. Existing registrations with several environments
remain accessible through environment rows and the selector. The current folder is marked;
unavailable worktrees are collapsed into a separate group. Discovery candidates
stay inside **Create environment**.

Selecting a worktree or environment opens one reusable management page in the
main editor area. It remembers the selected environment for each worktree and
provides **Overview**, **Logs**, and **Configuration** tabs. With no environment,
choose **Create environment**. Both surfaces follow the editor's theme. Selection
never starts the daemon or workloads. Compose and native projects use the same
interface; browser previews are optional.

The extension uses the CLI for discovery and setup checks and the local daemon
for runtime state and actions. It opens declared previews in your normal browser,
where your browser profile, extensions, and cookies are available.

## Prerequisites

- macOS or Linux with Git. Compose projects also need Docker Engine and Compose;
  native projects need the executables named in their configuration.
- The Fugato CLI and daemon installed locally. Follow the [Fugato install guide](install.md), then run `fugato doctor` from the selected project to check its setup.
- A trusted local folder opened in VS Code or Cursor. Fugato executes project workloads, so the extension does not support untrusted workspaces, remote editor sessions, or Windows in this release.

The extension connects to the local daemon and uses the configured Fugato state directory and API port. If the editor was launched from the desktop, it may not inherit `FUGATO_STATE_DIR` or `FUGATO_API_PORT` from your shell. Set the extension's user-level settings when your daemon uses non-default values. Do not put daemon credentials in workspace settings.

## Install a local VSIX

When working from this checkout, `./scripts/install-local.sh` builds and installs
the VSIX, sets the Cursor user settings, and registers the separate Fugato MCP
server. See the [install guide](install.md). The manual VSIX steps below remain
available when installing an artifact from CI.

The extension is distributed for local evaluation as a VSIX artifact from the **Extension** workflow. Open a successful workflow run, download its Fugato VSIX artifact, and extract it. The workflow does not publish to an extension marketplace.

In VS Code, open the Extensions view, select its `...` menu, choose **Install from VSIX...**, and select the `.vsix` file. In Cursor, use the same **Install from VSIX...** action in its Extensions view. You can also install from a terminal with the editor's CLI:

```sh
code --install-extension ./fugato-<version>.vsix
```

For Cursor, use `cursor --install-extension` with the same VSIX path if the Cursor command is on your `PATH`.

## Start and test a worktree

1. Open the repository folder in VS Code or Cursor and trust the workspace.
2. Select **Fugato** in the Activity Bar and click a worktree. The management page
   opens its remembered or only environment. With several, use the environment
   selector or sidebar children.
3. If the daemon is offline, use **Start Daemon** before creating an environment.
   With no environment, choose **Create environment**. When several configured
   projects are available, choose which one to use; a single configured project
   is selected automatically. The directory picker remains available during
   initial setup. Unconfigured manifest
   candidates appear under **Advanced**. Review configuration, then choose
   **Create**. Creation validates and registers the environment and leaves workloads
   stopped. It does not copy local inputs; run `fugato prepare` explicitly when
   the project needs files from another checkout. **Validate configuration** runs
   the shared validator; **Edit configuration**
   opens `.fugato.yaml`, or an unsaved version 1 template when it is missing.
   **Cancel** leaves setup. Once an environment is registered, the page offers
   its controls instead of another creation action. YAML schema completion and
   validation require an editor YAML extension.
4. Explicitly **Start** the created environment.
   The toolbar contains **Start** when stopped, or **Stop** and **Restart** while running.
   **Logs** and **Configuration** use tabs without duplicate navigation buttons.
   The **Logs** tab selects all services or an individual Compose
   service/native command and refreshes recent output.
5. Open a declared browser preview from its published service link. API-only,
   worker, and task projects do not need a preview.
6. Running environments automatically show **Services & ports**, including
   dimmed internal container ports and published local addresses. Click an HTTP(S)
   address to open it, or use **Copy Service Address** beside a published port.
   Use **Configuration** for project paths, relative configuration filenames, and
   **Validate configuration**. Overview lists project checks by name. Checks with
   known workload prerequisites are disabled while those prerequisites are
   stopped, with an inline explanation; independent checks remain runnable.
   Results appear inline as **Passed** or **Failed**. A failed project check does
   not offer setup instructions. Overview also contains tasks and a **Verification**
   disclosure. Inspect logs, run checks, and **Stop** when finished.
   Interactive commands use an integrated terminal; stopping retains data.
7. To remove an environment registration and its owned containers/networks, use
   the labelled **Destroy environment** control beside that environment.
   Choose an environment if there are several, then confirm its ID and
   paths. Volumes and native data are retained, as with `fugato destroy` without
   `--volumes`. Missing folders and broken configuration do not block cleanup.
   The Git worktree folder is not deleted.
   Destroy requires a daemon build that includes the `fugato destroy` endpoint.

Use the sidebar's **Create worktree** button to create a linked worktree from an
existing branch or a new branch from HEAD. Choose its parent directory and folder
name. The management page header offers **Open folder** and a labelled **Remove
worktree** control for linked worktrees. Removal retains the branch, requires
destroying all registered environments first, and refuses primary or modified
worktrees. The page explains the registered-environment restriction before
removal. Missing linked worktree registrations can be removed after environment
cleanup. Both destructive controls retain confirmation of the exact target.
When local files block removal, the warning lists changed, untracked, and ignored
paths (up to eight entries), explains how to preserve them, and offers **Open
folder**. Commit or stash tracked changes and move any untracked or ignored files
you want to keep outside the worktree before trying again. Ignored files such as
`.env` may contain local configuration or secrets; Fugato does not discard them.

The controls follow the selected context:

| Context | Controls |
| --- | --- |
| Worktree header | Open folder; Remove worktree for linked worktrees, with any environment restriction explained |
| Empty worktree | Create environment; choose a project during initial setup when needed |
| Registered environment | Start or Stop and Restart; direct Destroy environment control |
| Environment tabs | Overview, Logs, Configuration |
| Overview | Services/ports with preview links, named checks with prerequisites and inline results, tasks, verification |
| Configuration | Project path, configuration files, Edit configuration, Validate configuration |

Create registers an environment; Start runs that existing environment. A stopped
environment is already created, so it offers Start. The default editor flow
creates one environment per worktree; existing multi-environment registrations
keep their selection and management controls. The worktree header remains visible for
orientation; its path is not repeated in Configuration.

Services and ports update quietly with background polling. Unchanged data does
not redraw the views. Service inventory requires an updated Fugato daemon; older
daemons show published ports and an update hint. Internal ports are the ports
declared by the running container/image, not a scan of application listeners.
Process state and Docker health are shown separately; running does not imply ready.

The HTTP preview hostname is stable across restarts, so the browser can retain
cookies for that preview. Host-only cookies stay with their hostname; broad
cookie domains can share state between previews. Check the application's cookie
configuration when testing isolation. The extension does not show or take over
an AI agent's browser session.

The extension tries to connect to an existing daemon when activated. Discovery
and setup checking remain available while it is offline. Use **Start Daemon**
explicitly before runtime actions. Starting or stopping an environment changes
the same state seen by the CLI and MCP server.

Configuration changes made in the editor or by an agent refresh project details.
See the [environment reference](../reference/environment.md) for the schema,
supported workloads, and the setup-agent prompt. Native host processes use your
permissions; port and data separation require the app to honor configured values.

## Troubleshooting

- **Worktree folder exists but Git metadata is missing:** the checkout lost its
  `.git` marker, while files remain on disk. Fugato cannot verify those files are
  clean. Preserve or move the folder, then remove its stale worktree registration.
  The branch is retained; Fugato does not force-delete the remaining files.

- **Daemon unavailable:** install the Fugato CLI, run `fugato doctor`, then use **Start Daemon** or check the configured state directory and API port.
- **Daemon update required:** the VSIX and CLI/daemon are installed separately.
  Update the installed CLI selected by the user setting `fugato.executablePath`
  (or the host PATH), restart the existing daemon, then **Refresh**. **Start
  Daemon** starts an absent daemon; it does not replace an already-running one.
  Creation, destruction, and worktree actions negotiate their own capabilities;
  unsupported controls stay unavailable while supported actions remain usable.
- **No preview link:** start the selected runtime and refresh its state. The browser link is available only for a running runtime.
- **Project needs configuration:** run `fugato doctor` in that worktree. Use `fugato init` to create a configuration, then declare startup services/commands and optional named endpoints.
- **Browser verification unavailable:** configure a browser service for the project as described in the [project reference](../reference/project.md). Manual preview in your browser does not require browser verification to be configured.

## UI review acceptance: 2026-10-23

The review VSIX was installed in Cursor on macOS, its packaged code and UI bytes
were verified against the installed extension, and the window was reloaded.
Live checks confirmed these behaviors:

- The running example shows Stop, Restart, and a direct Destroy environment
  action. Preview links stay with their services, and Add environment is absent.
- Its readiness check reports Passed in Overview without opening a JSON editor.
- The stopped example shows Start and a disabled readiness check with its
  running-endpoint prerequisite. Invoking Run Check from the command palette
  keeps this guidance inline, without an error or Setup Instructions notification.
- Destroy environment opens a confirmation with the exact ID and paths and
  explains retained data. Cancelling leaves the environment registered.
- Linked worktree removal displays its environment-cleanup prerequisite; the
  primary checkout has no removal action.

The daemon update preserved all desired states and all three running Compose
container IDs. The extension's 30 tests cover independent stopped checks, result
isolation, creation guards, and existing multiple-environment access. Full Go
tests, race tests, vet, type checks, reference synchronization, and whitespace
checks pass. These results do not certify the Linux/VS Code UI matrix.

## Acceptance record: 2026-10-22

This record covers the revision tested on that date. It does not certify the
subsequent control simplification described above.

That VSIX was installed and the test window reloaded. It was exercised with
the installed CLI in Cursor on macOS using a disposable Git repository and real
native and Compose workloads. These checks used the editor UI:

- Native environment **Create**, **Start**, **Stop**, and **Restart**; creation
  left workloads stopped, and application data survived stop/restart.
- Per-service logs, configuration editing, setup validation, a named check and
  finite task, plus the unsaved configuration template for a directory without
  `.fugato.yaml`. An interactive command opened a real Node.js 20 REPL in the
  integrated terminal; a console statement ran and `.exit` closed the session.
- HTTP verification success and an HTTP 500 failure; a preview opened in the
  user's Chrome browser.
- Running, healthy Compose services with published addresses and dimmed internal
  ports; browser verification passed and its screenshot opened in the editor.
- Reuse of one management tab, remembered environment selection, and navigation
  between multiple environments in one worktree.
- Cursor checks confirmed that setup Cancel restores the selected
  environment, the Logs tab loads output, and arrow keys switch tabs. Configuration
  shows filenames relative to the project and omits the repeated worktree path.
- Linked worktree creation from both a new branch and an existing branch, plus
  **Open folder**; clean removal retained the branch. Removal refused registered
  environments and untracked files. After a dirty removal failed, the **Remove
  worktree** control became available again while the error notification remained
  visible.
- A moved worktree appeared under **Unavailable worktrees** after refresh.
  **Destroy environment** succeeded with its folder missing, followed by removal
  of the missing worktree registration.
- Invalid configuration version 99 disabled **Start** while leaving configuration
  editable and environment destruction available.

After cleanup, no test environment registrations or Compose test containers
remained, and retained native data was still present. All four pre-existing
environment records retained their stopped desired state; an existing observed
error remained unchanged. The CRM environment remained stopped.

This record does not certify the full compatibility matrix. Linux and VS Code
still need editor acceptance checks; the macOS Cursor checks cover the fixtures
and scenarios above.
