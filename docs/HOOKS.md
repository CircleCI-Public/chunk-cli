# Hooks

Quality checks that run automatically as Claude Code works.

## How It Works

`chunk init` generates `.claude/settings.json` with three hooks:

**PreToolUse** — matches the `Bash` tool. Each hook entry carries an `if: "Bash(git commit*)"` filter so only git commit commands trigger validation. If any command fails, the commit is blocked.

**Stop** — runs `chunk validate` after every session ends. Skips everything
when the working tree is clean. When there are changes, it runs all configured
commands so problems are surfaced before the agent stops working.

**UserPromptSubmit** — runs `chunk validate --collect`, which reports what any
background run concluded. It runs no commands of its own.

## Background Validation

A Stop hook that runs the full check suite is the right thing for a risky
change and an expensive interruption for a typo. When the watch daemon is
running, the Stop hook offers it the choice: run the checks now, while the hook
waits, or take them into the background and answer on the next turn.

The daemon takes the offer when the change is:

- **under 500 lines**, or
- **confined to docs and text** — `.md`, `.markdown`, `.txt`, `.rst`, `.adoc`,
  and `LICENSE`/`NOTICE`/`AUTHORS`/`CHANGELOG`, at any size.

### What "under 500 lines" is measured against

The last state that passed its checks — not `HEAD`.

Every run snapshots the working tree before validating it, and a run that
passes leaves that snapshot behind as the mark to measure the next change from.
So five turns of 100 lines are five small changes, not a 500-line one by the
fifth. Measured against `HEAD` the number would only grow until something
committed, and the agent would be made to wait for work it had been released
for four turns running.

The snapshot is content, not a commit, which keeps the measurement honest in
both directions: a commit in the middle of a change no longer hides it, and a
commit no longer resets the count to zero — which would otherwise call a large
pile of unvalidated work "no change" the moment anything committed.

Nothing about your repository moves. The snapshot stages into a throwaway index
in a temp file, so your own staging area is untouched; it writes unreferenced
objects into `.git/objects` that git's `gc` collects in its own time. Until
a project's first passing run — a freshly started daemon, say — there is no mark
yet and the change is measured against `HEAD`, which reads larger and so errs
towards making you wait.

It holds the caller — the blocking behaviour of every earlier version — when:

- the change is larger than the limit,
- the working tree cannot be measured, or cannot be fingerprinted,
- **the project's last run failed.** A failure found in the background has
  nobody left to report to, so it is remembered and the next run is made to
  block, where the hook can act on what it finds. A passing run clears it. This
  is also what keeps a discarded result from being a lost one: a run whose tree
  changed while it was in flight is thrown away rather than reported, but if it
  threw away a failure, the debt it left still forces the next run to block.

Only the Stop hook is ever released. The commit gate on PreToolUse never is —
releasing it would let the commit it exists to hold back go through
unvalidated — and a `chunk validate` typed at a terminal has no later turn to
hear the answer on, so it always waits too.

A released hook prints where the answer will come from and exits 0:

```
  validating in the background: 0192cf6e (small change, 42 lines)
```

and the next turn begins with what it concluded:

```
chunk validate passed in the background (0192cf6e)
```

Failures arrive the same way, with the output of the run that failed. Because
a failure also leaves the project owing a blocking run, the Stop hook after it
blocks as it always did.

With no daemon running nothing changes: every hook run is blocking.

### The risk score

Every judgement also produces a 0–100 score, the facts behind it, and any
advice that follows. A low-risk change prints nothing — it is the common case,
and a number printed every turn is a number nobody reads. Anything else says so:

```
  validating now: large change since the last passing run, 2100 lines, over the 500-line limit
  risk 92/100 high — 2100 lines (60), 3 files (6), last run failed (25)
  2100 lines across 3 files is past the point where checks can run in the background; committing it in parts would get each piece checked sooner
```

The score is a report, not the decision. Release still turns on the facts
themselves, because a threshold on lines can be argued with and a threshold on
a composite cannot — nobody can tell you whether 47 should have been 52. What
the score is for is the questions one bit cannot answer: which of two changes
is riskier, and whether there is anything worth advising about either.

Advice is only given where there is something to do about it. "Commit in parts"
is actionable for 2,000 lines across nine files and impossible for 2,000 lines
in one generated file, so a single-file change is told nothing rather than
something it cannot act on.

## Result Caching

In hook mode only, a successful `chunk validate` run is cached. If the hook
fires again with nothing changed, the commands are skipped entirely:

```
chunk validate: skipped (no changes since last successful run)
```

Only successes are cached. A failing run is never stored, so the agent always
gets a real re-run after a fix attempt.

The clean-tree skip above and the cache below read the same working-tree
fingerprint, taken once per hook invocation.

The cache key covers:

- all of `.chunk/config.json` — the `commands` block, and the `environment` block
  that decides what those commands run against. A project that gitignores
  `.chunk/` still gets a re-run when either changes
- the execution target — the configured sidecar snapshot image and the active
  sidecar's ID, so a result validated against one sidecar is never reused for
  another
- the HEAD commit SHA
- the contents of every file git reports as changed — tracked, staged, and
  untracked alike

Because contents are hashed rather than just the `git status` output, editing a
file that was already dirty invalidates the entry. Any edit that could change a
command's result produces a new key.

Two things deliberately stay out of the key:

