#!/usr/bin/env bash
set -euo pipefail

# ---------------------------------------------------------------------------
# Usage
# ---------------------------------------------------------------------------
# Run all steps:
#   ./demo-sandbox.sh
#
# Run specific steps:
#   ./demo-sandbox.sh sync
#   ./demo-sandbox.sh install
#   ./demo-sandbox.sh test
#   ./demo-sandbox.sh install test
#
# Steps: sync | install | test

# ---------------------------------------------------------------------------
# Configuration — fill these in before running
# ---------------------------------------------------------------------------
ORG_ID="${ORG_ID:-}"
SANDBOX_ID="${SANDBOX_ID:-}"
SANDBOX_NAME="${SANDBOX_NAME:-demo-sandbox}"
DEST="${DEST:-/workspace}"
IDENTITY_FILE="${IDENTITY_FILE:-$HOME/.ssh/chunk_ai}"

# ---------------------------------------------------------------------------
# Keepalive — pings the sandbox every second to prevent idle timeouts.
# Uses a temp file to share SANDBOX_ID with the background subshell since
# variables set after fork are not visible to background processes.
# ---------------------------------------------------------------------------
_SID_FILE=$(mktemp)
_KEEPALIVE_PID=""

_keepalive_loop() {
  local sid_file="$1" org="$2"
  while true; do
    local sid
    sid=$(cat "$sid_file" 2>/dev/null || true)
    if [[ -n "$sid" ]]; then
      chunk sandboxes exec \
        --org-id "$org" \
        --sandbox-id "$sid" \
        --command bash \
        --args -c "echo ping" >/dev/null 2>&1 || true
    fi
    sleep 1
  done
}

start_keepalive() {
  _keepalive_loop "$_SID_FILE" "$ORG_ID" &
  _KEEPALIVE_PID=$!
}

stop_keepalive() {
  if [[ -n "${_KEEPALIVE_PID:-}" ]]; then
    kill "$_KEEPALIVE_PID" 2>/dev/null || true
    wait "$_KEEPALIVE_PID" 2>/dev/null || true
  fi
  rm -f "$_SID_FILE"
}

trap stop_keepalive EXIT

# ---------------------------------------------------------------------------
# Logging helpers
# ---------------------------------------------------------------------------
BOLD="\033[1m"
DIM="\033[2m"
GREEN="\033[32m"
CYAN="\033[36m"
YELLOW="\033[33m"
WHITE="\033[97m"
RESET="\033[0m"

step()  { echo -e "\n${BOLD}${CYAN}==> $*${RESET}"; }
info()  { echo -e "    ${DIM}$*${RESET}"; }
ok()    { echo -e "    ${GREEN}✓ $*${RESET}"; }
cmd()   { echo -e "    ${YELLOW}\$ $*${RESET}"; "$@"; }

# exec_cmd — run sandboxes exec and render the JSON response as a single line.
# On success: prints "✓ <last non-empty stdout line>".
# On remote failure (non-zero exit_code in JSON): prints full stdout/stderr and exits.
exec_cmd() {
  echo -e "    ${YELLOW}\$ $*${RESET}"
  local out exit_status=0
  out=$("$@") || exit_status=$?
  if [[ $exit_status -ne 0 ]]; then
    echo "$out"
    return $exit_status
  fi
  local remote_exit
  remote_exit=$(echo "$out" | jq -r '.exit_code')
  if [[ "$remote_exit" -eq 0 ]]; then
    local summary
    summary=$(echo "$out" | jq -r '(.stdout | split("\n") | map(select(length > 0)) | last // "(done)")')
    echo -e "    ${GREEN}✓${RESET} ${DIM}${summary}${RESET}"
  else
    echo -e "    ${RED}✗ exit ${remote_exit}${RESET}"
    echo "$out" | jq -r '(.stdout + "\n" + .stderr) | split("\n") | map(select(length > 0)) | .[]'
    return "$remote_exit"
  fi
}

# ssh_cmd — run sandboxes ssh and truncate long output on success.
# Shows the last SSH_TAIL_LINES lines when output exceeds the threshold.
# Uses a temp file to capture both stdout and stderr.
SSH_TAIL_LINES="${SSH_TAIL_LINES:-25}"

