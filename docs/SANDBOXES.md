# Sandboxes

Sandboxes are ephemeral, cloud-hosted Linux environments that your coding
agents use to build, test, and validate changes — without touching your local
machine or shared CI infrastructure.

The core pitch: every agent session gets a fast, isolated, reproducible
environment it can trash freely. No port conflicts between parallel agents, no
leftover state from a previous run, no waiting for a CI queue. Sync your local
changes, run your full test suite in seconds, snapshot a working environment
so the next agent starts from a known-good baseline.

## Prerequisites

- `chunk` CLI installed and authenticated (`chunk auth set --circleci`)
- `CIRCLE_TOKEN` or `CIRCLECI_TOKEN` set in your environment
- An SSH keypair at `~/.ssh/chunk_ai` (generated automatically on first use)

---

## Creating a Sandbox

```bash
chunk sandbox create --name my-sandbox
```

`chunk` will prompt you to pick an organization if `--org-id` is not supplied
(or `CIRCLECI_ORG_ID` is not set). The new sandbox is automatically set as the
**active sandbox** for your project, written to `.chunk/sandbox.json`.

```bash
# Explicit org ID
chunk sandbox create --org-id <org-id> --name my-sandbox

# From a custom image or E2B template
chunk sandbox create --name my-sandbox --image ubuntu:22.04
chunk sandbox create --name my-sandbox --image <e2b-template-id>
```

### Listing sandboxes

```bash
chunk sandbox list
```

### Switching the active sandbox

Commands that accept `--sandbox-id` will use the active sandbox when the flag
is omitted. To switch:

```bash
chunk sandbox use <sandbox-id>
chunk sandbox current        # show active
chunk sandbox forget         # clear active
```

---

## Syncing Your Code

`sync` pushes your local working tree to the sandbox. It does **not** require
your changes to be committed or pushed to a remote — it works on whatever is
in your working directory right now.

```bash
chunk sandbox sync
```

Under the hood:

1. Detects your repo from the git remote (e.g. `github.com/org/repo`)
2. Clones into `/workspace/<repo>` on the sandbox (if not already present)
3. Computes the diff between your local working tree and the remote merge base
4. Resets the sandbox checkout to that merge base, then applies your diff as a
   patch

The result is that `/workspace/<repo>` on the sandbox exactly mirrors your
local working tree — including staged and unstaged changes — regardless of
what has been pushed.

### Incremental syncs

Subsequent `sync` calls are fast. The clone already exists; only the patch
changes. Run `sync` after every significant edit batch and before running
validation.

```bash
# Edit code locally, then:
chunk sandbox sync
chunk validate --sandbox-id <id>
```

### Custom destination

```bash
chunk sandbox sync --workdir /opt/myapp
```

---

## Running Validation on a Sandbox

Any `chunk validate` command can target a sandbox instead of your local
machine:

```bash
# Run all configured commands on the active sandbox
chunk validate

# Run a specific command
chunk validate test

# Run a one-off command without saving it
chunk validate --cmd "go test -count=1 ./..."
```

The validate command opens an SSH session to the sandbox, executes each
configured command in `/workspace/<repo>` (or `--workdir`), and streams
stdout/stderr back to your terminal. Exit codes are propagated — a non-zero
exit from any command fails the run.

This is the same command tree your agents use. See [HOOKS.md](HOOKS.md) for
how validation is wired into pre-commit checks.

---

## SSH Access

For direct exploration or debugging:

```bash
# Interactive shell
chunk sandbox ssh

# Run a single command
chunk sandbox ssh -- ls /workspace

# Forward environment variables into the session
chunk sandbox ssh -e DATABASE_URL=postgres://...
chunk sandbox ssh --env-file .env.local

# Use a specific identity file
chunk sandbox ssh --identity-file ~/.ssh/my_key
```

The SSH connection uses a WebSocket tunnel — no inbound firewall rules needed.
Host keys are stored in `~/.ssh/chunk_ai_known_hosts` (trust-on-first-use).

### Executing commands non-interactively

```bash
chunk sandbox exec --command bash --args -c "cat /workspace/my-app/go.sum"
```

`exec` returns JSON with `stdout`, `stderr`, and `exit_code`. Useful for
scripting or agent-driven automation.

---

## Snapshots

Snapshots capture the full state of a sandbox — installed packages, cached
build artifacts, populated databases — and let you boot new sandboxes from
that checkpoint instantly.

**Why this matters for agents:** preparing a sandbox (cloning, installing
dependencies, running database migrations) can take minutes. Do it once,
snapshot it, and every subsequent agent session starts from a ready environment
in seconds.

### Creating a snapshot

```bash
chunk sandbox snapshot create --name my-checkpoint
# or with an explicit sandbox:
chunk sandbox snapshot create --sandbox-id <id> --name my-checkpoint
```

Returns a snapshot ID.

### Booting from a snapshot

Pass the snapshot ID as `--image` when creating a new sandbox:

```bash
chunk sandbox create --name session-for-agent --image <snapshot-id>
```

