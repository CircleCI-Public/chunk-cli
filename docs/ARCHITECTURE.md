# Architecture

Module layout and dependency rules for the `chunk` Go CLI.

## Directory Structure

```
chunk-cli/
├── main.go                    # Entry point: cobra bootstrap + usererr handling
├── skills/                    # Skill definitions (go:embed) and skill subdirectories
├── acceptance/                # Acceptance tests
└── internal/
    ├── cmd/                   # Cobra command definitions (thin wrappers)
    │   ├── root.go            # Root command, registers all subcommands
    │   ├── auth.go            # auth set, auth status, auth remove
    │   ├── buildprompt.go     # build-prompt
    │   ├── completion.go      # completion install/uninstall/zsh
    │   ├── config.go          # config show/set
    │   ├── init.go            # init (project setup, settings.json generation)
    │   ├── sandbox.go         # sandbox list/create/exec/add-ssh-key/ssh/sync/env/build
    │   ├── skills.go          # skill install/list
    │   ├── task.go            # task run
    │   ├── upgrade.go         # upgrade
    │   └── validate.go        # validate
    ├── anthropic/             # Anthropic Messages API client
    ├── buildprompt/           # Three-step pipeline: discover → analyze → generate
    ├── circleci/              # CircleCI REST API client
    ├── config/                # User config (~/.chunk/config.json)
    ├── github/                # GitHub GraphQL client (reviews, repos)
    ├── gitremote/             # Git remote URL parsing for org/repo detection
    ├── gitutil/               # Git utility helpers
    ├── httpcl/                # HTTP client library (JSON + retries)
    ├── iostream/              # I/O stream abstraction
    ├── sandbox/               # CircleCI sandbox operations
    ├── skills/                # Skill definitions (go:embed) and installation
    ├── task/                  # Task run config and CircleCI trigger
    ├── testing/recorder/      # HTTP recorder for tests
    ├── tui/                   # Terminal UI components (confirm, input, select)
    ├── ui/                    # Colors, formatting, spinner
    ├── upgrade/               # CLI self-upgrade
    ├── usererr/               # User-facing error wrapper
    └── validate/              # Validation command logic
```

## Layering Rules

Dependencies flow strictly downward:

```
main.go → internal/cmd/ → internal/{business packages} → internal/httpcl/
```

- `main.go` creates the root command and handles top-level errors
- `internal/cmd/` contains thin cobra wrappers that parse flags and delegate
- Business packages (`buildprompt/`, `task/`, etc.) contain the logic
- `internal/httpcl/` is an independent library — no imports are allowed to other `internal/` packages
- `config/` is a leaf — no imports from other `internal/` packages

No upward or lateral imports between business packages, except where a
package naturally composes another (e.g. `task/` uses `circleci/`).

## Entry Point

```go
main() → cmd.NewRootCmd(version) → rootCmd.Execute()
```

Errors are caught in `main()`. If the error is a `usererr.Error`, only the
user-facing message is printed (no stack trace). Otherwise the raw error
is printed. Both exit with code 1.

## Data Flow: `build-prompt`

Three-step pipeline orchestrated by `buildprompt.Run()`:

```
1. Discover          github/ → FetchReviewActivity() per repo
                     → AggregateActivity() → TopN reviewers
                     → FilterDetailsByReviewers()
                     → writes details.json, details-pr-rankings.csv

2. Analyze           GroupByReviewer(comments)
                     → anthropic/ → AnalyzeReviews() → Claude
                     → writes analysis.md

3. Generate          Read analysis.md
                     → anthropic/ → GenerateReviewPrompt() → Claude
                     → writes review-prompt.md
```

### Org and repo resolution

- If `--org` is provided, `--repos` is required
- If neither is provided, both are auto-detected from the git remote

### Model defaults

- Analysis step: `claude-sonnet-4-6`
- Generation step: `claude-opus-4-6`
- Overridable via `--analyze-model` / `--prompt-model` flags

## Configuration and Environment Variables

### Single source of truth for env var names

Define every environment variable name as a `const` in the `config`
package. Use these constants in user-facing messages and `t.Setenv` calls.
Never use bare `os.Getenv("CIRCLE_TOKEN")` strings.

