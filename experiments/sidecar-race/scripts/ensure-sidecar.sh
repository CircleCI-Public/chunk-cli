#!/usr/bin/env bash
# Create and sync an active sidecar from validation.sidecarImage if none is running.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

require_cmd chunk

if has_active_sidecar; then
  echo "Active sidecar already set:"
  chunk sidecar current
  exit 0
fi

CONFIG="${REPO_ROOT}/.chunk/config.json"
[[ -f "${CONFIG}" ]] || die "missing ${CONFIG}"

read -r ORG_ID SNAPSHOT < <(python3 -c "
import json
from pathlib import Path
d = json.loads(Path('${CONFIG}').read_text())
print(d.get('orgID', ''), d.get('validation', {}).get('sidecarImage', ''))
")

[[ -n "${ORG_ID}" ]] || die "orgID missing in .chunk/config.json"
[[ -n "${SNAPSHOT}" ]] || die "validation.sidecarImage missing — run chunk sidecar setup and snapshot create first"

echo "Creating sidecar from snapshot ${SNAPSHOT}..."
chunk sidecar create --org-id "${ORG_ID}" --image "${SNAPSHOT}"
chunk sidecar sync
echo "Sidecar ready."