ssh_cmd() {
  echo -e "    ${YELLOW}\$ $*${RESET}"
  local tmpfile exit_status=0
  tmpfile=$(mktemp)
  "$@" >"$tmpfile" 2>&1 || exit_status=$?
  local line_count
  line_count=$(wc -l <"$tmpfile" | tr -d ' ')
  if [[ $exit_status -eq 0 && $line_count -gt $SSH_TAIL_LINES ]]; then
    tail -n "$SSH_TAIL_LINES" "$tmpfile"
    echo -e "    ${DIM}... ${line_count} lines total, showing last ${SSH_TAIL_LINES}${RESET}"
  else
    cat "$tmpfile"
  fi
  rm -f "$tmpfile"
  return $exit_status
}

# pause [label] — halts the script until Enter is pressed.
# Set NO_PAUSE=1 to skip all pauses (e.g. for automated runs).
pause() {
  local label="${1:-}"
  if [[ "${NO_PAUSE:-0}" == "1" ]]; then return; fi
  echo ""
  echo -e "${BOLD}${WHITE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
  if [[ -n "$label" ]]; then
    echo -e "${BOLD}${WHITE}  ⏸  $label${RESET}"
  fi
  echo -e "${DIM}  Press Enter to continue...${RESET}"
  echo -e "${BOLD}${WHITE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
  read -r
}

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------
preflight() {
  step "Checking prerequisites..."

  if [[ -z "$ORG_ID" ]]; then
    echo "Error: ORG_ID is not set. Export it or prefix the command:" >&2
    echo "  ORG_ID=<your-org-id> $0" >&2
    exit 1
  fi

  if [[ -z "${CIRCLE_TOKEN:-}" ]]; then
    echo "Error: CIRCLE_TOKEN is not set." >&2
    exit 1
  fi

  if ! command -v jq &>/dev/null; then
    echo "Error: jq is required (brew install jq)" >&2
    exit 1
  fi

  ok "CIRCLE_TOKEN is set"
  ok "jq is available"
  info "Org ID:       $ORG_ID"
  info "Destination:  $DEST"
  info "Identity:     $IDENTITY_FILE"
  if [[ -n "$SANDBOX_ID" ]]; then info "Sandbox ID:   $SANDBOX_ID (provided)"; fi
}

