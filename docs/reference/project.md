# Local project contract

Fugato runs Compose services, native commands, and mixed environments in Git
worktrees. The [version 1 environment configuration](environment.md) defines
startup, named endpoints, commands, and checks. The CLI, daemon, and editor
extension use the same contract.

```sh
fugato projects --json
fugato doctor --json
fugato schema
fugato example compose
fugato up
fugato ports
fugato down
```

`projects` discovers projects and candidates without starting workloads. `doctor`
validates setup; its JSON report includes `schema_version`, `ok`, project details,
selection sources, startup plan, and findings. The plan lists selected Compose
roots and ordered commands, dependencies, and services left unselected. It is
configuration, not live state or a verification result. An empty selection is
valid and `up` reports that no workloads were started. Blocking findings produce a nonzero exit status
while stdout remains valid JSON. `up` starts the daemon on demand and runs the
selected startup services and commands. Named checks run only when requested.
`down` stops workloads and retains project data. Failed startup cleans up resources
started by that attempt and retains data for a retry.

`fugato --help` and `fugato help COMMAND` show usage without starting the daemon.
Use `--worktree PATH` to select a directory inside the desired project.

## Discovery and configuration

Fugato searches upward from the requested directory to the Git worktree root for
the nearest `.fugato.yaml` or conventional Compose file. Compose remains the source
of truth for containers, dependencies, health checks, networks, and volumes.
Standard Compose base and override files are discovered together. Shell
`COMPOSE_FILE` is ignored so the CLI and daemon resolve the same project.

Without `.fugato.yaml`, Compose projects start all unprofiled services and their
dependencies. No browser endpoint is inferred from ports or service names.
`fugato init` saves those startup choices; repeat `--service NAME` to choose
specific roots, or use `--compose FILE` for a nonstandard filename. Native-only
projects receive an empty scaffold. Existing configuration is never overwritten.
The native scaffold includes commented placement examples while leaving startup
empty. `fugato example native|compose|mixed` prints complete configurations to
adapt to existing commands and services. `fugato schema` prints the exact schema
bundled with the installed CLI; the source reference is [fugato.schema.json](fugato.schema.json).
An explicit config makes `start` authoritative, including an empty selection.

```yaml
version: 1
start:
  services: [web]
endpoints:
  website:
    service: web
    port: 3000
    protocol: http
    browser: true
checks:
  ready:
    http:
      endpoint: website
      path: /health
browser:
  service: browser
```

Here, Compose starts `web` and its dependencies. `fugato open --endpoint website`
opens its declared preview; `fugato check ready` probes `/health`. The optional
`browser.service` selects a separate Compose runner for automated browser checks.
The runner's profile is inferred only when exactly one exists; otherwise select
`browser.profile` explicitly.

Adding or removing `.fugato.yaml` in the same project directory does not change
the runtime ID. A worktree can contain several projects, each with its own config.
Bare manifests and local documentation help an agent propose configuration;
discovery never executes their commands. See the [agent prompt](environment.md#prompt-for-the-users-agent).

## Ports and retained data

Fugato assigns dynamic `127.0.0.1` host bindings to published Compose TCP/UDP ports
and explicitly declared endpoint ports in the selected dependency closure.
Container ports, service addresses such as `db:5432`, and source Compose files
stay unchanged. `fugato ports` reports current bindings. Named HTTP previews use
`http://<endpoint>.<runtime-id>.localhost:<proxy-port>/`; direct host ports may
change after restart.

Compose project names, ordinary named volumes, and native data directories are
separate for each worktree project. Native commands must honor the configured
port variables and `FUGATO_DATA_DIR` for application-level separation. Host
commands have the user's permissions; shared Docker is not a security boundary
between mutually untrusted workloads.

## Compose validation

Before execution, selected services, their dependencies, explicitly configured
command targets, and the browser runner are validated. Mutable volumes and
networks must be project-scoped. Bind mounts, build contexts, and Dockerfiles may
use sibling directories within the same Git worktree. Host networking, external
or explicitly shared volumes/networks, fixed `container_name`, privileged or
host-access settings, outside-worktree mounts, and duplicate publication of a
container port/protocol are rejected.

| Finding | Meaning |
| --- | --- |
| `PORT_REMAP` | A source host port becomes a dynamic loopback binding. |
| `PORT_LAN_BINDING_CHANGED` | An all-interface or LAN binding becomes loopback-only. |
| `COMPOSE_HOST_NETWORK` | Use a project-scoped Compose network. |
| `COMPOSE_EXTERNAL_VOLUME` | A mutable volume cannot be shared across worktrees. |
| `PORT_DUPLICATE` | Remove duplicate publication of a container port/protocol. |
| `RESOURCE_LIMIT_UNSET` | Informational: the service has no CPU or memory cap. |
| `PROJECT_READY` | Static validation passed; runtime checks remain separate. |

Fugato preserves Compose CPU/memory limits and does not impose default container
caps. See the [install guide](../guides/install.md), [resource guide](../guides/local-resources.md),
and [MCP reference](mcp.md).
