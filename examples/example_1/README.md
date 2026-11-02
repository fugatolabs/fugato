# Example 1 — Todo app with Next.js, NestJS, and PostgreSQL

A complete todo app for developing a feature in a separate Git worktree with
Fugato. Add tasks, mark them complete, reopen them, and delete them. PostgreSQL
stores each worktree's list independently across restarts.

Everything runs in Docker. You need the Fugato CLI on your PATH and a running
Docker Engine with Compose. No MCP connection, AI agent, extension, host Node.js,
or manual dependency installation is needed. The included `.fugato.yaml` is ready
to use; leave it as it is for this walkthrough.

## Start a feature worktree

From the public repository root (`open-source/` in the development workspace):

These commands create a new branch from the current committed `HEAD`. If you are
testing uncommitted changes to this example, commit those changes first; Git
does not copy them into the new worktree. Choose an unused branch name and
destination directory.

```sh
source_repo="$(pwd)"
source_project="$source_repo/examples/example_1"
feature_worktree="/tmp/fugato-example1-feature"

cp -n "$source_project/.env.example" "$source_project/.env"
git worktree add -b codex/todo-feature "$feature_worktree" HEAD
cd "$feature_worktree/examples/example_1"

fugato prepare --from "$source_project" --dry-run --json
fugato prepare --from "$source_project"
fugato doctor
fugato up
fugato check ready
fugato open --endpoint website
```

The original checkout is itself a Git worktree. Git creates this additional
worktree and checks out the feature branch; Fugato does not create it. The first
`cp` provides the original project's local input. Because `.env` is ignored, it
is absent from the new worktree until `fugato prepare` copies it. The dry run
should report `.env` as `would_copy`, and the next command should report `copied`.

Fugato then starts the feature worktree's own containers, network, database
volume, and preview route. Open `$feature_worktree` in your editor and make your
feature changes there. An environment already running from the original checkout
can remain running for comparison.

The first start downloads images and installs packages inside the web and API
images using `npm ci`. Keep Docker running and let `up` finish. `check ready`
should pass before you open the page. You can also copy the `website` URL from
`up` into your browser. It looks like
`http://website.<runtime-id>.localhost:46656/` with the default proxy port.

Doctor may print `Browser: unconfigured`: that refers to the optional automated
browser test runner. The manual `website` preview is already configured.

The ignored `.env` contains `EXAMPLE_GREETING`, a demonstration value consumed by
Nest and displayed by Next under **Backend says:**. `.env.example` is committed;
`.env` is not. This value is intentionally public UI text; do not use a real
secret for it. A missing `.env` blocks Compose startup until it is created or
prepared.

## Check `.env` preparation and isolation

Continue in the feature worktree from the start sequence, using the same terminal
so `$source_project` still identifies the original project. These steps check
that preparation preserves your worktree-specific changes.

1. The preview should display the greeting copied from the source `.env` under
   **Backend says:**. With the supplied template, it is **Hello from the source
   .env**. `git check-ignore .env` confirms the copied file is still ignored.

2. Repeat preparation:

   ```sh
   fugato prepare --from "$source_project"
   ```

   It should report `.env` as `existing`.

3. Change only the feature worktree's `.env` to:

   ```dotenv
   EXAMPLE_GREETING=Hello from my feature worktree
   ```

4. Run preparation again. It should report `existing` and preserve your edit.
   Then recreate this worktree's containers to inject the new environment value:

   ```sh
   fugato prepare --from "$source_project"
   fugato down
   fugato up
   fugato check ready
   ```

   Refresh the same preview URL: it should show **Hello from my feature worktree**.
   The original `.env` should retain its original value, and its running app
   should remain unchanged.

Environment-file edits need container recreation; source hot reload does not
reload Compose's injected environment. Normal `down` retains the todo database.
`git check-ignore .env` confirms the copied file remains ignored.

## Test the app by hand

1. Add “Buy milk” and “Try another worktree”. Both should appear immediately.
2. Check “Buy milk”. It should be crossed out and the counters should update.
3. Refresh the page. Both tasks and the completion state should remain.
4. Uncheck “Buy milk”, then delete it. Refresh to verify the deletion persists.
5. Keep the other task and restart from the same directory:

   ```sh
   fugato down
   fugato up
   fugato check ready
   ```

6. Refresh the preview. The remaining task should still be there.

When finished, run `fugato down`. It stops the stack and retains its database.
For troubleshooting, run `fugato logs`, `fugato ports`, and `fugato doctor` from
this directory. `fugato ls` lists runtimes from all projects.

## What runs where

```text
Browser → Fugato preview proxy → Next.js web:3000
                                  → NestJS api:3001 → PostgreSQL db:5432
```

Next serves the page and `/api/todos` routes. Nest validates requests and runs
parameterized database queries. Only the web service has a dynamically assigned
loopback host binding. `fugato ports` shows that underlying `127.0.0.1:<port>`;
use the named preview URL in the browser. The API and database communicate on
the worktree's private Compose network. The database password is for this local
example only.

## Try two worktrees

Keep the feature stack running. In the same terminal used above, return to the
original repository and create another worktree from the same baseline. Choose
another unused branch name and destination:

```sh
cd "$source_repo"
git worktree add -b codex/todo-comparison /tmp/fugato-example1-comparison HEAD
cd /tmp/fugato-example1-comparison/examples/example_1
fugato prepare --from "$source_project"
fugato doctor
fugato up
fugato check ready
fugato open --endpoint website
```

Its todo list starts empty and its preview hostname differs. Add a task there:
it should not appear in the first worktree. Stop either stack with `fugato down`
in its own directory; the other should keep working. Both containers use internal
port 3000, while Fugato assigns different host ports and database volumes.

## Editing and data

Next page edits reload automatically. Nest runs `nest start --watch`: changes in
`api/src` recompile and restart the API process inside the existing container.
Requests may briefly fail during that restart; retry once the API is ready.
The API image includes `procps`, which Nest needs to stop its child processes
before starting the replacement server. Source is mounted read-only into
containers; dependencies live in the images. Package or Dockerfile changes need
an image rebuild.

For direct Docker Compose use, run `docker compose up --build -d --wait` here,
then `docker compose port web 3000`. Open `http://localhost:<reported-port>`;
use `localhost` so Next's development hot-reload connection is allowed.
Stop with `docker compose down` to preserve the database.

Browser extensions can inject root HTML attributes such as `data-kantu` before
React hydrates. The layout suppresses hydration warnings only on the root
`html` element; hydration checks inside the app remain enabled.

Moving the example directory changes its Fugato runtime identity. Existing
volumes are not migrated to the new location.

To deliberately reset **this project's** database and forget its runtime
registration, run this from the example folder:

```sh
fugato destroy --volumes
```

This stops the stack and deletes its owned named volumes, including todo data.
It leaves the worktree files in place. A later `fugato up` registers the project
again with an empty database. Normal `fugato down` retains that database.

## Automated verification

From the public repository root, `make acceptance SUITE=fullstack`
tests CRUD, validation, proxy routing, isolated databases and host ports, source
changes, and restart persistence in two disposable worktrees. This test also
uses the CLI directly, without MCP or an AI agent.
