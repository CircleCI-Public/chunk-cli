# Sidecar Setup Reference

Walk through four phases: **detect → plan → install → snapshot**. Each phase has a clear gate before moving on.

---

## Phase 1: Detect

Run these in parallel — you need both before planning.

### 1a. Tech stack
```
chunk sidecar env --dir .
```
This emits a JSON environment spec. Parse it carefully — it tells you most of what you need before installing anything.

Key fields and how to use them:

| Field | What it means | How to use it |
|-------|--------------|---------------|
| `stack` | Detected runtime (e.g. `"go"`, `"python"`, `"node"`) | Determines which Runtime Install section to follow |
| `image_version` | Detected runtime version (e.g. `"1.22"`, `"3.11"`) | The exact version to install — prefer this over guessing from config files |
| `system_deps` | List of system packages required (e.g. `["libpq-dev", "libssl-dev"]`) | Install these via `apt-get` before installing the runtime |
| `commands` | Commands the build/test process expects on PATH (e.g. `["go", "task", "golangci-lint"]`) | Use as a checklist — every entry here must be resolvable after setup |
| `env_vars` | Environment variables the project expects (e.g. `["DATABASE_URL", "API_KEY"]`) | Note these now; they may need to be set before validate passes |

**What to do with this output:**

1. If `system_deps` is non-empty, install them first (before the runtime):
   ```bash
   apt-get update && apt-get install -y <dep1> <dep2> ...
   ```

2. Use `commands` as your install checklist. After each install step, verify every command in the list is resolvable before moving on.

3. If `image_version` is missing or the `stack` is unrecognized, fall back to manual detection (see stack-specific notes in the Runtime Install Reference below).

4. If `env_vars` lists variables that require secrets or external config, flag them to the user before snapshotting — a snapshot taken with missing env vars will produce a sidecar that fails validate on first use.

### 1b. Build tools
Inspect the repo for these config files:

| File | Tool to install |
|------|----------------|
| `Taskfile.yml` or `Taskfile.yaml` | `task` (go-task) |
| `Makefile` | `make` |
| `.tool-versions` or `.mise.toml` | `mise` |
| `.nvmrc` or `.node-version` | Use the version it contains with nvm |

```bash
ls Taskfile.yml Taskfile.yaml Makefile .tool-versions .mise.toml .nvmrc .node-version 2>/dev/null
```

---

## Phase 2: Plan

Summarize what you're about to install and show it to the user before doing anything. Example:

> **Detected: Go 1.26 repo with Taskfile**
> Plan:
> - Install Go 1.26.x (from go.dev/dl)
> - Install `task` runner (go-task.dev)
>
> Sidecar name? (will be used for both the sidecar and the snapshot)

Wait for the user to confirm the name and approve the plan before continuing.

If the user asks for a **dry run** (or says "show me what you'd run", "what commands will you execute", "preview the setup"), print the full sequence of commands — sidecar creation, SSH key registration, every install command, and the snapshot command — without executing any of them. Label each step clearly so the user can see exactly what would happen. After printing, ask if they'd like to proceed.

If the detected stack isn't in the Runtime Install Reference below, tell the user which stack was detected and that you don't have a canned install script for it. Ask them to provide the install commands, or offer a best-effort install (see **Unsupported Stacks**).

---

## Phase 3: Create and Install

### 3a. Create the sidecar
```
chunk sidecar create --name <confirmed-name>
```
This sets it as the active sidecar automatically.

### 3b. Register your SSH key
The base sidecar won't have your key yet. Run:
```
chunk sidecar add-ssh-key --public-key-file ~/.ssh/chunk_ai.pub
```
If that file doesn't exist, try `~/.ssh/id_ed25519.pub` or `~/.ssh/id_rsa.pub`. If none exist, tell the user: SSH keys are needed for installation commands.

### 3c. Install the runtime
Use `chunk sidecar ssh -- bash -c "<commands>"` for each install step. Multi-step commands should be chained with `&&`. After each install, verify immediately with a version check before moving on — catching a broken install early is much cheaper than debugging it later.

See the **Runtime Install Reference** below for per-stack commands. If the stack isn't covered there, see **Unsupported Stacks**.

### 3d. Install build tools
Install each build tool the same way, and verify each one right after installing it. See **Build Tool Install Reference** below.

---

## Phase 4: Snapshot

### 4a. Run a validate smoke test
Before snapshotting, confirm the environment actually works end-to-end. Sync the current repo and run the configured validate commands:
```
chunk sidecar sync
chunk validate --remote
```
If validate passes, the snapshot will capture a working environment. If it fails with an environmental error (missing binary, wrong version), fix it now — re-snapshotting a broken environment just locks in the problem. If it fails with a code error (test failure, lint issue), that's fine — the environment itself is correct and the snapshot is still worth taking.

### 4b. Create the snapshot
Ask the user to confirm the snapshot name. Suggest `<sidecar-name>-ready` as the default. Then:

```
chunk sidecar snapshot create --name <snapshot-name>
```

Print the snapshot ID that comes back. Tell the user: future sidecars can boot from this snapshot with:
```
chunk sidecar create --name <new-name> --image <snapshot-id>
```

---

## Runtime Install Reference

All commands run via `chunk sidecar ssh -- bash -c "..."` unless noted.

