# chunk mutate

Mutation testing finds gaps in test coverage by changing production code and
checking whether the test suite detects each change. A mutation that passes the
test suite is reported as `SURVIVED` and points to behavior that lacks an
effective assertion.

`chunk mutate` currently supports Go projects. It parses non-test Go files with
the standard library, enumerates operator substitutions, and creates a small
patch for each mutation without changing the local working tree.

## Enumerate mutations

```sh
chunk mutate
chunk mutate ./internal/...
chunk mutate --max 100
chunk mutate --output json
```

Without `--parallel`, the command prints the candidate inventory and exits.
The optional path may name a file, directory, or recursive `...` pattern.

## Run mutations

```sh
chunk mutate --parallel 10
chunk mutate --parallel 10 --max 100
chunk mutate --parallel 10 --test-cmd "task test"
```

With `--parallel N`, the command creates or reuses the `mutate` sidecar pool
with capacity `N`. Pool members become available independently as synchronization
finishes. Each available member receives one mutation patch, runs the configured
`test` command, reverses the patch, and returns to the pool for another mutation.

The test command defaults to the command named `test` in `.chunk/config.json`.
Use `--test-cmd` to override it and `--test-timeout` to bound each run. Sidecars
and pool state are retained for reuse unless `--destroy-pool` is passed. The
organization comes from `--org-id`, `CIRCLECI_ORG_ID`, project configuration,
or the interactive organization picker, in that order.

Results are classified as:

- `KILLED`: the test command failed and detected the mutation.
- `SURVIVED`: the test command passed despite the mutation.
- `ERROR`: setup, patching, remote execution, or cleanup failed.
