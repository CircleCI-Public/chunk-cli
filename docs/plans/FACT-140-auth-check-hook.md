# FACT-140: Auth Check Hook

Exit 1 and prompt for configuration when required auth is missing.

## Goal

Agents start work without a valid CircleCI token and fail mid-task with unhelpful errors. Add a `UserPromptSubmit` hook that checks for CircleCI auth before the agent does anything, exits 1 with an actionable message if it's missing, and blocks the session until the user configures it.

## New Command: `chunk auth check`

Add `newAuthCheckCmd()` in `internal/cmd/auth.go` and register it under `newAuthCmd()` at line 31.

**Behavior:**
- Resolves config with `config.Resolve("", "")`.
- If `rc.CircleCIToken == ""`, writes to stderr and exits 1:
  ```
  chunk: CircleCI auth is not configured.
  Run: chunk auth set circleci
  ```
- Otherwise exits 0 silently.
- **Presence check only** (no API call). Fast enough for a per-prompt hook. Users who want full validation run `chunk auth status`.

**Cobra definition** (add after `newAuthRemoveCmd()` at line 33):
```go
cmd.AddCommand(newAuthCheckCmd())
```

```go
func newAuthCheckCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "check",
        Short: "Exit 1 if required auth is not configured",
        RunE: func(cmd *cobra.Command, _ []string) error {
            rc, _ := config.Resolve("", "")
            if rc.CircleCIToken == "" {
                io := iostream.FromCmd(cmd)
                io.ErrPrintln("chunk: CircleCI auth is not configured.")
                io.ErrPrintln("Run: chunk auth set circleci")
                return ErrSilentExit
            }
            return nil
        },
    }
}
```

`ErrSilentExit` (or equivalent) must cause cobra to exit 1 without printing "Error: ..." — check how other commands in the codebase suppress cobra's default error printing. If there's no existing sentinel, add one in `internal/cmd/usererr.go`.

## Hook Generation: `internal/settings/settings.go`

In `Build()` (line 36), add a `UserPromptSubmit` hook alongside the existing `Stop` hook:

```go
"UserPromptSubmit": {
    {
        Hooks: []hookEntry{
            {
                Type:    "command",
                Command: "chunk auth check",
                Timeout: 10,
            },
        },
    },
},
```

Full updated `Hooks` map in `Build`:
```go
s.Hooks = map[string][]hookGroup{
    "PreToolUse": { ... },            // unchanged
    "UserPromptSubmit": {
        {
            Hooks: []hookEntry{
                {Type: "command", Command: "chunk auth check", Timeout: 10},
            },
        },
    },
    "Stop": { ... },                  // unchanged
}
```

## Merge Logic: `internal/settings/merge.go`

`mergeHooks()` currently only handles `PreToolUse`. Extend it to also replace `Stop` and `UserPromptSubmit` wholesale from the generated settings (these are fully chunk-managed; no user groups to preserve).

Add after the `PreToolUse` merge block:

```go
// Replace chunk-managed matcherless hook types wholesale.
for _, hookType := range []string{"Stop", "UserPromptSubmit"} {
    if genGroup, ok := genHooks[hookType]; ok {
        if mergedHooks == nil {
            mergedHooks = map[string]interface{}{}
            merged["hooks"] = mergedHooks
        }
        mergedHooks[hookType] = genGroup
    }
}
```

This ensures `chunk init` run on an existing repo picks up the new `UserPromptSubmit` hook.

## Tests

### `internal/cmd/auth_test.go`

- `TestAuthCheckMissingToken`: no token in env or config → exit 1, stderr contains "chunk auth set circleci"
- `TestAuthCheckTokenPresent`: token set in env → exit 0, no output

### `internal/settings/merge_test.go`

- `TestMergePreservesUserPromptSubmit`: existing settings has no `UserPromptSubmit` → merged result includes the generated one
- `TestMergeReplacesUserPromptSubmit`: existing settings has a stale `UserPromptSubmit` → merged result has the new one from generated
- `TestMergePreservesUnknownHookTypes`: existing settings has a user-added hook type (e.g., `PostToolUse`) → it is preserved unchanged

### `internal/settings/settings_test.go`

- `TestBuildIncludesUserPromptSubmit`: `Build()` with at least one command produces JSON with a `UserPromptSubmit` hook containing `chunk auth check`

## Files Changed

| File | Change |
|------|--------|
| `internal/cmd/auth.go` | Add `newAuthCheckCmd()`, register under `newAuthCmd()` |
| `internal/cmd/usererr.go` | Add `ErrSilentExit` if not already present |
| `internal/settings/settings.go` | Add `UserPromptSubmit` hook in `Build()` |
| `internal/settings/merge.go` | Extend `mergeHooks()` to replace `Stop` and `UserPromptSubmit` from generated |
| `internal/cmd/auth_test.go` | New tests for `chunk auth check` |
| `internal/settings/merge_test.go` | New tests for `UserPromptSubmit` merge behavior |
| `internal/settings/settings_test.go` | Assert `UserPromptSubmit` hook present in `Build()` output |

## Out of Scope

- Checking Anthropic or GitHub tokens in the hook (those are workflow-specific; CircleCI is the minimum required credential for any sidecar/task operation)
- Network validation in the hook (use `chunk auth status` for that)
- Changes to `docs/HOOKS.md` or `docs/CLI.md` (update separately if needed)