```go
const (
    EnvCircleToken     = "CIRCLE_TOKEN"
    EnvAnthropicAPIKey = "ANTHROPIC_API_KEY"
    EnvGitHubToken     = "GITHUB_TOKEN"
    EnvModel           = "CODE_REVIEW_CLI_MODEL"
    // ...
)
```

### Struct-based env loading

Declare all environment variables once in an `EnvVars` struct with `env`
struct tags (via `go-envconfig`). Express defaults as tag values, not
if-empty checks:

```go
type EnvVars struct {
    CircleToken      string `env:"CIRCLE_TOKEN"`
    CircleCIBaseURL  string `env:"CIRCLECI_BASE_URL,default=https://circleci.com"`
    AnthropicAPIKey  string `env:"ANTHROPIC_API_KEY"`
    AnthropicBaseURL string `env:"ANTHROPIC_BASE_URL,default=https://api.anthropic.com"`
    // ...
}
```

`LoadEnv(ctx)` populates the struct via `envconfig.Process`.

When adding a new environment variable:
1. Add a `const Env...` for user-facing messages and test code.
2. Add a field to `EnvVars` with an `env` tag (and `default=` if needed).
3. Wire it into `Resolve()` or consume it from the struct directly.

### Layered resolution with explicit precedence

Config resolves through a strict priority chain:

    flag > env var > config file > default

`Resolve()` returns a `ResolvedConfig` struct. Each value is paired with
a source string (e.g. `"Environment variable (CIRCLE_TOKEN)"`) so
status/diagnostic output can show where the value came from.

Not all values support all layers — for example, CircleCI and GitHub
tokens have no flag, so their chain is `env var > config file`. The
Anthropic API key and model support the full chain.

User config lives at `~/.chunk/config.json`:

```json
{
  "apiKey": "sk-...",
  "model": "claude-sonnet-4-6"
}
```

### Client constructors accept config, not env

Client `New()` functions receive values from the resolved config. They
must not call `os.Getenv` themselves. This keeps env reading centralised
in `config.Resolve` and makes clients testable.

### Environment variable reference

| Variable | Used by | Purpose |
|---|---|---|
| `ANTHROPIC_API_KEY` | anthropic, config, validate | Anthropic authentication |
| `ANTHROPIC_BASE_URL` | anthropic, validate | API endpoint override |
| `GITHUB_TOKEN` | github | GitHub authentication |
| `GITHUB_API_URL` | github | GitHub API endpoint override |
| `CIRCLE_TOKEN` / `CIRCLECI_TOKEN` | circleci | CircleCI authentication |
| `CIRCLECI_BASE_URL` | circleci | CircleCI endpoint override |
| `CLAUDE_PROJECT_DIR` | init | IDE-provided project directory |

## Pre-Commit Hooks

`chunk init` generates `.claude/settings.json` with a `PreToolUse` hook that
runs configured validation commands before the AI agent commits code. See
**[docs/HOOKS.md](HOOKS.md)** for details.

`chunk validate` runs those same commands manually, with SHA256-based content
caching so unchanged files skip re-execution.

## HTTP Client (`internal/httpcl/`)

Shared HTTP infrastructure used by `anthropic/`, `circleci/`, and `github/`:

- JSON request/response encoding by default
- Automatic retry via `hashicorp/go-retryablehttp` (up to 3 retries)
- Configurable auth (Bearer token or custom header like `x-api-key`)
- Fluent request builder: `httpcl.NewRequest(method, path, opts...)`

## Error Handling

### Two-tier error model

Every error returned from a command carries two perspectives:

1. **Developer error** — the wrapped `error` chain for logs and debugging.
2. **User-facing message** — a plain-English sentence shown on stderr.

The `userError` struct in `internal/cmd/usererr.go` satisfies both `error`
and a set of display interfaces:

```go
type userError struct {
    msg        string // brief user-facing headline
    detail     string // optional clarification
    suggestion string // optional actionable hint
    err        error  // underlying Go error (for errors.Is / As)
}

func (e *userError) UserMessage() string  { return e.msg }
func (e *userError) Detail() string       { return e.detail }
func (e *userError) Suggestion() string   { return e.suggestion }
func (e *userError) Unwrap() error        { return e.err }
```

