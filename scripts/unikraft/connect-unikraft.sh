# Opens a socat TLS tunnel to the Unikraft instance then SSHes in.
# Requires: socat
#
# Usage:
#   ./sandbox/connect-unikraft.sh <instance-host>
#   e.g. ./sandbox/connect-unikraft.sh nameless-cherry-sw2e9ul2.fra.unikraft.app
#
# Optional env vars:
#   SANDBOX_KEY    path to SSH private key (default: ~/.ssh/unikraft_sandbox)
#   LOCAL_PORT     local port for the tunnel (default: 2222)
#   REMOTE_PORT    remote port on the instance (default: 2222)

set -euo pipefail

INSTANCE_HOST="${1:-${INSTANCE_HOST:-}}"
if [[ -z "$INSTANCE_HOST" ]]; then
  echo "Usage: $0 <instance-host>" >&2
  exit 1
fi

SANDBOX_KEY="${SANDBOX_KEY:-${HOME}/.ssh/unikraft_sandbox}"
LOCAL_PORT="${LOCAL_PORT:-2222}"
REMOTE_PORT="${REMOTE_PORT:-2222}"
REMOTE_USER="${REMOTE_USER:-root}"

# Start socat tunnel in background
socat TCP-LISTEN:${LOCAL_PORT},reuseaddr,fork \
  OPENSSL:${INSTANCE_HOST}:${REMOTE_PORT},verify=0 &
SOCAT_PID=$!
trap "kill $SOCAT_PID 2>/dev/null" EXIT

sleep 0.5

ssh-keygen -R "[localhost]:${LOCAL_PORT}" 2>/dev/null || true

ssh -p "${LOCAL_PORT}" \
  -o StrictHostKeyChecking=no \
  -o IdentitiesOnly=yes \
  -i "${SANDBOX_KEY}" \
  ${REMOTE_USER}@localhost