# ---------------------------------------------------------------------------
# Steps
# ---------------------------------------------------------------------------
step_sync() {
  # Create sandbox if no SANDBOX_ID provided
  if [[ -n "$SANDBOX_ID" ]]; then
    step "Reusing existing sandbox '${SANDBOX_ID}'..."
    ok "Skipping creation"
    _SANDBOX_PREEXISTED=1
  else
    step "Creating sandbox '${SANDBOX_NAME}'..."
    echo -e "    ${YELLOW}\$ chunk sandboxes create --org-id $ORG_ID --name $SANDBOX_NAME${RESET}"
    SANDBOX_JSON=$(chunk sandboxes create \
      --org-id "$ORG_ID" \
      --name "$SANDBOX_NAME")

    SANDBOX_ID=$(echo "$SANDBOX_JSON" | jq -r '.id')
    ok "Sandbox created"
    info "ID:   $SANDBOX_ID"
    info "Name: $(echo "$SANDBOX_JSON" | jq -r '.name')"
  fi

  # Publish SANDBOX_ID to the keepalive loop
  echo "$SANDBOX_ID" > "$_SID_FILE"

  # Set up SSH connectivity
  step "Setting up SSH connectivity..."

  if [[ ! -f "$IDENTITY_FILE" ]]; then
    info "No keypair found at ${IDENTITY_FILE}, generating one..."
    cmd ssh-keygen -t ed25519 -f "$IDENTITY_FILE" -N "" -C "chunk-sandbox" >/dev/null
    ok "Generated ${IDENTITY_FILE}"
  else
    ok "Using existing keypair at ${IDENTITY_FILE}"
  fi

  info "Registering public key with sandbox..."
  cmd chunk sandboxes add-ssh-key \
    --org-id "$ORG_ID" \
    --sandbox-id "$SANDBOX_ID" \
    --public-key-file "${IDENTITY_FILE}.pub"

  info "Waiting for SSH to be ready..."
  sleep 3

  pause "Sandbox is ready — SSH key registered"

  # Stamp a unique ID into a local file to prove sync picks up real changes
  step "Stamping local change..."
  _DEMO_UUID=$(uuidgen | tr '[:upper:]' '[:lower:]')
  echo "$_DEMO_UUID" > demo-change.txt
  ok "Wrote demo-change.txt → ${_DEMO_UUID}"

  # Sync (bootstrap only for new sandboxes)
  if [[ -n "${_SANDBOX_PREEXISTED:-}" ]]; then
    step "Syncing local changes to sandbox..."
    cmd chunk sandboxes sync \
      --org-id "$ORG_ID" \
      --sandbox-id "$SANDBOX_ID" \
      --dest "$DEST" \
      --identity-file "$IDENTITY_FILE"
  else
    step "Bootstrapping repository on sandbox..."
    info "Cloning repo and syncing local changes to ${DEST}..."
    cmd chunk sandboxes sync \
      --org-id "$ORG_ID" \
      --sandbox-id "$SANDBOX_ID" \
      --dest "$DEST" \
      --identity-file "$IDENTITY_FILE" \
      --bootstrap
  fi

  # Verify the stamped file made it to the sandbox
  step "Verifying sync..."
  _REMOTE_UUID=$(chunk sandboxes exec \
    --org-id "$ORG_ID" \
    --sandbox-id "$SANDBOX_ID" \
    --command bash \
    --args -c "cat ${DEST}/demo-change.txt" \
    | jq -r '.stdout' | tr -d '[:space:]') || true
  if [[ "$_REMOTE_UUID" == "$_DEMO_UUID" ]]; then
    ok "demo-change.txt confirmed on sandbox: ${_REMOTE_UUID}"
  else
    info "Could not verify (sync may still have succeeded)"
  fi

  pause "Repository synced to sandbox"
}

step_install() {
  step "Installing dependencies on sandbox..."

  info "Installing unzip (required by bun installer)..."
  exec_cmd chunk sandboxes exec \
    --org-id "$ORG_ID" \
    --sandbox-id "$SANDBOX_ID" \
    --command bash \
    --args -c "apt-get install -y unzip"

  info "Installing bun..."
  exec_cmd chunk sandboxes exec \
    --org-id "$ORG_ID" \
    --sandbox-id "$SANDBOX_ID" \
    --command bash \
    --args -c "curl -fsSL https://bun.sh/install | bash"

  info "Adding bun to PATH..."
  exec_cmd chunk sandboxes exec \
    --org-id "$ORG_ID" \
    --sandbox-id "$SANDBOX_ID" \
    --command bash \
    --args -c "ln -sf ~/.bun/bin/bun /usr/local/bin/bun"

  pause "Dependencies installed — bun is ready"
}

step_test() {
  pause "About to run tests on the sandbox"
  step "Running tests on sandbox..."
  cmd chunk validate run \
    --sandbox-id "$SANDBOX_ID" \
    --org-id "$ORG_ID" \
    --dest "$DEST" \
    --identity-file "$IDENTITY_FILE"
}

# ---------------------------------------------------------------------------
# Entrypoint
# ---------------------------------------------------------------------------
preflight
start_keepalive

# If SANDBOX_ID was provided up front (e.g. running install/test standalone),
# publish it immediately so the keepalive loop can start pinging right away.
if [[ -n "$SANDBOX_ID" ]]; then
  echo "$SANDBOX_ID" > "$_SID_FILE"
fi

if [[ $# -eq 0 ]]; then
  STEPS=(sync install test)
else
  STEPS=("$@")
fi

for s in "${STEPS[@]}"; do
  case "$s" in
    sync)    step_sync ;;
    install) step_install ;;
    test)    step_test ;;
    *) echo "Error: unknown step '$s'. Valid steps: sync install test" >&2; exit 1 ;;
  esac
done

echo ""
step "Done"
if [[ -n "$SANDBOX_ID" ]]; then ok "Sandbox ID: $SANDBOX_ID"; fi