The display interfaces (`UserMessage`, `Detail`, `Suggestion`) are checked
via type assertion at the top-level error handler — they are not imported
as a named interface. This keeps the error type private to `cmd`.

### Single error-rendering boundary

All formatting happens in `main()`, never inside command handlers:

```go
func main() {
    if err := rootCmd.Execute(); err != nil {
        msg, detail, suggestion := errorDetails(err)
        fmt.Fprint(os.Stderr, ui.FormatError(msg, detail, suggestion))
        os.Exit(1)
    }
}
```

`errorDetails` probes the error for the three display interfaces via duck
typing, then falls back to sensible defaults (the raw `.Error()` string
as detail, pattern-matched hints as suggestion).

Rules:
- Command handlers must never call `ui.FormatError` or print styled error
  text themselves. Return the error; let the boundary format it.
- Never use a sentinel "silent" error to suppress output. Every non-nil
  error produces output through the single boundary.
- Helpers like `notAuthorized(action, err)` and `sshSessionError(err)` can
  inspect an error and return a `*userError` (or nil to signal "not my
  error"). The caller chains them:
  ```go
  if err := notAuthorized("sync files", err); err != nil { return err }
  ```

### Typed package-level errors

API client packages export sentinel errors and typed error structs so
callers use `errors.Is` / `errors.As` instead of string matching:

```go
// internal/anthropic
var ErrKeyNotFound = errors.New("api key not found")
var ErrTokenLimit  = errors.New("prompt exceeds context window")

type StatusError struct {
    Op         string
    StatusCode int
}
```

HTTP client packages must not leak the shared `httpcl.HTTPError` type to
callers. Instead, wrap it into a package-local `StatusError` via a
`mapErr` helper:

```go
func mapErr(op string, err error) error {
    var he *hc.HTTPError
    if !errors.As(err, &he) { return err }
    return &StatusError{Op: op, StatusCode: he.StatusCode}
}
```

## Display and UI Decoupling

### Business logic never imports `ui`

The `ui` package owns all ANSI styling (`Bold`, `Dim`, `Red`, `Green`,
`Warning`, `Success`, `FormatError`). Business logic in `internal/` must
not import it.

Use **callback injection** for progress reporting via `iostream.StatusFunc`:

```go
// iostream/status.go
type Level int
const (
    LevelStep Level = iota
    LevelInfo
    LevelWarn
    LevelDone
)
type StatusFunc func(level Level, msg string)
```

The `cmd` layer wires the callback to styled output:

```go
func newStatusFunc(streams iostream.Streams) iostream.StatusFunc {
    return func(level iostream.Level, msg string) {
        switch level {
        case iostream.LevelStep:
            streams.ErrPrintln(ui.Bold(msg))
        case iostream.LevelInfo:
            streams.ErrPrintf("  %s\n", ui.Dim(msg))
        case iostream.LevelWarn:
            streams.ErrPrintf("  %s\n", ui.Warning(msg))
        case iostream.LevelDone:
            streams.ErrPrintf("  %s\n", ui.Success(msg))
        }
    }
}
```

Business logic accepts `StatusFunc` as a parameter and calls it for
progress output. Tests can pass a no-op or capturing stub.

# Test Assertions

Use `gotest.tools/v3/assert` for test assertions:

- **Prefer `assert.Check`** to keep the test running and collect as many
  failures as possible in a single run
- **Use `assert.Assert` / `assert.NilError` as gates** — only when failure means
  the remaining assertions are pointless or unsafe (e.g. a nil pointer would
  panic, or a missing resource means nothing else can be verified)
- **Do not call functions or methods** directly inside the assertion; always use
  a temporary variable
- **Use `cmp` comparisons** from `gotest.tools/v3/assert/cmp` for semantic
  matchers over raw boolean expressions

`assert.Assert` and `assert.Check` both accept three kinds of argument: a `bool`
expression, a `cmp.Comparison`, or an `error`.

## Assert vs Check

`assert.Check` calls `t.Fail` and returns `false`, allowing the test to continue
collecting failures — **prefer it by default**. `assert.Assert` calls
`t.FailNow` and stops immediately — use it only as a gate.

The canonical pattern: use `assert.NilError` (or `assert.Assert`) to gate on
preconditions, then use `assert.Check` for everything else:

```go
result, err := doSomething()
assert.NilError(t, err)                              // gate: no point checking result if err != nil
assert.Check(t, result.OK)
assert.Check(t, cmp.Equal(result.Status, "ready"))
assert.Check(t, cmp.Len(result.Items, 3))
assert.Check(t, cmp.Contains(result.Name, "prefix"))
```

**Named functions are all fatal.** `assert.Equal`, `assert.DeepEqual`,
`assert.Error`, `assert.ErrorContains`, and `assert.ErrorIs` all call
`t.FailNow`. To get the non-fatal equivalent, use `assert.Check` with the
corresponding `cmp` comparison:

| Fatal (gate only)                     | Non-fatal equivalent                             |
| ------------------------------------- | ------------------------------------------------ |
| `assert.Equal(t, a, b)`               | `assert.Check(t, cmp.Equal(a, b))`               |
| `assert.DeepEqual(t, a, b)`           | `assert.Check(t, cmp.DeepEqual(a, b))`           |
| `assert.Error(t, err, "msg")`         | `assert.Check(t, cmp.Error(err, "msg"))`         |
| `assert.ErrorContains(t, err, "sub")` | `assert.Check(t, cmp.ErrorContains(err, "sub"))` |
| `assert.ErrorIs(t, err, target)`      | `assert.Check(t, cmp.ErrorIs(err, target))`      |
| `assert.Assert(t, <bool or cmp>)`     | `assert.Check(t, <bool or cmp>)`                 |

Note: `assert.Assert` must be called from the goroutine running the test
function. `assert.Check` is safe to call from any goroutine.

## Temporary variable rule

Never pass a function or method call directly as an assertion argument. Always
capture the result in a variable first. This applies to all calls, including
error-returning functions, getters, and string conversions.

```go
// ❌ BAD
assert.NilError(t, os.WriteFile(path, data, perm))
assert.Check(t, cmp.Equal(st.Code(), codes.NotFound))
assert.Check(t, cmp.Len(registry.List(), 0))

// ✅ GOOD
err := os.WriteFile(path, data, perm)
assert.NilError(t, err)

stCode := st.Code()
assert.Check(t, cmp.Equal(stCode, codes.NotFound))

sandboxes := registry.List()
assert.Check(t, cmp.Len(sandboxes, 0))
```

Type conversions (`int32(x)`, `string(b)`) and built-in functions (`len`) are
exempt from this rule.

## Message argument

`assert.Check` (and `assert.Assert`) accept a trailing
`msgAndArgs ...interface{}` that is appended to the failure output. Pass a
message when the comparison alone does not make the intent obvious — for
example, when checking a boolean derived from non-obvious logic, when the
variable name is ambiguous, or when the test loops over cases and you need to
identify which iteration failed.

```go
// ❌ Opaque — failure says "false" with no context
assert.Check(t, got.ExpiresAt.Before(deadline))

// ✅ Clear — failure says what the check was verifying
assert.Check(t, got.ExpiresAt.Before(deadline), "token must expire before session deadline")

// ❌ In a loop — impossible to tell which item failed
for _, item := range items {
    assert.Check(t, cmp.Equal(item.State, "ready"))
}

// ✅ In a loop — failure identifies the offending item
for _, item := range items {
    assert.Check(t, cmp.Equal(item.State, "ready"), "item %q", item.ID)
}
```

Skip the message when the comparison is already self-documenting — `cmp.Equal`,
`cmp.DeepEqual`, `cmp.Len`, and `cmp.ErrorIs` all produce structured failure
messages that include the values involved, so they rarely need extra annotation.

## Examples

```go
import (
    "gotest.tools/v3/assert"
    "gotest.tools/v3/assert/cmp"
)

func TestSomething(t *testing.T) {
    // Gate: fail immediately if setup fails — nothing else can run
    err := startContainer(ctx)
    assert.NilError(t, err)

    result, err := doSomething()
    assert.NilError(t, err)  // gate: result is meaningless if err != nil

    // Check everything else — collects all failures in one run
    assert.Check(t, cmp.Equal(result.Status, "ok"))
    assert.Check(t, cmp.Len(result.Items, 3))
    assert.Check(t, result.Ready)
}
```

## Semantic Matchers

Use the most specific assertion for the situation. Prefer named functions over
raw boolean expressions when a named function exists. Fall back to
`assert.Check(t, <bool>)` for comparisons that have no dedicated function — the
expression source code appears verbatim in the failure message, which is good
enough.

### Core functions

| Situation                                  | Preferred form (non-fatal)                         | Gate form (fatal)                       |
| ------------------------------------------ | -------------------------------------------------- | --------------------------------------- |
| `err` must be nil                          | —                                                  | `assert.NilError(t, err)`               |
| Two scalar values must be equal (`==`)     | `assert.Check(t, cmp.Equal(actual, expected))`     | `assert.Equal(t, actual, expected)`     |
| Complex values must be equal (go-cmp diff) | `assert.Check(t, cmp.DeepEqual(actual, expected))` | `assert.DeepEqual(t, actual, expected)` |
| Error must match exact message             | `assert.Check(t, cmp.Error(err, "msg"))`           | `assert.Error(t, err, "msg")`           |
| Error must contain substring               | `assert.Check(t, cmp.ErrorContains(err, "sub"))`   | `assert.ErrorContains(t, err, "sub")`   |
| Error must match sentinel / wrapped error  | `assert.Check(t, cmp.ErrorIs(err, target))`        | `assert.ErrorIs(t, err, target)`        |
| Anything else                              | `assert.Check(t, <bool or cmp>)`                   | `assert.Assert(t, <bool or cmp>)`       |

### Nil and emptiness

There are no dedicated `NotNil` or `NotEmpty` helpers. Use
boolean expressions — the source is included in the failure message:

```go
// Use Assert as a gate when nil would cause a panic below
assert.Assert(t, result != nil)

// Use Check for non-fatal emptiness assertions
assert.Check(t, cmp.Nil(result))
assert.Check(t, len(items) != 0)
```

### Numeric comparisons

Express comparisons directly as boolean expressions:

```go
assert.Check(t, x > 0)
assert.Check(t, a >= b)
assert.Check(t, count < limit)
```

### String contains

To check if a string contains a substring:

```go
result := "this is the haystack"
assert.Check(t, cmp.Contains(result, "needle"))
```

### Length and containment

Use `cmp` comparisons for richer failure messages:

```go
// Length — prints expected vs actual length on failure
assert.Check(t, cmp.Len(items, 3))

// Containment — works for slices, maps, and strings
assert.Check(t, cmp.Contains(slice, item))
assert.Check(t, cmp.Contains(mapping, "key"))
assert.Check(t, cmp.Contains(str, "substr"))
```

### Structured data

Use `assert.Check(t, cmp.DeepEqual(...))` for structs, slices, and maps. It uses
`go-cmp` and produces a clear diff on failure:

```go
assert.Check(t, cmp.DeepEqual(result, myStruct{Name: "title"}))
// assertion failed: ... (diff of the two values)
```

For unordered slice comparison, pass `cmpopts.SortSlices` from
`github.com/google/go-cmp/cmp/cmpopts`:

```go
assert.Check(t, cmp.DeepEqual(actual, expected,
    cmpopts.SortSlices(func(a, b string) bool { return a < b }),
))
```

For JSON, unmarshal first and use `DeepEqual`:

```go
var actual, expected MyType
err := json.Unmarshal(data, &actual)
assert.NilError(t, err)
assert.Check(t, cmp.DeepEqual(actual, expected))
```

### Pattern matchers

```go
assert.Check(t, cmp.Regexp(`^\d{4}-\d{2}-\d{2}$`, dateStr))
```

### Custom comparisons

`assert.Check` (and `assert.Assert`) accept any `cmp.Comparison` — a function
that returns a `cmp.Result`. Use this for domain-specific checks:

```go
withinTolerance := func(got, want, delta float64) cmp.Comparison {
    return func() cmp.Result {
        if math.Abs(got-want) <= delta {
            return cmp.ResultSuccess
        }
        return cmp.ResultFailure(fmt.Sprintf("%v not within %v of %v", got, delta, want))
    }
}

assert.Check(t, withinTolerance(actual, expected, 0.01))
```
