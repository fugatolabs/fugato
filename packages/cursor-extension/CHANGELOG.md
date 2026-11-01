# Changelog

## Unreleased — 0.0.1

Changes in the current source and locally packaged VSIX. This version has not
been published to an extension marketplace.

- Explain blocked worktree removal with the local filenames, preservation guidance, and an Open folder action.
- Shorten action labels and state hints; omit ellipses from removal actions while retaining confirmation dialogs.

- Default to one environment per worktree; reserve Create for an empty worktree and select a project during initial setup when several are configured. Preserve management of existing multi-environment registrations.
- Use tabs as the only Logs/Configuration navigation. Keep Start or Stop/Restart in the toolbar and published preview links beside services. Add a setup Cancel action and keep Create after configuration review.
- Show labelled Destroy environment and linked-only Remove worktree controls with exact-target confirmation and explained removal restrictions. Retain volumes/data and support environment cleanup without project discovery.
- Show project checks by name, explain disabled workload prerequisites, and display Passed/Failed results inline. Keep independent checks runnable while stopped and reserve setup instructions for setup failures.
- Name configuration validation Validate configuration.
- Navigate worktrees in a themed sidebar and manage the selection in one reusable editor page, with dimmed IDs and theme-aware controls.
- Keep selection, disclosure, scroll and focus stable through quiet, incremental updates; validate actions in the extension host.
- Organize environments by repository and worktree, mark the current folder, and show environment IDs in dimmed descriptions.
- Prioritize environment lifecycle actions and live services/ports; separate configuration into its tab and collapse advanced setup candidates and unavailable worktrees.
- Show published addresses with open/copy actions and internal container ports as dimmed text using read-only daemon inventory.
- Keep unchanged background polling silent without tree redraws or a changing refresh banner.
- Validate local-file preparation declarations, exclusions, and default-filter exceptions in `.fugato.yaml`.
- Selecting a worktree opens its management page without starting a daemon or workloads.
- Discover multiple Compose/native projects and unconfigured manifest candidates.
- Edit version 1 environment configuration with a shared YAML schema and refresh after external edits.
- Add setup checks, ports, named tasks/checks, interactive terminals, and explicit browser endpoint selection.
- Give the worktree manager its own Fugato Activity Bar entry.
- Discover repository worktrees and select individual Fugato projects.
- Start and stop local runtimes; open or copy preview links for manual browser testing.
- Show bounded logs and shared HTTP/browser verification reports and screenshots.
- Validate local credentials, runtime identities and artifacts; retain stale state during daemon outages.
