#!/usr/bin/env bash
set -euo pipefail

# ---------------------------------------------------------------------------
# Usage
# ---------------------------------------------------------------------------
# Demonstrates the sandbox snapshot flow:
#   1. Create a sandbox
#   2. Create a snapshot from that sandbox
#   3. Create a new sandbox from the snapshot
#
#   ORG_ID=<your-org-id> ./demo-snapshot.sh

# ---------------------------------------------------------------------------
# Configuration — fill these in or export before running
# ---------------------------------------------------------------------------
ORG_ID="${ORG_ID:-}"
SANDBOX_NAME="${SANDBOX_NAME:-demo-snapshot-base}"
SNAPSHOT_NAME="${SNAPSHOT_NAME:-demo-checkpoint}"
RESTORED_NAME="${RESTORED_NAME:-demo-snapshot-restored}"
CHUNK="${CHUNK:-./dist/chunk}"

# ---------------------------------------------------------------------------
# Logging helpers
# ---------------------------------------------------------------------------
BOLD="\033[1m"
DIM="\033[2m"
GREEN="\033[32m"
CYAN="\033[36m"
YELLOW="\033[33m"
RESET="\033[0m"

step() { echo -e "\n${BOLD}${CYAN}==> $*${RESET}"; }
info() { echo -e "    ${DIM}$*${RESET}"; }
ok()   { echo -e "    ${GREEN}✓ $*${RESET}"; }
cmd()  { echo -e "    ${YELLOW}\$ $*${RESET}"; "$@"; }

# Strip ANSI escape codes then grab the last whitespace-delimited token.
extract_last_word() { sed 's/\x1b\[[0-9;]*m//g' <<< "$1" | awk '{print $NF}'; }

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------
step "Checking prerequisites..."

if [[ ! -x "$CHUNK" ]]; then
  echo "Error: chunk binary not found at ${CHUNK}. Run 'task build' first." >&2
  exit 1
fi
if [[ -z "$ORG_ID" ]]; then
  echo "Error: ORG_ID is not set. Export it or prefix the command:" >&2
  echo "  ORG_ID=<your-org-id> $0" >&2
  exit 1
fi
if [[ -z "${CIRCLE_TOKEN:-}" ]]; then
  echo "Error: CIRCLE_TOKEN is not set." >&2
  exit 1
fi

ok "CIRCLE_TOKEN is set"
info "Org ID:         $ORG_ID"
info "Base sandbox:   $SANDBOX_NAME"
info "Snapshot name:  $SNAPSHOT_NAME"
info "Restored name:  $RESTORED_NAME"

# ---------------------------------------------------------------------------
# Step 1 — Create sandbox
# ---------------------------------------------------------------------------
step "Creating base sandbox '${SANDBOX_NAME}'..."
if ! SANDBOX_OUT=$("$CHUNK" sandbox create \
  --org-id "$ORG_ID" \
  --name "$SANDBOX_NAME" 2>&1); then
  echo "$SANDBOX_OUT"
  echo "Error: sandbox create failed" >&2
  exit 1
fi
echo "$SANDBOX_OUT"

SANDBOX_ID=$(grep -oE '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' <<< "$SANDBOX_OUT" | head -1)
if [[ -z "$SANDBOX_ID" ]]; then
  echo "Error: could not extract sandbox ID from output." >&2
  exit 1
fi
ok "Sandbox ID: $SANDBOX_ID"

# ---------------------------------------------------------------------------
# Step 2 — Create snapshot from the sandbox
# ---------------------------------------------------------------------------
step "Creating snapshot '${SNAPSHOT_NAME}' from sandbox ${SANDBOX_ID}..."
if ! SNAPSHOT_OUT=$("$CHUNK" sandbox snapshot create \
  --sandbox-id "$SANDBOX_ID" \
  --name "$SNAPSHOT_NAME" 2>&1); then
  echo "$SNAPSHOT_OUT"
  echo "Error: snapshot create failed" >&2
  exit 1
fi
echo "$SNAPSHOT_OUT"

SNAPSHOT_ID=$(extract_last_word "$SNAPSHOT_OUT")
if [[ -z "$SNAPSHOT_ID" ]]; then
  echo "Error: could not extract snapshot ID from output." >&2
  exit 1
fi
ok "Snapshot ID: $SNAPSHOT_ID"

# Verify the snapshot is retrievable
step "Verifying snapshot..."
cmd "$CHUNK" sandbox snapshot get "$SNAPSHOT_ID"

# ---------------------------------------------------------------------------
# Step 3 — Create a new sandbox from the snapshot
# ---------------------------------------------------------------------------
step "Creating restored sandbox '${RESTORED_NAME}' from snapshot ${SNAPSHOT_ID}..."
if ! RESTORED_OUT=$("$CHUNK" sandbox create \
  --org-id "$ORG_ID" \
  --name "$RESTORED_NAME" \
  --image "$SNAPSHOT_ID" 2>&1); then
  echo "$RESTORED_OUT"
  echo "Error: sandbox create from snapshot failed" >&2
  exit 1
fi
echo "$RESTORED_OUT"

RESTORED_ID=$(grep -oE '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' <<< "$RESTORED_OUT" | head -1)
if [[ -z "$RESTORED_ID" ]]; then
  echo "Error: could not extract restored sandbox ID from output." >&2
  exit 1
fi
ok "Restored sandbox ID: $RESTORED_ID"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
step "Done"
info "Base sandbox:    $SANDBOX_ID  ($SANDBOX_NAME)"
info "Snapshot:        $SNAPSHOT_ID  ($SNAPSHOT_NAME)"
info "Restored sandbox: $RESTORED_ID  ($RESTORED_NAME)"
