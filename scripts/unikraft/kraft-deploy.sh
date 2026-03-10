#!/usr/bin/env bash
# scripts/kraft-deploy.sh
#
# Creates (or replaces) a Unikraft Cloud instance from a published image.
#
# Usage:
#   ./scripts/kraft-deploy.sh <image-name> [extra kraft flags...]
#   e.g. ./scripts/kraft-deploy.sh bun-ssh --env SSH_PUBLIC_KEY="$KEY" --port 2222:2222/tls
#
# Required env vars:
#   KRAFTCLOUD_USER    your Unikraft Cloud username
#   KRAFTCLOUD_TOKEN   your Unikraft Cloud API token
#
# Optional:
#   IMAGE_TAG          image tag (default: latest)
#   INSTANCE_NAME      instance name (default: <image-name>)
#   METRO              Unikraft Cloud metro (default: fra0)
#   MEMORY             instance memory (default: 512Mi)

set -euo pipefail

: "${KRAFTCLOUD_USER:?KRAFTCLOUD_USER is required}"
: "${KRAFTCLOUD_TOKEN:?KRAFTCLOUD_TOKEN is required}"

IMAGE_NAME="${1:?Usage: $0 <image-name> [extra kraft flags...]}"
shift

IMAGE_TAG="${IMAGE_TAG:-latest}"
INSTANCE_NAME="${INSTANCE_NAME:-${IMAGE_NAME}}"
METRO="${METRO:-fra0}"
MEMORY="${MEMORY:-512Mi}"
IMAGE="${KRAFTCLOUD_USER}/${IMAGE_NAME}"

echo "Image:    $IMAGE"
echo "Instance: $INSTANCE_NAME"
echo "Metro:    $METRO"
echo "Memory:   $MEMORY"
echo ""

echo "── deploying ────────────────────────────────────────────────────────────"
kraft cloud instance stop "$INSTANCE_NAME" --metro "$METRO" 2>/dev/null || true
kraft cloud instance remove "$INSTANCE_NAME" --metro "$METRO" 2>/dev/null || true

kraft cloud instance create \
  --metro "$METRO" \
  --name "$INSTANCE_NAME" \
  -M "$MEMORY" \
  "$@" \
  "$IMAGE"

echo ""
echo "Instance created. Get the address with:"
echo "  kraft cloud instance get $INSTANCE_NAME --metro $METRO"