# List local environments

Run `fugato ls` from any directory to list all runtimes registered with the local
daemon, including stopped runtimes. Stacks started directly with Docker Compose
are not included.

`REPOSITORY` shows the main Git checkout path. `WORKTREE` shows the specific
project directory managed by that runtime, which can be a subdirectory of a
checkout. Rows sort by repository path, then project path, then runtime ID.

The registry already retains each runtime's shared Git directory, so deleting a
linked worktree does not lose its repository association. The CLI resolves the
main checkout from that metadata without reading the deleted worktree. If Git
metadata is also unavailable, a saved `.git` directory supplies its parent path;
other Git layouts show the saved Git directory instead. These display paths do
not change runtime identity. Listing does not remove stale registrations.
