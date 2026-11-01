# Fugato for VS Code and Cursor

Manage local Git worktree environments from the Fugato sidebar and a reusable editor page. Create an environment to register it, then explicitly start its workloads. Stop retains Compose named volumes, bind-mounted files, and native data directories. Containers are removed, so their writable-layer data is not retained.

The sidebar navigates worktrees by branch, path, and compact environment status.
The default flow creates one environment per worktree. Existing registrations
with several environments retain their sidebar rows and environment selector. Selecting
a row opens the same management editor tab. The page remembers the selected
environment for each worktree and provides Overview, Logs, and Configuration tabs.
Services show published addresses and dimmed internal ports while running. Both
surfaces follow the editor's colors and fonts in VS Code and Cursor.

## Install

Requires a local, trusted macOS or Linux workspace, VS Code 1.116 or compatible Cursor, and the Fugato CLI/daemon with the extension API. Remote, WSL, container and web extension hosts are not supported by this version. Start with the [CLI installation guide](../../docs/guides/install.md).

Build this package with Node.js 22 or newer. This requirement comes from the
VSIX packaging tool; installing the prebuilt VSIX uses the editor's existing
extension host.

```sh
npm ci
npm audit --audit-level=high
npm run check
npm test
npm run package
```

In the editor, run **Extensions: Install from VSIX** and choose `fugato-0.0.1.vsix`. This package is for local installation; its publisher is a local placeholder and it is not published to a marketplace.

## Use

1. Install the current Fugato CLI outside your project. Configure `.fugato.yaml` using existing Compose files, package scripts, or documented launch commands. Configuration version 1 supports Compose, native commands, and mixed projects.
2. Open the repository and choose **Fugato** in the Activity Bar. **Worktrees** groups repositories and their worktrees. The current folder is marked; missing worktrees are grouped separately. Selection and refresh never start a daemon or workloads. Read-only CLI discovery also works before the daemon starts. Use **Start Daemon** before creating or managing environments if it is not running. This starts the local control service; workloads still need an explicit **Start**.
3. Click a worktree to manage its remembered or only environment in the main editor. Existing multi-environment registrations retain the compact selector and sidebar children. With none, choose **Create environment**. A single configured project is selected automatically; choose a project when several are available. Initial setup includes a directory picker; unconfigured manifest candidates stay under **Advanced**. Review configuration and choose **Create**. The resulting environment stays stopped and offers **Start**. Once registered, the page manages that environment without an additional creation action. Discovery candidates never enter the sidebar.
4. Use **Validate configuration** in Configuration for `fugato doctor --json` or **Edit configuration** to open `.fugato.yaml`. Missing configurations open an unsaved version 1 template. The extension contributes a YAML schema (completion and validation require an editor YAML extension). Saving or externally editing configuration refreshes Details.
5. Choose **Start** for a created environment. The toolbar shows **Start** when stopped, or **Stop** and **Restart** while running. **Logs** and **Configuration** use tabs without duplicate toolbar buttons. The Logs tab selects all services or a specific Compose service/native command and refreshes recent output. Overview lists project checks by name with inline **Passed** or **Failed** results. Checks with known workload prerequisites are disabled while those prerequisites are stopped, with an explanation; independent checks remain runnable. Routine check failures do not offer setup instructions. Tasks and browser/HTTP verification remain available in Overview. Persistent host commands start with the environment. Host commands run with your permissions and rely on the configured port/data overrides for isolation.
6. Running environments show an expanded **Services & ports** list automatically. Published ports show their local address; internal ports appear as dimmed descriptions without a browser action. Container status and reported health remain distinct. Open a browser preview from its HTTP(S) service link or use its inline **Copy Service Address** action. Projects without browser endpoints need no preview. **Stop (keep data)** preserves the storage described above.
7. Existing **Verify HTTP**, **Verify Browser**, and **Show Latest Verification** commands remain available. Verification asks which HTTP(S) endpoint to check. Browser automation requires a Compose service endpoint and configured browser runner; native endpoints support HTTP verification and named checks. Closing the editor leaves the shared daemon and environments running.