### Go
Detect the required version from `go.mod`:
```bash
grep '^go ' go.mod | awk '{print $2}'
```
If `go.mod` isn't present or the version field is absent, use the latest stable from https://go.dev/dl/?mode=json.

Install:
```bash
GO_VERSION=<version> && \
wget -q https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz -O /tmp/go.tar.gz && \
rm -rf /usr/local/go && \
tar -C /usr/local -xzf /tmp/go.tar.gz && \
echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh && \
/usr/local/go/bin/go version
```

> Note: Go version strings in `go.mod` refer to the language version, not a patch release. Install the latest `1.x.*` patch. For example, `go 1.26` → install `1.26.0` (or latest `1.26.x`). Check https://go.dev/dl/?mode=json for available versions.

### Python
Detect version from `.python-version`, `pyproject.toml` (`requires-python` or `[tool.poetry.dependencies] python`), or fall back to system default.

For system default (3.x):
```bash
apt-get update && apt-get install -y python3 python3-pip python3-venv && python3 --version
```

For a specific version, use pyenv:
```bash
apt-get update && apt-get install -y build-essential libssl-dev zlib1g-dev libbz2-dev \
  libreadline-dev libsqlite3-dev curl libncursesw5-dev xz-utils tk-dev libxml2-dev \
  libxmlsec1-dev libffi-dev liblzma-dev && \
curl https://pyenv.run | bash && \
echo 'export PYENV_ROOT="$HOME/.pyenv"' >> /etc/profile.d/pyenv.sh && \
echo 'export PATH="$PYENV_ROOT/bin:$PATH"' >> /etc/profile.d/pyenv.sh && \
echo 'eval "$(pyenv init -)"' >> /etc/profile.d/pyenv.sh && \
export PYENV_ROOT="$HOME/.pyenv" && export PATH="$PYENV_ROOT/bin:$PATH" && eval "$(pyenv init -)" && \
pyenv install <version> && pyenv global <version> && python --version
```

### Node / JavaScript / TypeScript
Detect version from `.nvmrc`, `.node-version`, or `engines.node` in `package.json`.

Install via nvm:
```bash
curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.3/install.sh | bash && \
export NVM_DIR="$HOME/.nvm" && . "$NVM_DIR/nvm.sh" && \
nvm install <version> && nvm use <version> && node --version
```

If no version is pinned, install the LTS:
```bash
nvm install --lts
```

### Rust
```bash
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y && \
. "$HOME/.cargo/env" && \
rustc --version
```

### Ruby
Detect version from `.ruby-version` or `Gemfile`. Install rbenv then the target version:
```bash
apt-get update && apt-get install -y rbenv ruby-build && \
rbenv install <version> && rbenv global <version> && ruby --version
```

---

## Build Tool Install Reference

### task (go-task)
```bash
sh -c "$(curl --location https://taskfile.dev/install.sh)" -- -d -b /usr/local/bin && task --version
```

### make
```bash
apt-get update && apt-get install -y make && make --version
```

### mise
```bash
curl https://mise.run | sh && \
echo 'eval "$($HOME/.local/bin/mise activate bash)"' >> /etc/profile.d/mise.sh && \
$HOME/.local/bin/mise --version
```
After installing mise, run `mise install` in the project directory to pick up `.tool-versions` or `.mise.toml`.

---

## Unsupported Stacks

If `chunk sidecar env` returns a stack that isn't covered in the Runtime Install Reference (e.g. `dotnet`, `java`, `haskell`, `scala`, `php`, `elixir`):

1. Tell the user: "I detected a `<stack>` project but don't have a built-in install script for that runtime."
2. Ask: "Do you have the install commands you'd like me to run, or should I attempt a best-effort install?"
3. If the user provides commands — run them as-is via `chunk sidecar ssh -- bash -c "..."`.
4. If the user says "best effort" — describe your plan before executing. Common Debian approaches:
   - **Java**: `apt-get install -y default-jdk`
   - **.NET**: use the official `dotnet-install.sh` script from https://dot.net/v1/dotnet-install.sh
   - **PHP**: `apt-get install -y php php-cli php-mbstring`
   - **Elixir**: `apt-get install -y elixir`
   - **Haskell**: GHCup (`curl --proto '=https' --tlsv1.2 -sSf https://get-ghcup.haskell.org | sh`)
   - For anything else, try `apt-cache search <runtime>` to find a package, describe what you found, and wait for user confirmation before installing.
5. Always verify with `<runtime> --version` before snapshotting. If the install fails, report back to the user with the error rather than proceeding to snapshot.

---

## Troubleshooting

- **`permission denied (publickey)`** — the SSH key wasn't registered. Re-run `chunk sidecar add-ssh-key`. If keys don't exist locally, ask the user to generate one: `ssh-keygen -t ed25519 -f ~/.ssh/chunk_ai`.
- **Go version not found on go.dev** — the `go.mod` version may be ahead of the current release. Check https://go.dev/dl/?mode=json for the actual latest available version and install that instead.
- **`apt-get` not found** — the base image may not be Debian. Run `chunk sidecar ssh -- bash -c "cat /etc/os-release"` to check. Adjust the package manager accordingly (apk for Alpine, yum/dnf for RHEL/Amazon Linux).
- **Install succeeds but binary not found on next exec** — PATH changes via `/etc/profile.d/` require a login shell. Use full paths (e.g. `/usr/local/go/bin/go`) in subsequent exec calls, or prefix with `. /etc/profile.d/<file>.sh &&`.
