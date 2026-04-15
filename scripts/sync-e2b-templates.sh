#!/usr/bin/env bash
set -euo pipefail

# Fetch recent cimg Docker images from Docker Hub and create E2B templates from them.
#
# Prerequisites:
#   - e2b CLI installed and authenticated (e2b auth login or E2B_ACCESS_TOKEN set)
#   - jq, curl
#
# Usage:
#   ./scripts/sync-e2b-templates.sh [--dry-run]

LANGUAGES=(go python node rust)
TAGS_PER_LANG=3
SEMVER_REGEX='^[0-9]+\.[0-9]+\.[0-9]+$'
DRY_RUN=false

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    *) echo "Unknown argument: $arg" >&2; exit 1 ;;
  esac
done

# Check prerequisites
for cmd in e2b jq curl; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "Error: '$cmd' is required but not found in PATH." >&2
    exit 1
  fi
done

# Fetch the N most recent full-semver tags for a cimg repository.
# Args: $1 = repository name (e.g. "go")
fetch_tags() {
  local repo="$1"
  curl -s "https://hub.docker.com/v2/repositories/cimg/${repo}/tags?page_size=100&ordering=last_updated" \
    | jq -r --arg regex "$SEMVER_REGEX" \
        '.results[] | select(.name | test($regex)) | .name' \
    | head -n "$TAGS_PER_LANG"
}

# Sanitize a version string for use as an E2B template name (dots → dashes).
sanitize_version() {
  echo "$1" | tr '.' '-'
}

created=0
failed=0
cleanup_dirs=()
trap 'rm -rf "${cleanup_dirs[@]+"${cleanup_dirs[@]}"}"' EXIT

for lang in "${LANGUAGES[@]}"; do
  echo "--- cimg/${lang} ---"

  tags=$(fetch_tags "$lang")
  if [[ -z "$tags" ]]; then
    echo "  No semver tags found, skipping."
    continue
  fi

  while IFS= read -r tag; do
    template_name="cimg-${lang}-$(sanitize_version "$tag")"

    if "$DRY_RUN"; then
      echo "  [dry-run] Would build template '${template_name}' from cimg/${lang}:${tag}"
      continue
    fi

    echo "  Building template '${template_name}' from cimg/${lang}:${tag} ..."

    tmpdir=$(mktemp -d)
    cleanup_dirs+=("$tmpdir")
    echo "FROM cimg/${lang}:${tag}" > "${tmpdir}/Dockerfile"

    if e2b template build --name "$template_name" --dockerfile "${tmpdir}/Dockerfile" --cmd "sleep infinity"; then
      echo "  Created ${template_name}"
      created=$((created + 1))
    else
      echo "  Failed to create ${template_name}" >&2
      failed=$((failed + 1))
    fi

  done <<< "$tags"
done

echo ""
echo "Done. Created: ${created}, Failed: ${failed}"