Use the environment's labelled **Destroy environment** control to stop it, remove its owned containers/networks, and forget its registration. Confirmation shows the exact environment ID and paths. Volumes and native data are retained, matching `fugato destroy` without `--volumes`. Cleanup works with missing worktree folders or broken configuration. The worktree folder remains available.

The sidebar's **Create worktree** button creates from an existing branch or a new branch from HEAD. Choose its parent directory and folder name. The page header offers **Open folder** and a labelled **Remove worktree** control for linked worktrees. If environments are still registered, the page explains that they must be destroyed first. Removal retains the branch and refuses primary or modified worktrees. Missing linked worktree registrations can be removed after environment cleanup. Setup offers **Cancel** and places **Create** after configuration review.

| Context | Controls |
| --- | --- |
| Worktree | Open folder; Remove worktree for linked worktrees |
| Empty worktree | Create environment and initial project selection |
| Environment | Start or Stop and Restart; Destroy environment |
| Overview | Service preview links, named checks/results, tasks, verification |
| Logs / Configuration | Service logs / configuration files, editing, Validate configuration |

The status bar shows the selected worktree. Unchanged background polls send no webview updates or refresh timestamp. Changed content is patched in place to preserve focus, scroll and disclosure state. Service inventory is read-only and requires an updated daemon; older daemons show published ports with an update hint. Individual service logs require a daemon advertising service log support; otherwise the Logs tab offers aggregate output with an update hint. Internal container ports come from Docker's exposed-port metadata, not application port scanning. Command palette actions use your selection or ask you to choose.

The sidebar has a restrictive Content Security Policy and no network access.
Its page receives only presentation data and opaque action keys. The extension
host owns all command arguments, checks workspace trust, and rejects stale actions.

## Settings and connection

User settings only:

- `fugato.executablePath`: absolute installed CLI path, otherwise a host PATH entry outside the workspace.
- `fugato.stateDirectory`: absolute state directory, otherwise `FUGATO_STATE_DIR`, then `~/Library/Application Support/fugato` on macOS or `$XDG_CONFIG_HOME/fugato` / `~/.config/fugato` on Linux.
- `fugato.apiPort`: API port, otherwise `FUGATO_API_PORT`, then 46655.

GUI editors may not inherit terminal environment variables. Workspace settings cannot override these values. The extension reads the protected daemon token in its Node host and only connects to `127.0.0.1`. Preview links never receive this token.

The VSIX and CLI/daemon are installed separately. Update the installed CLI selected
by `fugato.executablePath` (or the host PATH) and restart the existing daemon after
a CLI upgrade. **Start Daemon** starts an absent daemon; it does not replace an
already-running one. **Refresh** reconnects without starting anything.

An older daemon produces **Daemon update required** for unsupported features.
Creation, destruction, and worktree controls negotiate their own capabilities;
supported environment controls remain available. Unsupported actions are blocked
before confirmation or mutation, including stale editor actions and command
palette calls. Missing token or daemon shows unavailable runtime state while
read-only project discovery remains available. Cached rows and reports are marked
stale on connection loss. Rejected requests show the daemon error. Cancelled,
timed-out, or disconnected operations are never retried; refresh to see the
eventual outcome.

Verification reports are session-only and can be evicted or lost when the daemon restarts. The report's commit is HEAD at verification start; local edits may have been included. The report cache does not delete retained screenshots. Screenshots open only after validating their PNG signature, size and runtime artifact path.

## Development and validation

`npm run test:integration` runs the official VS Code extension-host smoke test. Set `VSCODE_EXECUTABLE_PATH` to a local editor executable to avoid a download; CI uses VS Code 1.116.0. Unit tests cover credentials, transport bounds/cancellation, identities, preview URLs, screenshots, and worktree joins. Press F5 from this package to launch a development host.

Full acceptance requires both editors on macOS and Linux with real Docker workloads; automated host tests alone do not establish the full compatibility matrix. See the dated acceptance records in the [editor guide](../../docs/guides/editor-extension.md) for results.
