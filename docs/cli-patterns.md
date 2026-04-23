# CLI Construction Patterns

Patterns extracted from the `chunk` CLI (Go/cobra). Use these as constraints
when building new commands, error paths, config resolution, or HTTP clients.

---

## Error Architecture

### Two-tier error model

Every error returned from a command carries two perspectives:

1. **Developer error** — the wrapped `error` chain for logs and debugging.
2. **User-facing message** — a plain-English sentence shown on stderr.

Implement with a struct that satisfies both `error` and a set of
display interfaces:

```go
type userError struct {
    msg        string // brief user-facing headline
    detail     string // optional clarification
    suggestion string // optional actionable hint
    errMsg     string // fallback Error() text when err is nil
    err        error  // underlying Go error (for errors.Is / As)
}

func (e *userError) Error() string {
    if e.err != nil {
        return e.err.Error()
    }
    if e.errMsg != "" {
        return e.errMsg
    }
    return e.msg
}
func (e *userError) UserMessage() string  { return e.msg }
func (e *userError) Detail() string       { return e.detail }
func (e *userError) Suggestion() string   { return e.suggestion }
func (e *userError) Unwrap() error        { return e.err }
```

The display interfaces (`UserMessage`, `Detail`, `Suggestion`) are checked
via type assertion at the top-level error handler — they are not imported
as a named interface. This keeps the error type private to the `cmd` package.

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
as the detail, pattern-matched hints as the suggestion).

**Rules:**
- Command handlers must never call `ui.FormatError` or print styled error
  text themselves. Return the error; let the boundary format it.
- Never use a sentinel "silent" error to suppress output. Every non-nil
  error produces output through the single boundary.
- Helpers like `notAuthorized(action, err)` and `sshSessionError(err)`
  inspect an error and return an `error` (or nil to signal "not my
  error"). The caller chains them:
  ```go
  if err := notAuthorized("sync files", err); err != nil { return err }
  ```

### Typed package-level errors instead of string matching

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
callers. Instead they wrap it into their own `StatusError` via a `mapErr`
helper.

---

## Configuration and Environment Variables

### Single source of truth for env var names

Every environment variable name is a `const` in the `config` package,
grouped by domain. Constants are kept for user-facing messages and
test `t.Setenv` calls:

```go
const (
    EnvCircleToken     = "CIRCLE_TOKEN"
    EnvAnthropicAPIKey = "ANTHROPIC_API_KEY"
    EnvGitHubToken     = "GITHUB_TOKEN"
    EnvModel           = "CODE_REVIEW_CLI_MODEL"
    // ...
)
```

No bare `os.Getenv("CIRCLE_TOKEN")` strings anywhere. Test code uses
the same constants.

### Struct-based env loading with `go-envconfig`

All environment variables are declared once in an `EnvVars` struct with
`env` struct tags. Defaults (e.g. base URLs) are expressed as tag values,
not if-empty checks:

```go
type EnvVars struct {
    CircleToken      string `env:"CIRCLE_TOKEN"`
    CircleCIBaseURL  string `env:"CIRCLECI_BASE_URL,default=https://circleci.com"`
    AnthropicAPIKey  string `env:"ANTHROPIC_API_KEY"`
    AnthropicBaseURL string `env:"ANTHROPIC_BASE_URL,default=https://api.anthropic.com"`
    GitHubToken      string `env:"GITHUB_TOKEN"`
    GitHubAPIURL     string `env:"GITHUB_API_URL,default=https://api.github.com"`
    Model            string `env:"CODE_REVIEW_CLI_MODEL"`
    // ...
}
```

`LoadEnv(ctx)` populates the struct via `envconfig.Process`. `Resolve()`
calls `LoadEnv` once and reads fields from the struct — no scattered
`os.Getenv` calls.

When adding a new environment variable:
1. Add a `const Env...` for user-facing messages and test code.
2. Add a field to `EnvVars` with an `env` tag (and `default=` if needed).
3. Wire it into `Resolve()` or consume it from the struct directly.

### Layered resolution with explicit precedence

Config resolves through a clear priority chain

    flag > env var > config file > default (default could be zero value)

The `Resolve()` function returns a `ResolvedConfig` struct with the value
and its source string (e.g. `"Environment variable (CIRCLE_TOKEN)"`),
so status/diagnostic output can show where a value came from.

### Client constructors accept config, not env

Client `New()` functions read from the resolved config rather than calling
`os.Getenv` themselves. This makes them testable and keeps the env-reading
responsibility centralised in `config.Resolve`.

---

## Display and UI Decoupling

### Business logic never imports `ui`

The `ui` package owns all ANSI styling (`Bold`, `Dim`, `Red`, `Green`,
`Warning`, `Success`, `FormatError`). Business logic in `internal/` must
not import it.

Instead, use **callback injection** for progress reporting:

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

## Testing Conventions

- Integration over mocks. Use `httptest.NewServer` with fake handlers.
- Each test isolates config by setting `$HOME` to a temp dir.
- Always run with `-race`.
- Tests needing external credentials skip gracefully.
- Acceptance tests live in `acceptance/` and run the compiled binary.
- Acceptance tests assert on both stdout and stderr content, checking
  for user-facing message fragments (not raw error strings).
