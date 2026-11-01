# Working on Fugato

Read `CONTRIBUTING.md` for the Go toolchain, checks, and security boundaries.
Before changing daemon lifecycle, concurrency, storage, control HTTP, or logs,
read `docs/architecture/daemon-lifecycle.md`. It records the decisions and
invariants that prevent previously reproduced races and cancellation failures.

- Preserve unrelated working-tree changes. Other tasks may be editing this repository.
- Keep request contexts through the manager, database, and subprocess boundaries.
- Treat effective Compose validation, runtime identity, and operation limits as
  security boundaries; each new execution entry point must honor them.
- Changes to these contracts need a regression test and an update to the decision
  record. Passing the race detector does not prove lifecycle ordering is correct.
- Do not introduce remote transport, automatic retries of mutations, or background
  work without defining ownership, cancellation, and delivery semantics first.
