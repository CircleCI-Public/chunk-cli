## Summary 

Fixes FACT-589. When chunk runs on a terminal, it now recommends `chunk watch` at the moments it helps and says what shows. Watch never opens automatically.

- **Start of a run**: `factory build` and `factory start` print a second line after what chunk will do: "Best way to follow this run and every other one: chunk watch (findings, check output, the pull request). Or leave it running." `--remote` keeps its old wording.
- **Something needs a look**: a failed check or a review with findings ends its line with "(details in `chunk watch`)". The findings and check output still print under it.


- **End of a run**: every outcome prints the run ID and a pointer to watch:

> Passed: "Run <id>: see its reviews and checks in `chunk watch`."

> Exhausted or stuck:  "Run <id>: see the findings in `chunk watch`.", then the existing --continue <run id> line.

>  Cancelled, failed or no change: "see what happened in `chunk watch`".


- **`chunk init`**: ends with "Keep `chunk watch` open in another terminal to stay on top of your runs." Bare chunk help already lists watch (FACT-576).

- Only on a terminal: a new showHints check covers every hint. Nothing prints with `--json`, in CI, or when stdout or `stderr` isn't a terminal. The FACT-585 start message uses the same check, so it now also stays out of CI and piped `stdout`.
- Docs: docs/CLI.md and docs/GETTING_STARTED.md say watch is the recommended way to interact with chunk, and why.

Not included: the optional w key that would open watch from a running build. It depends on FACT-587's key handling and will be filed separately, as the ticket allows.

## Test plan

- [x] `task test` (-race) and `task lint` pass
- [x] New tests in `internal/cmd/factorywatch_test.go`: hints show only on a terminal (not with `--json`, in CI or piped), and the start, suffix, end-of-run and `chunk init` lines appear in order
- [ ] Manual: `chunk factory build` in a terminal shows the start, findings, check and end-of-run lines; piped or with `--json` it shows none
