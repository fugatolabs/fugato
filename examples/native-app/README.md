# Native app with two previews and a worker

This example uses Node.js and npm, with no dependencies to install and no Compose
file. Run from this directory inside a Git worktree:

```sh
fugato doctor
fugato up
fugato ports
fugato open --endpoint website
fugato open --endpoint admin
fugato check ready
fugato run test
fugato run console
fugato logs
fugato down
```

`web` and `worker` are persistent host commands. `test` is a finite task and
`console` attaches the Node REPL to your terminal. The two endpoints receive
separate dynamically allocated ports through explicit environment variables.
Neither endpoint is implicitly chosen when opening a preview.
Exit the REPL with `.exit` before continuing with logs and shutdown.

The app writes `runtime.txt` into `FUGATO_DATA_DIR`. Stop retains that directory.
Host processes run with your permissions; only applications that honor the
provided port/data settings gain those separation properties.

For comparison, `npm run dev` starts the app directly on ports 3000 and 3001.
The checked-in scripts and this documentation give an agent enough evidence to
configure the same workload without guessing from package names.
