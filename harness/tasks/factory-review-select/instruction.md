Add a `--review <name>` flag to `chunk factory` that runs only the named review
prompts instead of every prompt in the reviews directory. A review's name is
its prompt file's name without the extension: `bugs` for
`.chunk/reviews/bugs.md`. The flag can be repeated to select several reviews
(`--review bugs --review security`), and naming the same review more than once
selects it once. It selects from whichever directory the run uses: the default
`.chunk/reviews`, or the one given with `--reviews`.

Without the flag, every review prompt runs, as now. The flag only narrows the
reviews: the project's validation commands still run unless `--no-validate` is
given.

When reviews are selected:

- only the selected reviews run, and only they appear in the run's record (the
  rounds' reviews that `chunk watch` shows and `chunk factory --json` prints);
- the reviewer sidecar count follows the selection: by default one per selected
  review, and `--reviewers` is capped at the number of selected reviews.

A name that matches no review prompt is refused before the run starts:
`chunk factory` exits with the bad-arguments exit code (2), and its error names
each unknown review and lists the names of the reviews that are available. This
includes a project whose reviews directory has no prompts at all.

The watch daemon enforces the same rules, since other clients start factory
runs through its API: the factory request (`POST /factory`) takes the selection
as `review_names`, a JSON array of names, and an unknown name is refused with a
400 whose message names it, without starting a run.

Add focused tests, and document the flag in `docs/CLI.md`.
