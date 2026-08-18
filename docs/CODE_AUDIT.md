# Code audit

A point-in-time review of `chunk-cli` for correctness, duplication and adherence to the
conventions in [AGENTS.md](../AGENTS.md) and [ARCHITECTURE.md](ARCHITECTURE.md).

**Audited at:** `6bb97f9` (main, after #505 merged). Every `file:line` below was re-derived
against that commit. Line numbers drift — treat them as a starting point, not a contract.

**Nothing in this audit has been fixed.** It is a work list.

## How to read this

Findings are grouped by kind and ordered by severity within each group. Each one gives the
defect, why it matters, and the fix. Where a finding is a symptom of a deeper one it says so,
so the same thing does not get fixed twice.

Items marked **[swept]** came from a broad automated sweep and were not individually
re-verified by hand. Everything else was confirmed by reading the code. Two findings were
confirmed by running code rather than reading it, and say so explicitly.

---

## 1. Correctness

### 1.1 Nested retry gives 12 HTTP attempts per GraphQL query

`internal/github/client.go:37` constructs the shared HTTP client without `DisableRetries`, so
`internal/httpcl` retries three times internally (`internal/httpcl/client.go:106`,
`RetryMax = 3`, roughly 1s + 2s + 4s of backoff). `internal/github/retry.go:75`
(`doWithRetry`) then wraps that in **another** three attempts with its own 2s/4s backoff.

A GitHub 5xx therefore costs up to 12 HTTP attempts and around 27 seconds **per query**, and
`build-prompt` issues many. Retry amplification also multiplies load on the failing service.

**Fix:** `DisableRetries: true` on the `hc.Config` at `internal/github/client.go:37`, or delete
`doWithRetry` and let `httpcl` own retries. Pick one layer. One field either way.

### 1.2 Wrapping a `userError` silently discards the whole payload

`main.go:63` (`errorDetails`) and `main.go:90` (`errorCode`) interrogate errors with direct
interface type assertions:

```go
if um, ok := err.(interface{ UserMessage() string }); ok {
```

A type assertion does not traverse `Unwrap`. The moment a `userError` is wrapped — say
`fmt.Errorf("outer: %w", ue)` — the user-facing message, detail, suggestion, namespaced code
**and** exit code are all lost, and output degrades to `An unknown error occurred.` with
exit 1.

*Confirmed by execution*, not by reading: a scratch test in `internal/cmd` built a `userError`
via the builder, wrapped it once with `fmt.Errorf`, and all five accessors failed to resolve.

Cobra does not currently wrap `RunE` errors, so this is latent rather than live. It is a trap
for the next person who adds context to an error on the way out.

**Fix:** `errors.As` with an interface target at each of the five assertion sites in `main.go`.

### 1.3 Git helpers run against the process working directory

`internal/gitutil/gitutil.go:37` (`CurrentBranch`), `:51` (`IsBranchPushed`), `:62`
(`MergeBase`) and `:88` (`GeneratePatch`) shell out to `git` with no `-C`, no `cmd.Dir` and no
`context.Context`. They take no directory argument at all, so they operate on whatever the
process cwd happens to be.

Their callers in `internal/sidecar/sync.go` have both a `ctx` and an explicit repo path in
hand. So `chunk validate --project /somewhere/else`, on the `--checkout` sync path, diffs the
wrong repository.

Siblings in the same file get this right: `HeadRefCtx:123` and `TopLevelCtx:139` both take a
`ctx` and a directory.

**Fix:** thread `ctx` and a `dir` through all four. That also closes 1.4, 1.5 and 3.6.

### 1.4 `GeneratePatch` mutates the user's index and hides the cleanup failure

`internal/gitutil/gitutil.go:88` stages untracked files with `git add -N` so they appear in the
diff, then restores state in a `defer`:

```go
defer func() {
    args := append([]string{"reset", gitHEAD, "--"}, untracked...)
    _ = exec.Command("git", args...).Run()
}()
```

The error is discarded. If the reset fails — or the process is killed between the two commands
— the user is left with intent-to-add entries in their real index and nothing reported. On a
repository with no commits, `git reset HEAD --` fails outright.

**Fix:** surface the reset error (join it with the return error), and scope the operation to an
explicit directory per 1.3.

### 1.5 `MergeBase` reports a cause it has not established

`internal/gitutil/gitutil.go:62` reassigns `err` partway through, discarding the actual
`git merge-base` failure, then reports "no upstream tracking branch or origin/HEAD found" — a
diagnosis that can be flatly wrong about what went wrong.

**Fix:** keep the original error and wrap it with `%w`.

### 1.6 A locked keychain is reported as "no credential stored"

`internal/keyring/keyring.go:48` (`Get`) collapses **every** failure into `ErrNotFound`:
keychain locked, D-Bus absent, permission denied, operation timed out. Callers in
`internal/config/config.go` cannot tell "nothing saved" from "keychain unreachable", so the
user is told to authenticate again when the credential is sitting right there.

**Fix:** return a distinct wrapped error for operational failures and only map genuine absence
to `ErrNotFound`.

### 1.7 Colour globals are raced by the spinner goroutine

`internal/ui/colors.go:13-14` declares `stdoutColorEnabled` and `stderrColorEnabled` as plain
package variables. Exported `SetColorEnabled` (`:28`) writes both with no synchronisation, and
`Spinner.render` (`internal/ui/spinner.go:112`) reads `stderrColorEnabled` via `ErrDim` — from
the goroutine started at `internal/ui/spinner.go:54`.

Unsynchronised read/write on a shared global, detectable under `-race`.

**Fix:** an `atomic.Bool` per stream, or route colour state through a struct the caller owns.

### 1.8 Colour detection cannot be influenced by tests

The same two variables at `internal/ui/colors.go:13-14` are initialised at **package load**,
and `detectColorFor` (`:17`) calls `os.Getenv` there. `t.Setenv` can therefore never affect
them. Two test files then reset the baseline in opposite directions —
`internal/ui/colors_test.go:6` to `false`, `internal/cmd/init_test.go:459` to `true` — leaving
the package baseline dependent on execution order.

**Fix:** resolve colour lazily on first use, or inject it.

### 1.9 Abandoned stdin reader steals later input

`internal/oauth/login.go:107` (`waitForEnter`) starts a goroutine holding
`bufio.NewReader(os.Stdin)` and abandons it when `ctx` is done. The reader has already buffered
whatever it consumed, so a subsequent prompt in the same process loses that input.

**Fix:** share one buffered stdin reader for the process lifetime, or read with a cancellable
wrapper.

### 1.10 Login teardown has no deadline

`internal/oauth/callback.go:71` calls `srv.Shutdown(context.Background())`. A browser
keep-alive connection blocks teardown of the login flow indefinitely.

**Fix:** `context.WithTimeout`.

### 1.11 `chunk upgrade` cannot be interrupted or timed out

`internal/upgrade/upgrade.go:68` builds requests with `http.NewRequest` (not
`NewRequestWithContext`), takes no `ctx`, and is handed `http.DefaultClient` — which has no
`Timeout` — from `internal/cmd/upgrade.go`. An unresponsive GitHub hangs the command with no
way out. See also 3.7.

**Fix:** route through `internal/httpcl`, which every other caller already uses.

### 1.12 Error classification by substring

Two places decide behaviour by grepping error text rather than inspecting typed errors:

- `main.go:98` (`errorSuggestion`) matches `"401"`, `"authentication"`, `"dial tcp"`.
- `internal/github/retry.go:29` (`isRetryable`) matches `"timeout"`, `"ETIMEDOUT"`,
  `"invalid character '<'"`.

`circleci.StatusError` and `httpcl.HTTPError` both exist and carry status codes. Substring
matching breaks silently whenever an upstream message is reworded, and the repo enables
`errorlint` precisely to discourage this class of thing.

**Fix:** switch on the typed errors.

### 1.13 `config.Resolve` returns a half-built config with a discarded error

`internal/config/config.go:304` captures the error from `Load()`, carries on building `rc`
regardless, and returns `rc, err` at the end. Its sibling `ResolveCircleCI`
(`internal/config/config.go:346`) returns early on the identical failure — the two constructors
disagree about whether a config-load failure is fatal.

Every call site then discards it: `rc, _ := config.Resolve(...)`, 27 times across
`internal/cmd`. A corrupt `~/.config/chunk/config.json` produces a partially populated config
and no diagnostic anywhere.

**Fix:** make the two agree, then handle the error at the call sites.

### 1.14 `--insecure-storage` is a dead parameter on 27 call sites

`internal/config/config.go:304` is declared:

```go
func Resolve(flagAPIKey, flagModel string, _ bool) (ResolvedConfig, error)
```

The third parameter is discarded in the signature. Same at `ResolveCircleCI(_ bool)`
(`internal/config/config.go:346`). Twenty-seven call sites pass `insecureStorage` into it.

Reads are not *wrong* — the resolution order already prefers the config file over the keychain
— but the API tells 27 call sites they are selecting behaviour they are not.

**Fix:** drop the parameter, or honour it.

### 1.15 JSON:API decoding by type-assertion ladder silently drops fields

`internal/circleci/client.go:80` types the resource envelope as `Attributes any` /
`References any`. `ListSidecars` (`:122-140`) and `ListSnapshots` (`:540-558`) therefore
hand-roll nested `map[string]any` assertion ladders to get at fields.

The package **already has the right typed structs**: `sidecarAttrs:93`, `orgRefs:103`,
`commandAttrs:450`, `snapshotAttrs:484`, `instanceRefs:458`.

This is worse than verbose. An assertion ladder drops a field silently when the type does not
match; a typed decode errors. The two blocks are also a confirmed 19-line duplicate.

**Fix:** decode into the existing typed structs.

---

## 2. Two implementations that disagree

### 2.1 Stack detection exists twice and produces different commands

Two independent detectors map project marker files to test commands:

| | `internal/validate/setup.go:26` | `envbuilder/envbuilder.go:1219` |
|---|---|---|
| shape | flat `switch` on marker files | scored detection, workspace aware |
| Python | `pytest` | `uv run pytest` / `pipenv run pytest` / `pytest` by lockfile |
| Go | `go test ./...` | `go test -p 1 ./...` |
| Rust | `cargo test` | `cargo test --workspace` with excludes |

So on a `uv` project `chunk init` writes `pytest` while the sidecar image builds
`uv run pytest`. Which answer a user gets depends on which command they ran. This is the
finding most likely to be reported as a bug by someone who has no idea two detectors exist.

**Fix:** `validate.DetectCommands` should call the envbuilder detector. Removes roughly 160
lines and the divergence with it.

### 2.2 The session/branch key is derived in two production places

The same derivation — `sha256(sessionID + ":" + branch)`, first four bytes as hex — appears at:

- `internal/sidecar/active.go:64` — names the **state file**
- `internal/cmd/validate.go:982` — names the **sidecar**

Change the scheme in one and the state file silently stops corresponding to the sidecar it
points at.

**Fix:** one exported `sidecar.SessionBranchKey(sessionID, branch)`.

### 2.3 `chunk skill list` prints a path that does not exist

`internal/skills/install.go:47` hard-codes the description string
`"...review standards from .chunk/review-prompt.md."`. The real path, everywhere else in the
repo — all five `SKILL.md` files, `AGENTS.md`, and the output of `chunk build-prompt` — is
`.chunk/context/review-prompt.md`.

The underlying cause is that skill name and description are declared **twice**:
`internal/skills/install.go:40` in Go, and the `description:` frontmatter of each
`skills/<name>/SKILL.md`. Both are embedded in the binary. They have already diverged in
trigger phrases too: the Go copy for `chunk-sidecar` is missing "sidecar dev loop", "validate
remotely", "scaffold test-suites.yml" and "set up smarter testing".

**Fix:** parse the frontmatter from `skills.Content` and delete the Go copies.

### 2.4 "Find the project root", five ways [swept]

- `internal/gitutil/gitutil.go:21` — `RepoRoot`, walks up looking for `.git`
- `internal/gitutil/gitutil.go:139` — `TopLevelCtx`, shells `rev-parse --show-toplevel`
- `internal/gitutil/fingerprint.go:77` — a third path to the same answer
- `internal/sidecar/active.go` — re-walks for `.git`, falls back to cwd
- `internal/cmd/hook.go:31` — `resolveHookRoot`, shells out, falls back to cwd

`internal/config/paths.go` documents the hazard in a comment: two of these must hash to the
same string, and nothing enforces it.

**Fix:** one function in `gitutil` with the cwd fallback; the other callers use it.

### 2.5 `isRetryable` twice, same name, different contract

`internal/github/retry.go:29` is a retry policy for idempotent requests.
`internal/circleci/client.go:436` is a resume policy for a cursor-based stream. Both are
correct for their own purpose; sharing a name across sibling packages invites someone to
"unify" them.

**Fix:** rename to say what each decides, e.g. `shouldRetryRequest` and `shouldResumeStream`.

---

## 3. Duplication

Ordered by how much code the fix removes.

### 3.1 The auth provider matrix, written out 18 times

Three providers (CircleCI, Anthropic, GitHub) times six operations, hand-written across three
files:

| operation | sites |
|---|---|
| `authSet*` | `internal/cmd/auth.go:142`, `:201`, `:592` |
| `authRemove*` | `internal/cmd/auth.go:466`, `:529`, `:666` — around 63 lines **each** |
| `hasStored*` | `internal/cmd/auth.go:439`, `:448`, `:457` — 9 lines each, differing only in the keyring service function and the config field |
| `ensure*Client` | `internal/cmd/authhelper.go:57`, `:143`, `:205` |
| `Save*` | `internal/authprompt/authprompt.go:123`, `:140`, `:157` |
| `Validate*` | `internal/authprompt/authprompt.go:25`, `:47`, `:174` |

`internal/cmd/auth.go` is 727 lines and almost all of it is this. The `Save*` trio is a
confirmed three-way duplicate; `internal/authprompt/authprompt.go:85-94` and `:110-119` are
another confirmed pair.

**Fix:** one descriptor plus a table:

```go
type provider struct {
    label       string
    envVars     []string
    service     func(baseURL string) string
    configField func(*config.UserConfig) *string
    validate    func(ctx context.Context, cred, baseURL string) error
}
var providers = []provider{ /* circleci, anthropic, github */ }
```

Adding a fourth provider becomes one table entry instead of six hand-written functions.
Largest single win in the repo, and it wants its own PR.

### 3.2 `ExecOverSSH` boilerplate, 17 times in one file [swept]

The shape `ExecOverSSH` then check `err` then check `ExitCode != 0` then wrap, repeats 17 times
in `internal/sidecar/sync.go`, plus 3 in `internal/variants/variants.go` and 1 in
`internal/cmd/sidecar.go`. `initRemoteWorkspace` and `syncWorkspace` also repeat the same
mkdir-parent-then-test-directory pair verbatim.

**Fix:** `sidecar.mustExec(ctx, sess, label, cmd, stdin)` in `internal/sidecar/ssh.go`.
Around 90 lines.

### 3.3 `writeSettings` and `writeCodexHooks` are the same 90 lines twice

`internal/cmd/init.go:43` and `internal/cmd/init.go:151` differ only in the build function,
directory name, file name, merge function, example-writer function and two blurb strings. The
doc comment on the second one admits it: "Uses the same merge/confirm/fallback pattern as
writeSettings."

The coloured unified-diff printing loop is verbatim in both.

**Fix:** one helper taking a small descriptor. Move the diff-printing loop into `internal/ui`.

### 3.4 Git-repo test setup, five copies [swept]

`internal/gitutil/gitutil_test.go:14` and `:87`, `internal/cmd/init_test.go:343`,
`internal/cmd/validate_test.go:461` (plus the same `run := func(args ...string)` closure
re-declared at `:493`, `:521`, `:555`, `:572`), and `acceptance/validate_test.go:604`.

`internal/testing/gitrepo` already provides this and `internal/sidecar/sync_test.go` already
uses it.

**Fix:** everyone uses `internal/testing/gitrepo`. Around 130 lines.

### 3.5 Bespoke session store beside a generic one [swept]

`internal/validate/attempts.go:19` hand-rolls read/increment/write/delete of a JSON file keyed
by session ID under `os.TempDir()`. `internal/filecache` already provides a generic
`FileCache[T]` with atomic write-then-rename and age sweeping — used by
`internal/cmd/validate.go:1023`. The hand-rolled version also breaks the storage convention:
everything else lives under `config.ProjectDataDir`.

**Fix:** `filecache.FileCache[attemptsState]` rooted at `config.ProjectDataDir`.

### 3.6 The three keyring timeout wrappers

`internal/keyring/keyring.go:48`, `:70` and `:84` (`Get`, `Set`, `Delete`) are three copies of
the same goroutine plus `select` plus `time.After` wrapper. None takes a `ctx`, so the signal
context wired up in `main.go` cannot cancel them, and `time.After` leaks its timer at all three
sites.

`internal/variants/variants.go` already does this correctly: `time.NewTimer` with
`defer timer.Stop()` and a `ctx.Done()` case.

**Fix:** one generic helper following the `variants` pattern. Also closes 1.6's ctx half.

### 3.7 Two ad-hoc HTTP paths beside the canonical client

`internal/upgrade/upgrade.go:68` takes a raw `*http.Client` and hand-builds requests with
manual status checks. `internal/oauth/exchange.go:36` uses `http.DefaultClient`, so no timeout
at all. Every other caller — `circleci`, `github`, `anthropic`, `envbuilder` — goes through
`internal/httpcl`.

**Fix:** both onto `httpcl`. `oauth` needs a form-body option adding to
`internal/httpcl/request.go`. See 1.11.

### 3.8 `mapErr` written three times [swept]

`internal/github/client.go:94`, `internal/anthropic/client.go:115` (identical bar one guard),
and `internal/circleci/client.go:575` (the same core plus 401/403 and 410 handling).

**Fix:** `httpcl.MapErr(op, err)`; `circleci` keeps only its extra branches.

### 3.9 One text-input model, implemented twice

`internal/tui/input.go:38` (`hiddenInputModel`) and `internal/tui/text.go:33`
(`textInputModel`) have identical `Init` and `Update`. Confirmed duplicate, 22 lines.

**Fix:** one model with a `hidden bool`.

### 3.10 Env var names declared twice, 16 literals each [swept]

`internal/config/config.go:52-77` declares them as exported constants; `:87-102` declares the
same 16 names again as `env:` struct tags, with nothing tying the two together.

Three call sites also bypass the constants that already exist:
`internal/cmd/sidecar.go:980` uses `"SSH_AUTH_SOCK"`, `internal/cmd/upgrade.go:22` uses
`"GITHUB_API_URL"`, `internal/cmd/usererr.go:228` uses `"CI"`.

Three more have no constant at all: `CHUNK_TELEMETRY_LOG` (`internal/cmd/root.go`),
`CHUNK_INSTALL_PATH` (`internal/cmd/upgrade.go`), `CHUNK_SIDECAR_HOME`
(`internal/sidecar/sync.go`).

**Fix:** constants win; `LoadEnv` populates via explicit `os.Getenv(EnvX)` lookups.

### 3.11 Default base URLs in four places [swept]

`internal/config/config.go` (envconfig tag defaults), `internal/authprompt/authprompt.go:27`,
`:49`, `:176` (independent fallbacks), `internal/cmd/upgrade.go` (`https://api.github.com`
again), and `internal/cmd/root.go:59-61` help text repeating all three — which means the help
text can lie.

**Fix:** exported `DefaultCircleCIBaseURL` / `DefaultAnthropicBaseURL` /
`DefaultGitHubAPIURL` in `internal/config`.

### 3.12 Smaller confirmed duplicates

| what | sites | canonical |
|---|---|---|
| `FormatDuration` / `formatElapsed`, character-identical | `internal/ui/format.go:35`, `internal/validate/validate.go:18` | `ui.FormatDuration` (already imported by `internal/cmd/validate.go`) |
| POSIX single-quote escape, identical bodies, one test each | `internal/sidecar/ssh.go:35`, `internal/validate/validate.go:47` | one of them; no import cycle blocks it |
| `.chunk` as a bare literal | `internal/cmd/hook.go:54`, `:74`, `internal/config/project.go:57`, `:168`, `internal/task/config.go:39`, `:70`, `:97`, `internal/validate/validate.go:315` | a constant plus path helpers in `internal/config` |
| `hooks-disabled` sentinel path built independently by writer and reader | `internal/cmd/hook.go:54`, `internal/validate/validate.go:315` | one function |
| ed25519 keypair generation | `internal/sidecar/session.go:40`, `internal/testing/fakes/ssh.go:41`, `acceptance/validate_test.go:213` | first two; delete the third |
| `iostream.Level` to string | `internal/eventlog/eventlog.go`, `internal/tui/watch/model.go` (with a raw `"warn"` literal), `internal/validate/validate_test.go` | a `String()` method on `iostream.Level` |
| recorded-request filters | `acceptance/sidecar_test.go:691`, `acceptance/validate_variants_test.go:49` (byte-identical), `acceptance/build_prompt_test.go:1377`, `:1387` | methods on `internal/testing/recorder` |
| `isolateConfig` + `randToken` | `internal/authprompt/authprompt_test.go:20`, `:28`, `internal/cmd/authhelper_test.go:23`, `:30` | `internal/testing/env` |
| verbatim block | `internal/cmd/auth.go:328-344` and `:349-365` | — |
| verbatim block | `internal/testing/fakes/github.go:94-104` and `:128-138` | — |

---

## 4. Structure

### 4.1 `envbuilder` is three parallel switches over one axis

`envbuilder/envbuilder.go` is 2710 lines in a single file with two `//nolint:gocyclo`
suppressions. The root cause of both: **31 stack-conditional branches over the same 14 stack
constants** (`envbuilder/envbuilder.go:22-36`), spread across exactly three functions.

| function | lines | stack branches |
|---|---|---|
| `detectCommands` `:1219` | 378 | 13 |
| `dockerfileContent` `:444` | 271 | 9 |
| `detectImageVersion` `:2449` | 118 | 9 |

Plus 29 loose `detect*` helpers, most of them per-stack: `detectNodeTestCommand`,
`detectGradleJavaVersion`, `detectSBTTestCommand`, `detectDotNetVersion`,
`detectElixirVersionFromCI`, `detectHaskellGHCVersionFromCI` and so on.

**Fix: split by stack, not by phase.**

```go
type stack interface {
    name() string
    indicators() []string
    commands(dir string) (install, test string, extra []string)
    dockerfileSteps(dir string, env *Environment) []string
    imageVersion(ctx context.Context, c *hc.Client, dir, install string) (string, error)
}
var stacks = []stack{python{}, golang{}, node{}, java{}, rust{} /* ... */}
```

One file per stack (`stack_python.go`, `stack_java.go`, …), roughly 100-200 lines each, each
owning its own `detect*` helpers. Both `nolint:gocyclo` disappear because every method lands at
20-40 lines. Adding a fifteenth language becomes one new file instead of edits to three giant
switches in three separate places.

### 4.2 `internal/config` is not the leaf it is documented to be

`docs/ARCHITECTURE.md:64` states:

> `config/` is a leaf — no imports from other `internal/` packages

`internal/config/config.go` imports `internal/keyring`. That dependency is what turned a pure
resolver into something that performs blocking inter-process communication on every `Resolve`
call, and it is the root cause of the ctx-less keyring problem in 3.6 rather than a separate
issue.

**Fix:** invert it — the caller reads the keychain and passes credentials in — or amend
`ARCHITECTURE.md` to describe what is actually true.

### 4.3 `internal/cmd/sidecar.go` is 1294 lines

Twenty cobra constructors plus setup orchestration (`sidecarSetupResolveSidecar`,
`sidecarSetupSync`, `sidecarSetupRunSetup`) in one file.

**Fix:** split into `sidecar_setup.go` / `sidecar_snapshot.go` / `sidecar_env.go`, and move the
orchestration down into `internal/sidecar` where it can be tested without cobra.

### 4.4 The `userError` error code is empty for ~85% of errors

`internal/cmd` constructs `userError` two ways: 26 builder calls and 143 struct literals.
**Not one** of the 143 struct literals sets `code:`, so the namespaced `ErrorCode` that
`main.go:90` reads is empty for the overwhelming majority of user-facing errors.

**Fix:** pick one construction style. If the code field is meant to be machine-parseable, make
it required by construction.

### 4.5 `HeadRef` / `HeadRefCtx` twins

`internal/gitutil/gitutil.go:117` and `:123`. One call site each
(`internal/sidecar/sync.go` and `internal/tui/watch/model.go`), so collapsing to a single
ctx-taking function is a two-line change.

### 4.6 Naming that stutters against the repo's own rule [swept]

`AGENTS.md` requires no stuttering (`pipeline.ID`, not `pipeline.PipelineID`). Current:
`config.ProjectConfig`, `config.UserConfig`, `config.ResolvedConfig`, `config.ValidationConfig`,
`config.VCSConfig`, `config.LoadProjectConfig`, `config.SaveProjectConfig`. Also
`filecache.FileCache`, `sidecar.ActiveSidecar`, `skills.SkillState`,
`telemetry.DisableTelemetry`.

**Fix:** `config.Project`, `config.User`, `config.Resolved`, `config.LoadProject`, and so on.
Mechanical but wide, so it wants its own PR.

### 4.7 Test-only mutable package variables [swept]

`maxStreamAttempts` and `streamRetryBase` (`internal/circleci/client.go:219`, `:224`) and
`maxDigestBytes` (`internal/gitutil/fingerprint.go:23`) are package `var`s solely so tests can
overwrite them. Any future `t.Parallel()` in those packages races on them.

**Fix:** make them struct fields with a test constructor.

### 4.8 `interface{}` versus `any`

`internal/settings/merge.go` uses `interface{}` 36 times; the rest of the repo uses `any` 88
times. Purely mechanical.

---

## 5. Linter configuration

The linter runs clean today, which makes the gaps in its configuration the reason several
findings above went unnoticed.

- **`dupl` is not enabled.** It independently confirms 3.9, 3.1's `Save*` trio, 1.15, and both
  verbatim blocks in 3.12. Enabling it would have caught them at review time.
- **`gocyclo` has no `min-complexity`** (`.golangci.yml:167`), so it defaults to 30. That is why
  a 378-line function needs only a `nolint` comment.
- **`funlen` is excluded for tests at `.golangci.yml:120` but never enabled.** Dead
  configuration.
- **`govet` is disabled wholesale for `_test.go`** (`.golangci.yml:118-127`), which switches off
  `printf` checking across the entire test suite.
- **Eight per-path `goconst` exclusions**, which whitelist repeated domain literals rather than
  hoisting them. 3.10 and 3.11 live inside those exclusions.

Suggested: enable `dupl`, set `gocyclo` `min-complexity`, enable `funlen` or drop its dead
exclusion, and narrow the `govet` test exclusion to the specific checks that are noisy.

---

## 6. Tests

### 6.1 The envbuilder end-to-end suite never runs

`acceptance/sidecars_build_e2e_test.go` gates the whole suite on
`CHUNK_ENV_BUILDER_ACCEPTANCE`, which is set **nowhere** in `.circleci/config.yml` or
`Taskfile.yml`. The largest and most branch-heavy file in the repo has 56.1% unit coverage and
no end-to-end coverage in CI at all.

**Fix:** either run it on a schedule with the variable set, or delete it so the gap is visible.

### 6.2 `task test` can pass from cache

`Taskfile.yml:19` runs `gotestsum -- -race` with no `-count=1`; `ci:test` has both. The repo
embeds `skills/*.md`, so a local run can report PASS from cache after a change that CI will
then fail on.

**Fix:** add `-count=1` to `task test` and `task acceptance-test` (`Taskfile.yml:59`).

### 6.3 Coverage concentrates away from where the logic is

`internal/cmd` 35.5% across 8181 lines, `internal/tui/watch` 17.9%, `internal/ui` 37.8%,
`internal/validate` 50.8%, `envbuilder` 56.1%.

Acceptance tests drive the real binary, so effective `internal/cmd` coverage is higher than the
unit figure suggests. The low number is a symptom rather than a finding in itself: logic sitting
in cobra wrappers is awkward to unit test, so 3.1 and 4.3 raise coverage as a side effect.

---

## 7. Documentation

`docs/CLI.md` was checked mechanically: the real cobra tree was enumerated from a built binary
and all 103 command/flag pairs compared against the document. It is accurate apart from the
following.

- **`chunk sidecar delete` is absent entirely.** The tree jumps from `create`
  (`docs/CLI.md:93`) to `use` (`:97`). It is the one destructive sidecar command, and it is the
  one that is undocumented. Real flag: `--sidecar-id`.
- **Three flags appear nowhere:** `init --skip-git-hook`, `init --skip-org-id`, and
  `skill --user` (on both `install` and `list`).
- **`--insecure-storage` is documented but hidden** at `internal/cmd/root.go:93`
  (`MarkHidden`), and the document does not say so, which makes it read as discoverable.
- **`docs/SKILLS.md` tells users to run `cat ~/.claude/skills/chunk-review.md`.** Installs write
  `~/.claude/skills/<name>/SKILL.md` — the directory form, as the CI smoke test in
  `.circleci/config.yml` confirms. The documented command fails as written.
- **Two production env vars are undocumented** in the `ARCHITECTURE.md` list:
  `CHUNK_INSTALL_PATH` (`internal/cmd/upgrade.go`) and `CHUNK_SIDECAR_HOME`
  (`internal/sidecar/sync.go`).

A caution for anyone repeating this check: `docs/CLI.md` renders as a tree diagram, so
`chunk auth status` appears as `│   ├── status`. A naive substring search reports 16 false
"missing command" hits. All 16 were verified present by hand.

---

## 8. Checked and clean

Recorded so nobody spends time re-checking.

- **Layering holds.** No `internal/*` package imports `internal/cmd`; only `main.go` does. No
  production package imports `internal/testing/*`. No business package imports `internal/ui`.
  `internal/httpcl` has no internal imports.
- **Output discipline holds.** Domain packages write to an injected `iostream` or a
  `strings.Builder`, never `os.Stdout` directly.
- **No sentinel errors compared with `==`** anywhere. No genuine `defer` inside a loop body.
- **Test skips are all justified**: 12 in total, covering root-bypasses-permissions, a missing
  `op`/docker/git binary, and the opt-in end-to-end suite.
- **`internal/gitutil` is not over-exported**: 10 exported functions, every one used.
- **One stale TODO** in the tree, at `acceptance/sidecar_test.go:501`.
- `internal/cmd/validate.go:67`'s `_ = json.NewDecoder(r).Decode(&p)` is correct by design — a
  non-hook invocation with piped stdin decodes to nothing and the empty `SessionID` is the
  signal.
- Best-effort silent renames in `internal/config/paths.go` and `internal/eventlog/eventlog.go`
  are deliberate and commented.
- Goroutines in `internal/variants/variants.go` and `internal/cmd/task.go` are correct and
  well-commented; `watchWindowSize` in `internal/sidecar/terminal.go` owns its signal channel
  and lifecycle properly.
- `internal/sidecar/env.go` is the single source for env-pair and dotenv parsing, and
  `internal/cmd/env.go` delegates to it correctly.
- `internal/testing/fakes` is the only set of fake CircleCI/GitHub/Anthropic/SSH servers; there
  is no second implementation.
- `internal/cmd/validate.go:74` swallowing a config-load error into an empty config is real but
  **already addressed by #497** — do not fix it twice.

---

## 9. Suggested order

Cheap and high value first, so the linter starts catching the rest.

1. `DisableRetries: true` — 1.1. One field.
2. `errors.As` in `main.go` — 1.2. Five call sites.
3. Linter configuration — section 5. Enables `dupl` to police the rest.
4. `-count=1` on `task test` — 6.2.
5. `docs/CLI.md` and `docs/SKILLS.md` corrections — section 7.
6. Small duplicate collapses — 3.9, 3.12's `FormatDuration` and shell-escape rows.
7. Thread `ctx` and `dir` through `gitutil` — closes 1.3, 1.4, 1.5 and 4.5 together.
8. Keyring: error classification plus the generic wrapper — 1.6 and 3.6.
9. Reconcile the two stack detectors — 2.1. Own PR.
10. The auth provider table — 3.1. Own PR.
11. Split `envbuilder` by stack — 4.1. Own PR.
