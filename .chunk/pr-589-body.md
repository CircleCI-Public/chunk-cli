## Summary

Fixes FACT-589. When chunk runs on a terminal, it now recommends `chunk watch` at the moments it helps and says what watch shows. Watch never opens automatically.

- **Start of a run:** `factory build` and `factory start` print a second line after what chunk will do: *"Best way to follow this run and every other one: `chunk watch` (findings, check output, the pull request). Or leave it running."* `--remote` keeps its old wording.
- **Something needs a look:** a failed check or a review with findings ends its line with *"(details in chunk watch)"*. The findings and check output still print under it.
- **End of a run:** every outcome prints the run ID and a pointer to watch.
  - Passed: *"Run <id>: see its reviews and checks in chunk watch."*
  - Exhausted or stuck: *"Run <id>: see the findings in chunk watch."*, then the existing `--continue <run id>` line.
  - Cancelled, failed or no change: *"see what happened in chunk watch"*.
- **`chunk init`:** ends with *"Keep chunk watch open in another terminal to stay on top of your runs."* Bare `chunk` help already lists watch (FACT-576).
- **Only on a terminal:** a new `showHints` check covers every hint. Nothing prints with `--json`, in CI, or when stdout or stderr isn't a terminal. The FACT-585 start message uses the same check, so it now also stays out of CI and piped stdout.
- **Docs:** `docs/CLI.md` and `docs/GETTING_STARTED.md` say watch is the recommended way to interact with chunk, and why.

## Test plan

- [x] `task test` passes (all 3,210 tests, run with `-race`)
- [x] `task lint` passes
- [x] New tests in `internal/cmd/factorywatch_test.go`:
  - `showHints` is false for a non-terminal, `--json` and `CI`, and true on a terminal
  - the start messages include the watch line, and `--remote` doesn't
  - on a terminal, failed check and findings lines get the suffix; off a terminal their output is unchanged
  - a passed build on a terminal prints the start line and the end-of-run pointer, in order
  - an exhausted build on a terminal points at watch, then suggests `--continue run-1`
  - `--json` on a terminal, and a non-terminal, never mention `chunk watch`
  - `chunk init` ends with the keep-watch-open line on a terminal, and not otherwise
- [ ] Manual: run `chunk factory build "..."` in a real terminal and check the start, findings, check and end-of-run lines
- [ ] Manual: `chunk factory build "..." 2>&1 | cat` and `--json` print no watch hints
- [ ] Manual: `chunk init` in a fresh project ends with the watch line

🤖 Generated with [Claude Code](https://claude.com/claude-code)
