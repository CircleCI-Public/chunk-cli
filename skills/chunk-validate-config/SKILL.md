---
name: chunk-validate-config
description: Use when the user says "chunk keeps making me wait", "stop chunk blocking every turn", "run my tests in the background", "configure background validation", "always validate my migrations", "don't wait for docs changes", "change the async validate threshold", or asks why a change blocked instead of being backgrounded. Reads and sets the asyncValidate* keys in .chunk/config.json.
version: 1.0.0
allowed-tools:
  - Bash(chunk config set:*)
  - Bash(chunk config show)
  - Bash(cat .chunk/config.json)
  - Bash(git diff --name-only*)
  - Bash(git ls-files*)
---

# Configure background validation

The daemon decides per change whether the agent waits for `chunk validate` or is
released and told the answer on its next turn. These keys move that decision.
Nothing here changes *whether* a change is validated — every change is, always.
The only question is who waits.

## What each key does

| Key | Effect |
|---|---|
| `asyncValidate` | `auto` (default), `always`, `never` |
| `asyncValidateMaxLines` | Largest change still released under `auto` (default: 500) |
| `asyncValidateInert` | Extensions or file names this project counts as prose, released however large |
| `asyncValidateBlocking` | Extensions or file names that always block, whatever the size or mode |
| `asyncValidateWorktree` | Run background checks in a snapshot so edits can't stale the result |

The two rule lists take an extension with its dot (`.sql`) or an exact file name
(`NOTICE`). Not paths, not globs — both are refused with an error.

## How to decide

Ask what the user actually wants, then set the narrowest key that does it:

- **"it makes me wait too often"** – raise `asyncValidateMaxLines`, or add their
  prose-ish extensions to `asyncValidateInert`. Look at what they actually
  change: `git diff --name-only` and the repo's file types are better evidence
  than a guess.
- **"I never want to wait"** – `asyncValidate always`. Say plainly that a
  failure then always arrives a turn late.
- **"I always want to wait"** – `asyncValidate never`.
- **"but always check X"** – `asyncValidateBlocking`. This is the one to reach
  for when a default is wrong for them: a repo that lints its markdown puts
  `.md` here, and the built-in "markdown is prose" rule stops applying.
- **"the result was about code I'd already changed"** – `asyncValidateWorktree true`,
  but only if their checks don't need gitignored files (`node_modules`, build
  caches, `.env`). If they do, that flag will report environment failures as
  code failures. Ask before setting it.

## Setting them

```bash
chunk config set asyncValidateMaxLines 800
chunk config set asyncValidateInert ".sql,.csv"
chunk config set asyncValidateBlocking ".tf,.sql"
chunk config set asyncValidate auto
```

Comma-separated, and each `set` replaces that list rather than adding to it, so
read the current value first when the user says "also":

```bash
cat .chunk/config.json
```

Pass an empty string to clear a list: `chunk config set asyncValidateInert ""`.

## Rules

- **Never hand-edit `.chunk/config.json`** for these. `chunk config set`
  validates the value and rejects a path, a glob, an unknown mode, or an entry
  listed as both inert and blocking. Editing the file directly turns those into
  a config the daemon refuses to load.
- **`asyncValidateBlocking` wins over everything** – an inert default, a small
  diff, and `asyncValidate always`. It only ever makes somebody wait, which is
  why it is allowed to override an explicit mode.
- **Don't add source extensions to `asyncValidateInert`.** `.go`, `.ts`, `.py`
  and friends are what the checks exist to read. Putting them here releases the
  agent on changes most likely to fail.
- **Report what you set and what it means**, in one line: which key, the value,
  and whether the user will now wait more or less.