The new sandbox starts with the filesystem state captured at snapshot time.
Sync your latest code on top and validate immediately:

```bash
chunk sandbox create --name agent-session --image <snapshot-id>
chunk sandbox sync
chunk validate
```

### Inspecting a snapshot

```bash
chunk sandbox snapshot get <snapshot-id>
```

---

## Customizing the Environment

### Auto-detection

`chunk` can analyse your repository and generate a Dockerfile tailored to its
tech stack:

```bash
chunk sandbox env
```

Outputs a JSON environment spec describing detected languages, runtimes,
package managers, and tools. Review it, then build:

```bash
chunk sandbox env | chunk sandbox build --tag myapp-test:latest
```

This writes `Dockerfile.test` to the current directory and runs `docker build`.
Use the resulting image as the `--image` when creating sandboxes:

```bash
chunk sandbox create --name ci-env --image myapp-test:latest
```

### Manual customization

Once inside a sandbox, install whatever you need over SSH:

```bash
chunk sandbox ssh -- bash -c "apt-get install -y ripgrep && pip install -r requirements.txt"
```

After customization, snapshot the sandbox to preserve the state:

```bash
chunk sandbox snapshot create --name configured-env
```

All future sessions start from that snapshot — no reinstallation required.

### Environment variables

Secrets and configuration can be injected at SSH-session time without baking
them into the image:

```bash
# From flags
chunk sandbox ssh -e DATABASE_URL=postgres://... -e API_KEY=secret

# From a .env file (supports comments, export prefix, quoted values)
chunk sandbox ssh --env-file .env.local

# Combining both (flags win on conflicts)
chunk sandbox ssh --env-file .env.local -e API_KEY=override
```

For secrets managed by 1Password, prefix the value with `op://`:

```bash
chunk sandbox ssh -e API_KEY=op://vault/item/field
```

---

## Multiple Sessions and Worktrees

Each Claude Code session can maintain its own active sandbox. When
`CLAUDE_SESSION_ID` is set (which Claude Code does automatically), the active
sandbox is stored at `.chunk/sandbox.<session-id>` — so two sessions running
concurrently in the same repository target different sandboxes without
conflict.

```
.chunk/
  sandbox.abc123   # session abc123's sandbox
  sandbox.def456   # session def456's sandbox
```

This makes parallel agent workflows straightforward: spin up one sandbox per
worktree or per feature branch, let agents work independently, validate each in
isolation.

---

## Agentic Workflows

### Basic agent loop

The minimal setup for an agent that validates its own changes:

```bash
# 1. One-time: create and configure a sandbox, snapshot it
chunk sandbox create --name agent-base
chunk sandbox ssh -- bash -c "go install ... && npm ci"
chunk sandbox snapshot create --name agent-ready

# 2. Per-session: restore from snapshot, sync code, validate
chunk sandbox create --name session-$(date +%s) --image <snapshot-id>
chunk sandbox sync
chunk validate
```

### Wiring into Claude Code hooks

`chunk init` generates `.claude/settings.json` with:

- A **PreToolUse** hook that runs `chunk validate` before every commit —
  blocking the commit if any command fails.
- A **Stop hook** that runs `chunk validate --if-changed` after every session —
  warming the cache so the pre-commit check is near-instant.

To point those hooks at your sandbox instead of the local machine, set
`--sandbox-id` in your `.chunk/config.json` commands, or use the
`sandbox-dev` skill which manages the sync+validate loop automatically.

### The sandbox-dev skill

Install the skill:

```bash
chunk skill install sandbox-dev
```

Once installed, agents can run `sandbox-dev` to enter a loop that:

1. Syncs local changes to the active sandbox
2. Runs all configured validation commands remotely
3. Reports results back inline

This gives agents a tight edit → remote-validate feedback loop without any
manual intervention.

### Running validation in CI

The same `chunk validate --sandbox-id` command works in CI pipelines. Check
out the `debug-ci-failures` skill for patterns around surfacing sandbox output
into PR comments and agent context.

---

## Reference

| Command | Description |
|---|---|
| `chunk sandbox create` | Create a sandbox (sets it active) |
| `chunk sandbox list` | List all sandboxes |
| `chunk sandbox use <id>` | Set active sandbox |
| `chunk sandbox current` | Show active sandbox |
| `chunk sandbox forget` | Clear active sandbox |
| `chunk sandbox sync` | Sync local working tree to sandbox |
| `chunk sandbox ssh` | Interactive or command SSH session |
| `chunk sandbox exec` | Non-interactive command execution (JSON output) |
| `chunk sandbox add-ssh-key` | Register a public key manually |
| `chunk sandbox env` | Detect tech stack, output environment spec JSON |
| `chunk sandbox build` | Generate `Dockerfile.test` and build image |
| `chunk sandbox snapshot create` | Snapshot the current sandbox state |
| `chunk sandbox snapshot get <id>` | Inspect a snapshot |
| `chunk validate --sandbox-id <id>` | Run validation commands on a sandbox |
