# Example 2 — Web + PostgreSQL

This is the smaller Node web app with PostgreSQL, live source reload, and an optional Playwright browser test. It supports adding, completing, and deleting tasks. For the Next.js + NestJS + PostgreSQL walkthrough, start with [example 1](../example_1/README.md).

## Manual Fugato test

From this folder, with Docker running and Fugato installed:

```sh
fugato doctor
fugato up
fugato check ready
fugato open --endpoint website
```

Add, complete, and delete tasks in the browser. Refresh to check persistence.
Run `fugato down` to stop; data is retained. No MCP or AI agent is required.
The browser automation and alternate Docker setups below are optional.

With Fugato installed, run `fugato doctor` and `fugato up` from this directory. The checked-in `.fugato.yaml` selects `web` and its PostgreSQL dependency, declares the `website` browser endpoint and `ready` HTTP check, and selects the optional browser runner. Run `fugato check ready` to check `/health`. Without this file, Compose services still start, but no browser endpoint or HTTP check is inferred. `fugato ports` shows the current host bindings. `fugato down` stops the stack and retains its PostgreSQL data.

Run `npm ci` in this directory before using Fugato's `verify_browser` tool. This optional step requires host Node.js and npm. The Playwright image supplies browser binaries, while the example's local `node_modules` supplies the `playwright` package.

## Shared Docker runtime

From the public repository root:

```sh
cd examples/example_2
npm ci
docker compose -p fugato-example-shared up --build -d --wait web
docker compose -p fugato-example-shared port web 3000
docker compose -p fugato-example-shared --profile test run --rm browser
docker compose -p fugato-example-shared down
```

Open the loopback address printed by `port`. Docker assigns the port, so separate Compose project names can run without editing configuration. `down` keeps the PostgreSQL volume; `down -v` deletes test data.

## Dedicated Lima/VZ guest

This is a manual Docker Compose comparison, not a Fugato-managed VM backend.
When running two Docker engines, check host-port forwarding for collisions.

On macOS, from the public repository root:

```sh
cd examples/example_2
npm ci
limactl start --name=fugato-example --vm-type=vz --mount-type=virtiofs --mount-inotify --mount-only "$PWD:w" --cpus=2 --memory=4 --disk=20 --tty=false template:docker
limactl shell --workdir "$PWD" fugato-example -- docker compose -p fugato-example-vm up --build -d --wait web
limactl shell --workdir "$PWD" fugato-example -- docker compose -p fugato-example-vm --profile test run --rm browser
limactl shell --workdir "$PWD" fugato-example -- docker compose -p fugato-example-vm down
limactl stop fugato-example
```

The VM keeps its disk and PostgreSQL volume after `stop`; `limactl delete fugato-example` removes the VM and its data. Check your installed Lima version's support for `--mount-inotify` before using it for Node's file watcher in the guest.

## Checks to repeat for each backend

- Read `/health` and `/api/todos` through the published web port.
- Run the Playwright service; it checks create, reload, complete, and delete.
- Change `src/server.js` and `src/public/style.css` on the host. The web container should restart, and `/dev/version` should change. The page polls that endpoint and reloads on change.
- Create a task, run `docker compose down`, then `up` without `-v`. The task should remain.
- Run both project names concurrently to verify distinct PostgreSQL volumes and networks.

For startup measurement, time `docker compose up --build -d --wait web` after a clean `down`, then time `--profile test run --rm browser`. Record whether images and npm packages were cached. `docker stats --no-stream` reports container memory; inspect the Lima VM process and host memory pressure separately for guest overhead.
