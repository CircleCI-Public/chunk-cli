Remove the `chunk session` command and all of its subcommands (`start`,
`attach`, `cancel`, `resume`, `restore`, `list`). After the change, `session` is
no longer a subcommand of `chunk`, so `chunk session ...` fails as an unknown
command. Delete the tests that only covered the removed commands.

`chunk factory` runs as a session on the watch daemon and reuses code that
currently lives alongside the session commands, so `chunk factory` and
`chunk watch` must keep working and behave exactly as before: same usage, same
flags and flag defaults (including watch's hidden daemon subcommand), and the
same up-front refusals from `chunk factory` (when `CHUNK_WATCHD_REMOTE_ADDR` is
set, outside a git repository, and when the project has no `origin` remote).
Keep the tests that cover code factory still uses. The one intended change in
their behaviour: nothing chunk prints, including error messages, suggestions
and the `chunk watch` dashboard, may tell the user to run a `chunk session ...`
command any more, so reword or drop those hints.

Update the docs to match: no mention of `chunk session` may remain in `docs/`,
`README.md`, `AGENTS.md` or `skills/`, and the command tree in `docs/CLI.md`
no longer lists `session`.

Leave the watch daemon's session API in `internal/watchd` alone, since factory
runs on it. Don't add any new commands, flags or features.
