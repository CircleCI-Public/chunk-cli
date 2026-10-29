# Code audit

A point-in-time review of `chunk-cli` for correctness, duplication and adherence to the
conventions in [AGENTS.md](../AGENTS.md) and [ARCHITECTURE.md](ARCHITECTURE.md).

**Originally audited at:** `6bb97f9` (main, after #505 merged).

**Re-audited at:** `0f3882a` (main, 2026-09-22). Every `file:line` below was re-derived
against that commit. Line numbers drift — treat them as a starting point, not a contract.

**Findings re-checked at:** `13eeff0` (main, 2026-10-20). Every open finding was re-read
against the code and every `file:line` re-derived. Closed since the previous check: 3.3, 2.3's
headline path, 3.12's `hooks-disabled` sentinel, and the swallowed config-load error in
section 8. Half closed: 6.2, 6.3, and three of 3.12's rows. Everything else holds, and five
findings have got *worse* on their own terms — see section 9.

Findings marked **[fixed]** are resolved; where a specific commit is known it is cited.
Everything else is still open. A count in a finding (18 sites, 143 literals) is the figure at
the audit that first recorded it, with the current figure beside it where the two differ —
the drift is often the point.

## How to read this

Findings are grouped by kind and ordered by severity within each group. Each one gives the
defect, why it matters, and the fix. Where a finding is a symptom of a deeper one it says so,
so the same thing does not get fixed twice.

Items marked **[swept]** came from a broad automated sweep rather than from reading each site.
Everything else was confirmed by reading the code; every `file:line` in the document was
re-derived at the most recent re-check. Two findings were confirmed by running code rather than
reading it, and say so explicitly.

---

## 1. Correctness

### 1.1 Nested retry gives 12 HTTP attempts per GraphQL query

`internal/github/client.go:37` constructs the shared HTTP client without `DisableRetries`, so
`internal/httpcl` retries three times internally (`internal/httpcl/client.go:177`,
`RetryMax = 3`, roughly 1s + 2s + 4s of backoff). `internal/github/retry.go:75`
(`doWithRetry`) then wraps that in **another** three attempts with its own 2s/4s backoff.

A GitHub 5xx therefore costs up to 12 HTTP attempts and around 27 seconds **per query**, and
`build-prompt` issues many. Retry amplification also multiplies load on the failing service.

**Fix:** `DisableRetries: true` on the `hc.Config` at `internal/github/client.go:37`, or delete
`doWithRetry` and let `httpcl` own retries. Pick one layer. One field either way.

### 1.2 Wrapping a `userError` silently discards the whole payload

`main.go:63` (`errorDetails`) and `main.go:90` (`errorCode`) interrogate errors with direct
interface type assertions (five assertion sites in `errorDetails` at lines 68–84, one in
`errorCode` at line 91):

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

### 1.3 Git helpers run against the process working directory **[fixed]**

`CurrentBranch`, `IsBranchPushed`, `MergeBase`, and `GeneratePatch` no longer exist.
`CurrentBranch` was renamed `CurrentBranchIn(dir string)` and now uses `-C dir` correctly.
The other three were removed when the sidecar sync path switched from git-patch to
git-bundle and then rsync (PRs #442, #574; commits `80f2936`, `dc6d73f`). Findings 1.4 and
1.5 are closed by the same removals.

### 1.4 `GeneratePatch` mutates the user's index and hides the cleanup failure **[fixed]**

`GeneratePatch` was removed as part of the sync-path rewrite described in 1.3.

### 1.5 `MergeBase` reports a cause it has not established **[fixed]**

`MergeBase` was removed as part of the sync-path rewrite described in 1.3.

### 1.6 A locked keychain is reported as "no credential stored"

`internal/keyring/keyring.go:48` (`Get`) collapses **every** failure into `ErrNotFound`:
keychain locked, D-Bus absent, permission denied, operation timed out. Callers in
`internal/config/config.go` cannot tell "nothing saved" from "keychain unreachable", so the
user is told to authenticate again when the credential is sitting right there.

**Fix:** return a distinct wrapped error for operational failures and only map genuine absence
to `ErrNotFound`.

### 1.7 Colour global is raced by the spinner goroutine

`_colorEnabled` at `internal/ui/styles.go:22` is a plain package variable, `SetColorEnabled`
(`:63`) writes it without synchronisation, and `Spinner.render`
(`internal/ui/spinner.go:122`) reads it via `ErrDim` from the goroutine started at
`spinner.go:54`.

Unsynchronised read/write on a shared global, detectable under `-race`.

**Fix:** an `atomic.Bool`, or route colour state through a struct the caller owns.

### 1.8 Colour detection cannot be influenced by tests

`_colorEnabled` at `internal/ui/styles.go:22` is still initialised at **package load** via
`colorSupported()` which calls `os.Getenv`. `t.Setenv` can therefore never affect it. Two test
files still reset the baseline in opposite directions — `internal/ui/colors_test.go:6` to
`false`, `internal/cmd/init_test.go:495` to `true` — leaving the package baseline dependent on
execution order.

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

`internal/upgrade/upgrade.go:173` builds requests with `http.NewRequest` (not
`NewRequestWithContext`), takes no `ctx`, and is handed `http.DefaultClient` — which has no
`Timeout` — from `internal/cmd/upgrade.go:40`. An unresponsive GitHub hangs the command with
no way out. `downloadAndReplace` (`:209`) is worse still: a bare `client.Get(url)`. See also
3.7.

**Fix:** route through `internal/httpcl`, which every other caller already uses.

### 1.12 Error classification by substring

Two places decide behaviour by grepping error text rather than inspecting typed errors:

- `main.go:98` (`errorSuggestion`) matches `"authentication"`, `"401"`, `"dial tcp"`.
- `internal/github/retry.go:29` (`isRetryable`) matches `"timeout"`, `"ETIMEDOUT"`,
  `"invalid character '<'"` at `:42-57`, after a typed `hc.HTTPError` 5xx check — so the
  typed path and the substring path sit side by side in one function.

`circleci.StatusError` and `httpcl.HTTPError` both exist and carry status codes. Substring
matching breaks silently whenever an upstream message is reworded, and the repo enables
`errorlint` precisely to discourage this class of thing.

**Fix:** switch on the typed errors.

### 1.13 `config.Resolve` returns a half-built config with a discarded error

`internal/config/config.go:411` (`Resolve`) captures the error from `Load()` at `:412`, carries
on building `rc` regardless, and returns `rc, err` at `:446`. Its sibling `ResolveCircleCI`
(`internal/config/config.go:451`) returns early on the identical failure at `:452-455` — the two
constructors disagree about whether a config-load failure is fatal.

Nearly every call site then discards it: `rc, _ := config.Resolve(...)` at `internal/cmd/auth.go:55`,
`:72`, `:128`, `:570`, `:634`, `:706` and `:843`. Only `:464` keeps it. A corrupt
`~/.config/chunk/config.json` produces a partially populated config and no diagnostic
anywhere.

**Fix:** make the two agree, then handle the error at the call sites.

### 1.14 `--insecure-storage` is a dead parameter on 27 call sites **[fixed]**

`Resolve` and `ResolveCircleCI` now declare `insecureStorage bool` (not `_ bool`) and pass it
through to `resolveCircleCIToken`, `resolveAnthropicAPIKey`, and `resolveGitHubToken`, each of
which switches on it to bypass the keychain. The parameter is honoured.

### 1.15 JSON:API decoding by type-assertion ladder silently drops fields

`internal/circleci/client.go:85-87` types the resource envelope as `Attributes any` /
`References any`. `ListSidecars` (`:128-143`) and `ListSnapshots` (`:641-659`) therefore
hand-roll nested `map[string]any` assertion ladders to get at fields.

The package **already has the right typed structs** nearby in the same file, and already uses
them for the same resource: `CreateSidecar` (`:155`) decodes into `sidecarAttrs` / `orgUserRefs`.

This is worse than verbose. An assertion ladder drops a field silently when the type does not
match; a typed decode errors. The two blocks are also a confirmed 19-line duplicate.

**Fix:** decode into the existing typed structs.

---

## 2. Two implementations that disagree

### 2.1 Stack detection exists twice and produces different commands

Two independent detectors map project marker files to test commands:

| | `internal/validate/setup.go:205` | `envbuilder/envbuilder.go:1238` |
|---|---|---|
| shape | flat `switch` on marker files | scored detection, workspace aware |
| Python | `pytest` (`:239`) | `uv run pytest` on a `uv.lock` (`:1247`) |
| Go | `go test ./...` (`:229`) | `go test -p 1 ./...` (`:1298`) |
| Rust | `cargo test` (`:236`) | `cargo test --workspace` with excludes (`:1382`) |

So on a `uv` project `chunk init` writes `pytest` while the sidecar image builds
`uv run pytest`. Which answer a user gets depends on which command they ran. This is the
finding most likely to be reported as a bug by someone who has no idea two detectors exist.

**Fix:** `validate.DetectCommands` should call the envbuilder detector. Removes roughly 160
lines and the divergence with it.

### 2.2 The session/branch key is derived in two production places

The same derivation — `sha256(sessionID + ":" + branch)`, first four bytes as hex — appears at:

- `internal/sidecar/active.go:87`, in `sidecarFileName` (`:80`) — names the **state file**
- `internal/cmd/validate.go:1778-1779` — names the **sidecar**

Change the scheme in one and the state file silently stops corresponding to the sidecar it
points at.

**Fix:** one exported `sidecar.SessionBranchKey(sessionID, branch)`.

### 2.3 Skill name and description are declared twice **[path fixed]**

The bad path is gone: `internal/skills/install.go:47` now reads "review prompts in
`.chunk/reviews/`", which exists. Fixed by `2ab63f9` (#723), which folded `build-prompt` into
`factory bootstrap` and took `.chunk/context/review-prompt.md` with it.

The cause survives. Skill name and description are declared **twice**:
`internal/skills/install.go:40` in Go, and the `description:` frontmatter of each
`skills/<name>/SKILL.md`. Both are embedded in the binary. They have already diverged in
trigger phrases too: the Go copy for `chunk-sidecar` (`install.go:52`) is still missing
"sidecar dev loop", "validate remotely", "scaffold test-suites.yml" and "set up smarter
testing", all four of which are in `skills/chunk-sidecar/SKILL.md:3`.

**Fix:** parse the frontmatter from `skills.Content` and delete the Go copies.

### 2.4 "Find the project root", five ways [swept]

- `internal/gitutil/gitutil.go:21` — `RepoRoot`, walks up looking for `.git`
- `internal/gitutil/gitutil.go:139` — `TopLevelCtx`, shells `rev-parse --show-toplevel`
- `internal/gitutil/fingerprint.go:77` — a third path to the same answer
- `internal/sidecar/active.go` — re-walks for `.git`, falls back to cwd
- `internal/cmd/hook.go:31` — `resolveHookRoot`, shells out, falls back to cwd

The hashing half of this has since been closed: `internal/config/paths.go` now exports
`CanonicalProjectRoot`, whose comment states that anything keying state by project root must
go through it, and records the duplicate-rows bug that followed from two spellings of the same
repo hashing apart. What remains is the five ways of *finding* the root in the first place.

**Fix:** one function in `gitutil` with the cwd fallback; the other callers use it.

### 2.5 `isRetryable` twice, same name, different contract

`internal/github/retry.go:29` is a retry policy for idempotent requests.
`internal/circleci/client.go:504` is a resume policy for a cursor-based stream — it returns
true for any non-HTTP error. Both are
correct for their own purpose; sharing a name across sibling packages invites someone to
"unify" them.

**Fix:** rename to say what each decides, e.g. `shouldRetryRequest` and `shouldResumeStream`.

---

## 3. Duplication

Ordered by how much code the fix removes.

### 3.1 The auth provider matrix, written out 22 times

Four credential kinds (CircleCI, Anthropic, GitHub, and now Claude OAuth) times six
operations, hand-written across three files:

| operation | sites |
|---|---|
| `authSet*` | `internal/cmd/auth.go:157`, `:216`, `:299`, `:768` |
| `authRemove*` | `internal/cmd/auth.go:381`, `:633`, `:705`, `:842` — around 63 lines **each** |
| `hasStored*` | `internal/cmd/auth.go:599`, `:608`, `:619`, `:624` — 9 lines each, differing only in the keyring service function and the config field |
| `ensure*Client` | `internal/cmd/authhelper.go:60`, `:192`, `:254` |
| `Save*` | `internal/authprompt/authprompt.go:144`, `:161`, `:179`, `:185` |
| `Validate*` | `internal/authprompt/authprompt.go:28`, `:57`, `:202` |

`internal/cmd/auth.go` is 903 lines and almost all of it is this. The count has gone from 18 to
22 since the first audit, because a fourth credential kind cost six hand-written functions —
which is the finding. The `Save*` trio is a confirmed duplicate; the `Resolve*Client` trio at
`internal/authprompt/authprompt.go:95`, `:119` and `:131` is another.

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

Adding a credential kind becomes one table entry instead of six hand-written functions.
Largest single win in the repo, and it wants its own PR.

### 3.2 `ExecOverSSH` boilerplate, repeated at every call site [swept]

The shape `ExecOverSSH` then check `err` then check `ExitCode != 0` then wrap. `sync.go`, where
the first audit found 15 of these, was deleted in `c0246dc` (#667) and replaced by
`internal/sidecar/rsync.go`, which carries 9 of them (`:124`, `:219`, `:315`, `:322`, `:327`,
`:437`, `:444`, `:449`, `:460`). The rest are spread over `internal/variants/variants.go:186`,
`:262`, `:274`, `internal/sidecar/provision.go:140`, `internal/sidecar/writefile.go:27`,
`internal/sidecar/sidecar.go:56` and `internal/cmd/factorybootstrapsetup.go:357`.

**Fix:** `sidecar.mustExec(ctx, sess, label, cmd, stdin)` in `internal/sidecar/ssh.go`.
Around 90 lines.

### 3.3 `writeSettings` and `writeCodexHooks` are the same 90 lines twice **[fixed]**

Collapsed into exactly the shape this finding asked for: one `writeAgentFile` in
`internal/settings/write.go`, driven by an `agentFile` descriptor (`dir`, `name`, `example`,
`build`, `merge`), with `WriteClaude` and `WriteCodex` as two-line wrappers. The diff-printing
loop is now a single `confirmDiff` at `internal/cmd/init.go:45`.

### 3.4 Git-repo test setup, still duplicated [swept]

`acceptance/validate_test.go` was migrated to use `gitrepo.SetupGitRepo`. The unit-test copies
remain: `internal/cmd/init_test.go:388`, `internal/cmd/validate_test.go:742` (plus multiple
`run := func(args ...string)` closures re-declared at `:777`, `:805`, `:839`, `:856`).

`internal/testing/gitrepo` already provides this and is used by acceptance tests.

**Fix:** migrate `internal/cmd/init_test.go` and `internal/cmd/validate_test.go` to
`internal/testing/gitrepo`. Around 80 lines remaining.

### 3.5 Bespoke session store beside a generic one [swept]

`internal/validate/attempts.go:19` hand-rolls read/increment/write/delete of a JSON file keyed
by session ID under `os.TempDir()`. `internal/filecache` already provides a generic
`FileCache[T]` with atomic write-then-rename and age sweeping — used by
`internal/cmd/validate.go:1023`. The hand-rolled version also breaks the storage convention:
everything else lives under `config.ProjectDataDir`.

**Fix:** `filecache.FileCache[attemptsState]` rooted at `config.ProjectDataDir`.

### 3.6 The three keyring timeout wrappers

`internal/keyring/keyring.go:54`, `:76` and `:90` (`Get`, `Set`, `Delete`) are three copies of
the same goroutine plus `select` plus `time.After` wrapper (timers at lines `:70`, `:84`,
`:102`). None takes a `ctx`, so the signal context wired up in `main.go` cannot cancel them, and
`time.After` leaks its timer at all three sites.

`internal/variants/variants.go` already does this correctly: `time.NewTimer` with
`defer timer.Stop()` and a `ctx.Done()` case.

**Fix:** one generic helper following the `variants` pattern. Also closes 1.6's ctx half.

### 3.7 Two ad-hoc HTTP paths beside the canonical client

`internal/upgrade/upgrade.go:81` takes a raw `*http.Client` and hand-builds the request at
`:173` with manual status checks at `:185` and `:215`. `internal/oauth/exchange.go:36` uses
`http.DefaultClient`, so no timeout at all. Every other caller — `circleci`, `github`, `anthropic`, `envbuilder` — goes through
`internal/httpcl`.

**Fix:** both onto `httpcl`. `oauth` needs a form-body option adding to
`internal/httpcl/request.go`. See 1.11.

### 3.8 `mapErr` written three times [swept]

`internal/github/client.go:94`, `internal/anthropic/client.go:115` (identical bar one guard),
and `internal/circleci/client.go:650` (the same core plus 401/403 and 410 handling).

**Fix:** `httpcl.MapErr(op, err)`; `circleci` keeps only its extra branches.

### 3.9 One text-input model, implemented twice

`internal/ui/input.go:14` (`hiddenInputModel`) and `internal/ui/text.go:10`
(`textInputModel`) have character-identical `Init` and `Update` (`input.go:34-59`,
`text.go:29-54`); the structs differ only in name. (Paths changed from `internal/tui/` in
commit `33ee96a`; the duplicate itself was not resolved.) Confirmed duplicate, 26 lines.

**Fix:** one model with a `hidden bool`.

### 3.10 Env var names declared twice, 16 literals each [swept]

`internal/config/config.go:52-77` declares them as exported constants; `:87-102` declares the
same 16 names again as `env:` struct tags, with nothing tying the two together.

Three call sites also bypass the constants that already exist:
`internal/cmd/sidecar.go:980` uses `"SSH_AUTH_SOCK"`, `internal/cmd/upgrade.go:22` uses
`"GITHUB_API_URL"`, `internal/cmd/usererr.go:228` uses `"CI"`.

Three more have no constant at all: `CHUNK_TELEMETRY_LOG` (`internal/cmd/root.go`),
`CHUNK_INSTALL_PATH` (`internal/cmd/upgrade.go:27`), `CHUNK_SIDECAR_HOME`
(`internal/sidecar/workspace.go:13`).

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
| `FormatDuration` / `FormatElapsed`, character-identical — **one of the three went, two remain** | `internal/ui/format.go:35`, `internal/commandutil/commandutil.go:16` (`internal/validate/validate.go:21` now delegates) | still `ui.FormatDuration` |
| POSIX single-quote escape, identical bodies, one test each | `internal/sidecar/ssh.go:36`, `internal/validate/validate.go:50` | one of them; no import cycle blocks it |
| `.chunk` as a bare literal — **8 sites at the first audit, 11 now** | `internal/cmd/reviewdetach.go:279`, `internal/cmd/validate.go:524`, `internal/validate/validate.go:354`, `internal/chunkd/paths.go:107`, `internal/chunkd/server/load.go:101`, `internal/config/project.go:106`, `:211`, `internal/task/config.go:39`, `:70`, `:97`, `internal/sidecar/pool.go:100` | a constant plus path helpers in `internal/config` |
| ~~`hooks-disabled` sentinel path built independently by writer and reader~~ **[fixed]** | one `validate.HooksDisabledPaths` at `internal/validate/validate.go:347`; the writer (`internal/cmd/hook.go:61`) and remover (`:88`) both call it | — |
| ed25519 keypair generation — **3 sites at the first audit, 6 now** | `internal/sidecar/session.go:47`, `internal/testing/fakes/ssh.go:55`, `:96`, `:405`, `internal/sidecar/ssh_test.go:378`, `acceptance/validate_test.go:268` | the `sidecar` and `fakes` ones; the test copies should borrow |
| `iostream.Level` to string — still no `String()` on `Level` (`internal/iostream/status.go:4`) | `internal/eventlog/eventlog.go:360` (`levelStr`), `internal/factory/log.go:294` (`levelName`, raw `"warn"` at `:299`), `internal/validate/validate_test.go:70` | a `String()` method on `iostream.Level` |
| recorded-request filters — **partly consolidated** | the byte-identical pair survives at `acceptance/sidecar_test.go:855` (`filterByMethod`) and `acceptance/validate_variants_test.go:49` (`filterVariantRequests`); `acceptance/requests_test.go:9`, `:19` now share `filterByPath` / `filterByPathPrefix` | still `internal/testing/recorder`; the shared half went into the `acceptance` package instead |
| `isolateConfig` + `randToken` | `internal/authprompt/authprompt_test.go:21`, `:28`, `internal/cmd/authhelper_test.go:24`, `:69` | `internal/testing/env`, which exists and neither uses |
| verbatim block: already-stored / `force` / `nonInteractive` / `ui.Confirm("Do you want to replace it?")` / keeping-existing — **2 sites at the first audit, 4 now** | `internal/cmd/auth.go:175-190`, `:233-246`, `:321-335`, `:786-799` | part of 3.1 |
| verbatim block — **the 11-line pair went, a 4-line one spread** | the `authorization == ""` → 401 "Bad credentials" guard at `internal/testing/fakes/github.go:92`, `:102`, `:138`, `:160`, `:185` | one guard helper |

---

## 4. Structure

### 4.1 `envbuilder` is three parallel switches over one axis

`envbuilder/envbuilder.go` is 2756 lines in a single file with two `//nolint:gocyclo`
suppressions. The root cause of both: **stack-conditional branches over the same 14 stack
constants** (`envbuilder/envbuilder.go:21-35`, 113 references to them), spread across exactly
three functions.

| function | lines | nolint |
|---|---|---|
| `detectCommands` `:1238` | 331 | yes |
| `dockerfileContent` `:457` | 269 | yes |
| `detectImageVersion` `:2495` | 115 | no |

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

It imports three: `internal/keyring` (`internal/config/config.go:16`), `internal/envspec`
(`internal/config/project.go:12`) and `internal/gitexec` (`internal/config/paths.go:12`). The
keyring one is what turned a pure resolver into something that performs blocking inter-process
communication on every `Resolve` call, and it is the root cause of the ctx-less keyring problem
in 3.6 rather than a separate issue. The other two arrived after the first audit, so the gap
between the doc and the code is widening, not closing.

**Fix:** invert it — the caller reads the keychain and passes credentials in — or amend
`ARCHITECTURE.md` to describe what is actually true.

### 4.3 `internal/cmd/sidecar.go` is 1326 lines

Twenty cobra constructors plus setup orchestration in one file. 1294 lines at the first audit,
1326 now.

**Fix:** split into `sidecar_setup.go` / `sidecar_snapshot.go` / `sidecar_env.go`, and move the
orchestration down into `internal/sidecar` where it can be tested without cobra.

### 4.4 The `userError` error code is empty for ~80% of errors

`internal/cmd` constructs `userError` two ways: 69 builder calls (63 of which reach
`.withCode(`) and 248 struct literals. **Exactly one** of the 248 struct literals sets `code:`
(`internal/cmd/org.go:146`, `"org.none_found"`), so 64 of 317 constructions carry a code and
the namespaced `ErrorCode` that `main.go:91` reads is empty for about 80% of user-facing
errors. The type is at `internal/cmd/usererr.go:226`, `ErrorCode()` at `:290`.

**Fix:** pick one construction style. If the code field is meant to be machine-parseable, make
it required by construction.

### 4.5 `HeadRef` / `HeadRefCtx` twins

`internal/gitutil/gitutil.go:61` (`HeadRef`) is still a `context.Background()` wrapper over
`:67` (`HeadRefCtx`). The call sites this finding originally named are gone: the only remaining
`HeadRef` caller is `internal/gitutil/fingerprint.go:74`, and `HeadRefCtx` is called from
`internal/chunkd/server/conflicts.go:187` and `internal/chunkd/server/load.go:182`. One caller
is all that stands between the wrapper and deletion.

### 4.6 Naming that stutters against the repo's own rule [swept]

`AGENTS.md` requires no stuttering (`pipeline.ID`, not `pipeline.PipelineID`). Current:
`config.ProjectConfig`, `config.UserConfig`, `config.ResolvedConfig`, `config.ValidationConfig`,
`config.VCSConfig`, `config.LoadProjectConfig`, `config.SaveProjectConfig`. Also
`filecache.FileCache`, `sidecar.ActiveSidecar`, `skills.SkillState`,
`telemetry.DisableTelemetry`.

**Fix:** `config.Project`, `config.User`, `config.Resolved`, `config.LoadProject`, and so on.
Mechanical but wide, so it wants its own PR.

### 4.7 Test-only mutable package variables [swept]

`maxStreamAttempts` and `streamRetryBase` (`internal/circleci/client.go:224`, `:229`) and
`maxDigestBytes` (`internal/gitutil/fingerprint.go:23`) are package `var`s solely so tests can
overwrite them. Any future `t.Parallel()` in those packages races on them.

**Fix:** make them struct fields with a test constructor.

### 4.8 `interface{}` versus `any`

`internal/settings/merge.go` uses `interface{}` 37 times; the rest of the repo uses `any`.
Purely mechanical.

---

## 5. Linter configuration

The linter runs clean today, which makes the gaps in its configuration the reason several
findings above went unnoticed.

- **`dupl` is not enabled** — absent from the `enable:` list at `.golangci.yml:154-180`. It
  independently confirms 3.9, 3.1's `Save*` trio, 1.15, and both verbatim blocks in 3.12.
  Enabling it would have caught them at review time.
- **`gocyclo` has no `min-complexity`.** There is no `gocyclo:` key under `linters.settings`
  (`.golangci.yml:19-35`), only the enable entry at `:167`, so it defaults to 30. That is why a
  331-line function needs only a `nolint` comment.
- **`funlen` is excluded for tests at `.golangci.yml:120` but never enabled.** Dead
  configuration.
- **`govet` is disabled wholesale for `_test.go`** — listed at `.golangci.yml:124` inside the
  `path: _test\.go` block (`:116-125`), against `enable-all: true` at `:28`. That switches off
  `printf` checking across the entire test suite.
- **Eight per-path `goconst` exclusions** (`.golangci.yml:130`, `:132`, `:134`, `:136`, `:138`,
  `:140`, `:142`, `:146`), which whitelist repeated domain literals rather than hoisting them.
  3.10 and 3.11 live inside those exclusions, as do `envbuilder/`, `internal/config/config.go`
  and `internal/settings/`.

Suggested: enable `dupl`, set `gocyclo` `min-complexity`, enable `funlen` or drop its dead
exclusion, and narrow the `govet` test exclusion to the specific checks that are noisy.

---

## 6. Tests

### 6.1 The envbuilder end-to-end suite never runs

`acceptance/sidecars_build_e2e_test.go:92` gates the whole suite on
`CHUNK_ENV_BUILDER_ACCEPTANCE`. A repo-wide grep over `.go`, `.yml` and `.sh` finds that name
in that one file and nowhere else — nothing in `.circleci/config.yml` or `Taskfile.yml` sets
it. The largest and most branch-heavy file in the repo has 63.4% unit coverage and no
end-to-end coverage in CI at all.

**Fix:** either run it on a schedule with the variable set, or delete it so the gap is visible.

### 6.2 `task test` can pass from cache **[half fixed]**

`task acceptance-test` has been fixed: `Taskfile.yml:62` now runs `-race -count=1`, with a
comment saying why. `task test` has not: `Taskfile.yml:19` still runs `gotestsum -- -race`
with no `-count=1`. The repo embeds `skills/*.md`, so a local `task test` can still report PASS
from cache after a change that CI will then fail on.

**Fix:** add `-count=1` to `task test`, the same way `acceptance-test` now has it.

### 6.3 Coverage concentrates away from where the logic is **[largely closed]**

Most of this has reversed since the first audit:

| package | first audit | now |
|---|---|---|
| `internal/ui/watch` (was `internal/tui/watch`) | 17.9% | 90.2% |
| `internal/validate` | 50.8% | 81.5% |
| `internal/cmd` | 35.5% | 49.8% |
| `envbuilder` | 56.1% | 63.4% |
| `internal/ui` | 37.8% | 20.9% |

`internal/ui` went the other way, and `internal/ui/promptrows` is at 0.0%. `envbuilder` is still
the one that matters, because it is also the most branch-heavy file in the repo and its
end-to-end suite does not run (6.1).

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
  one that is undocumented. It lives at `internal/cmd/sidecar.go:306` and takes `--sidecar-id`
  and `--force`.
- **Three flags appear nowhere:** `init --skip-git-hook` (`internal/cmd/init.go:257`),
  `init --skip-org-id` (`:262`), and `skill --user` on both `install` and `list`
  (`internal/cmd/skills.go:80`, `:129`).
- **`--insecure-storage` is documented but hidden** at `internal/cmd/root.go:137-138`
  (`MarkHidden`), and `docs/CLI.md:222` does not say so, which makes it read as discoverable.
- **`docs/SKILLS.md:212-213` tells users to run `cat ~/.claude/skills/chunk-review.md`.**
  Installs write `filepath.Join(skillsDir, s.Name, "SKILL.md")`
  (`internal/skills/install.go:115`) — the directory form, as the CI smoke test at
  `.circleci/config.yml:90` confirms. The documented command fails as written.
- **Two production env vars are undocumented** in the `ARCHITECTURE.md` env tables (`:196-210`,
  `:352-355`): `CHUNK_INSTALL_PATH` (`internal/cmd/upgrade.go:27`) and `CHUNK_SIDECAR_HOME`
  (`internal/sidecar/workspace.go:13`).

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
- **Test skips are all justified**: 37 across 20-odd files, covering root-bypasses-permissions,
  a missing `op`/docker/git binary, and the opt-in end-to-end suites. 12 at the first audit —
  the growth is new suites, not new evasion.
- **`internal/gitutil` is not over-exported**: 20 exported functions, every one used. The
  thinnest case is `HeadRef`, down to a single caller; see 4.5.
- **One stale TODO** in the tree, at `acceptance/sidecar_test.go:548`. (A `grep` for TODO also
  hits `internal/testing/fixtures/github.go:164`, which is review-comment fixture data, not a
  TODO.)
- `internal/cmd/validate.go:179`'s `_ = json.NewDecoder(r).Decode(&p)` is correct by design —
  a non-hook invocation with piped stdin decodes to nothing and the empty `SessionID` is the
  signal.
- Best-effort silent renames in `internal/config/paths.go` and `internal/eventlog/eventlog.go`
  are deliberate and commented.
- Goroutines in `internal/variants/variants.go` and `internal/cmd/task.go` are correct and
  well-commented; `watchWindowSize` (`internal/sidecar/terminal_unix.go:18`, with a polling
  Windows twin) owns its signal channel and lifecycle properly.
- `internal/sidecar/env.go` is the single source for env-pair and dotenv parsing, and
  `internal/cmd/env.go` delegates to it correctly.
- `internal/testing/fakes` is the only set of fake CircleCI/GitHub/Anthropic/SSH servers; there
  is no second implementation.
- Every `config.LoadProjectConfig` call in `internal/cmd/validate.go` now keeps its error
  (`:235`, `:253`, `:863`, `:918`, `:1735`). The swallowed-config-load finding from the first
  audit is closed.

---

## 9. Suggested order

Cheap and high value first, so the linter starts catching the rest.

1. `DisableRetries: true` — 1.1. One field.
2. `errors.As` in `main.go` — 1.2. Five call sites.
3. Linter configuration — section 5. Enables `dupl` to police the rest.
4. `-count=1` on `task test` — 6.2. `acceptance-test` already has it; copy that line.
5. `docs/CLI.md` and `docs/SKILLS.md` corrections — section 7. All five still stand.
6. Small duplicate collapses — 3.9, 3.12's `FormatDuration` and shell-escape rows.
7. Keyring: error classification plus the generic wrapper — 1.6 and 3.6.
8. Reconcile the two stack detectors — 2.1. Own PR.
9. The auth provider table — 3.1. Own PR, and the most urgent of the three: it is the only
   finding that has measurably worsened on its own terms, 18 hand-written functions to 22,
   because a fourth credential kind cost six.
10. Split `envbuilder` by stack — 4.1. Own PR.

Done since the first audit: 1.3, 1.4, 1.5, 1.14, 3.3, 3.12's `hooks-disabled` sentinel, 2.3's
bad path, and the swallowed config-load error. 6.2 and 6.3 are half done. Going the wrong way
on their own terms: 3.1 (18 → 22), 4.2 (one stray import → three), 3.12's `.chunk` literal
(8 → 11), ed25519 keygen (3 → 6) and the auth confirm block (2 → 4).
