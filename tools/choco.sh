#!/bin/bash
# choco.sh is a wrapper around the Chocolatey Docker image that lets
# goreleaser pack the chocolatey package as if choco were installed locally.
set -euo pipefail
exec docker run --rm \
  -v "$(pwd):/work" \
  -w /work \
  chocolatey/choco choco "$@"
