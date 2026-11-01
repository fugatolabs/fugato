# Local resource use and cleanup

For Compose workloads, Fugato gives each worktree project a separate Compose
project on the configured Docker Engine. The Engine's CPU, memory, and disk
capacity is shared with every other Docker project on the host. Fugato does not
reserve a share of that capacity or enforce a host-wide CPU or memory budget.
Starting a new environment is refused once the daemon retains 100 environment
owners in memory, including stopped environments.
Destroy an unused environment registration to release its owner. This count
limit does not establish that the host can run 100 workloads at once.

## Set limits for a service

Compose is the source of truth for service limits. Add `cpus` and `mem_limit`
to a service in your project's Compose file when it needs a cap:

```yaml
services:
  web:
    cpus: 1.5
    mem_limit: 512m
```

`fugato doctor` reports a missing CPU or memory limit as information. It does
not block startup. Set limits based on the application: a cap that is too low
can prevent the web service or database from becoming healthy.

## Check what is running

`fugato ls` lists Fugato's registered runtimes and their states. A worktree can
contain more than one runtime when it has several configured projects. To inspect
the Docker Engine's available CPU and memory, then live container use, copy a
runtime ID from that output and run:

```sh
docker info --format 'Docker CPUs: {{.NCPU}}; memory bytes: {{.MemTotal}}'
docker ps --filter label=com.docker.compose.project=fugato-RUNTIME_ID
docker stats --no-stream
```

Match the container names from `docker ps` with the `docker stats` output.
Docker reports CPU and memory for containers that are running now. Check all
Fugato runtime IDs and other Docker projects when judging total host use.
These samples are observations, not reservations or peak-use guarantees.

## Retained data and explicit removal

`fugato down --worktree PATH` removes the selected project's containers and networks,
but keeps its named volumes so the next `up` can recover database data. A
failed or cancelled `up` also removes partial containers while keeping named
volumes. Fugato never deletes retained volume data automatically.

To see the volumes owned by one runtime, replace `RUNTIME_ID` with the ID from
`fugato ls`:

```sh
docker volume ls --filter label=com.docker.compose.project=fugato-RUNTIME_ID
docker system df -v
```

`docker system df -v` shows disk use across the whole Engine. To remove one
runtime and forget its registry entry, use:

```sh
fugato destroy --worktree PATH
```

This removes the runtime's containers and networks while retaining its named
volumes and native `FUGATO_DATA_DIR`. To permanently remove that data as well,
choose the explicit data-deletion flag instead:

```sh
fugato destroy --worktree PATH --volumes
```

If the worktree was deleted or its configuration no longer loads, use the ID
from `fugato ls`; cleanup does not execute the worktree's current configuration:

```sh
fugato destroy --runtime RUNTIME_ID --volumes
```

Cleanup checks the saved runtime identity and Docker's Compose ownership labels.
It removes only the selected runtime's containers, networks, and, when requested,
named volumes. Native deletion is confined to the daemon's `data/RUNTIME_ID`
directory and rejects symlink replacements. Arbitrary files written by host
commands, source worktrees, bind-mounted files, images, and build cache remain.
Docker must be available for a runtime that has used Compose. Resources with
inconsistent ownership are refused; a failed cleanup keeps the registry entry
with stopped desired state and an error so the command can be retried deliberately.
A resource already removed before a later failure is not restored.

Browser verification screenshots are stored separately under
`FUGATO_STATE_DIR/artifacts/RUNTIME_ID`. They have no automatic expiry and are
not removed by `destroy`, including `destroy --volumes`. Review and remove
unneeded screenshot files separately; they may contain application data.

`destroy` without `--volumes` forgets the runtime, so later data deletion requires
registering the same project again with `up`, then running `destroy --volumes`.
A later `up` at the same repository/project identity reuses retained volumes;
after `--volumes`, it creates empty replacements. Images and cache may be shared
with other environments; broad Docker pruning is not runtime-scoped cleanup.

This local policy leaves the host-wide running-worktree and retained-disk
budget to the developer and their Docker Engine configuration. Native processes
also share host resources and need application-specific resource controls.