- **Gitignored files.** `git status` does not report ignored paths, so nothing
  under `.gitignore` participates in the digest — `.env.local`, generated code,
  vendored dependencies, local tooling config. Hashing them would mean walking
  trees like `node_modules` on every hook invocation. A command whose result
  depends on an ignored file can therefore report a hit after that file changes;
  touch a tracked file, or delete the cache directory, to force a re-run.
- **Environment variables** passed with `--env` or loaded from `.env.local`,
  for the same reason.

Caching is skipped, and commands always run, when:

- the run is not a hook invocation (a manual `chunk validate` never caches)
- `--cmd` supplied an inline command
- the working-tree state cannot be hashed reliably — not a git repo, a repo with
  no commits yet, or a changed path that cannot be read (an unreadable file, or a
  non-regular path such as a dirty submodule)
- the changed files total more than 64 MiB, the point past which hashing the tree
  costs more than re-running the commands

Those last two fail closed: without a trustworthy digest the key would depend on
the config alone and would stay stable across code changes, so no cache is
consulted at all. When the working tree cannot be hashed, the hook says so:

```
chunk validate: working tree state unavailable (hash sub: changed path is not a
regular file); running everything, caching nothing
```

That line is the only signal that a repo is getting no benefit from the cache, so
a repo that never prints `skipped` is not left unexplained.

Entries live outside the repo, under the per-project data directory:

```
$XDG_DATA_HOME/chunk/<sha256-of-project-path>/validate-cache/
```

Each entry is a small JSON file. Keys are content-addressed, so a superseded
entry (an older commit, an earlier working-tree state) is never read again; each
write sweeps entries older than 7 days, along with any partial file left behind by
an interrupted run. Nothing needs cleaning by hand, though deleting the directory
is always safe and forces a re-run.

## Worktree Support

The Stop hook uses `CLAUDE_WORKING_DIR` (the actual session working directory)
when available, falling back to `CLAUDE_PROJECT_DIR`. This means it correctly
targets the active worktree rather than the main repo root. No special
configuration is needed.

## Quick Start

```bash
# 1. Install chunk (see README)
chunk --version

# 2. Initialize project (detects commands, writes settings.json)
chunk init

# 3. Edit .chunk/config.json to adjust commands if needed
```

## Configuration

### `.chunk/config.json`

Commands are defined in the project config:

```json
{
  "commands": [
    {"name": "format", "run": "task fmt", "timeout": 30},
    {"name": "lint", "run": "task lint", "timeout": 60},
    {"name": "test", "run": "task test", "timeout": 300}
  ],
  "stopHookMaxAttempts": 3,
  "asyncValidate": "auto",
  "asyncValidateMaxLines": 500
}
```

`stopHookMaxAttempts` controls how many times the Stop hook will re-signal the
agent when validation keeps failing for the same uncommitted changes. After that
many consecutive failures the hook exits 0 (ending the session) instead of
non-zero (which would ask Claude to try again). Defaults to 3 if unset.

`asyncValidate` decides whether hook runs may be validated in the background:
`auto` (the default) applies the rules above, `never` keeps every run blocking,
and `always` releases every hook run — including one for a project that owes a
blocking run, since a project that asked for this has opted out of that safety
net. `asyncValidateMaxLines` moves the size threshold `auto` uses; a project
whose checks are fast enough to be worth waiting for can lower it.

### `.claude/settings.json`

Generated by `chunk init`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {"type": "command", "if": "Bash(git commit*)", "command": "cd ${CLAUDE_PROJECT_DIR:-.} && task fmt", "timeout": 30},
          {"type": "command", "if": "Bash(git commit*)", "command": "cd ${CLAUDE_PROJECT_DIR:-.} && chunk validate lint", "timeout": 60},
          {"type": "command", "if": "Bash(git commit*)", "command": "cd ${CLAUDE_PROJECT_DIR:-.} && chunk validate test", "timeout": 300}
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {"type": "command", "command": "chunk validate", "timeout": 420}
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {"type": "command", "command": "chunk validate --collect", "timeout": 10}
        ]
      }
    ]
  }
}
```

The `Bash(chunk:*)` permission is also granted so chunk CLI commands run
without prompting.

## Supported Environments

| IDE | Status | Notes |
|---|---|---|
| **Claude Code** (CLI / terminal) | Fully supported | Canonical provider |
| **Cursor** | Supported | Reads `.claude/settings.json` directly |
| **Codex** | Supported | `chunk init` writes `.codex/hooks.json` when Codex is detected |

## Disabling Stop-Hook Validation

Temporarily disable the `chunk validate` Stop hook without modifying `.claude/settings.json`:

```bash
chunk hook disable   # Creates .chunk/hooks-disabled sentinel file
chunk hook enable    # Removes the sentinel file
chunk hook status    # Shows "enabled" or "disabled"
```

Stop-hook validation is also skipped when the `CHUNK_HOOKS_DISABLED` environment variable is set to any non-empty value. This does not suppress `PreToolUse` commit hooks generated by `chunk init`, because those commands run directly from `.claude/settings.json`.

The `--project` flag overrides the project root used to locate the sentinel file.

## Manual Validation

Use `chunk validate` to run checks manually (outside of hooks):

```bash
chunk validate           # Run all configured commands
chunk validate test      # Run a specific command
chunk validate --list    # List configured commands
chunk validate --dry-run # Show commands without executing
```
