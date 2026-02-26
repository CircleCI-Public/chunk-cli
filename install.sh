#!/usr/bin/env bash
set -euo pipefail

BIN_DIR="$HOME/.local/bin"
BINARY_NAME="chunk"
BINARY_DEST="$BIN_DIR/$BINARY_NAME"

# Download correct release.
RELEASE_NAME="chunk-$(echo "$(uname -s)-$(uname -m)" | tr '[:upper:]' '[:lower:]')"

mkdir -p "$BIN_DIR"

if ! command -v "gh" >/dev/null 2>&1; then
	echo "Missing required command: gh" >&2
	echo "Install the Github CLI before proceeding." >&2
	exit 1
fi

echo "Downloading release $RELEASE_NAME with gh CLI..."

gh release download \
	--clobber \
	--repo circleci/code-review-cli \
	--pattern "$RELEASE_NAME" \
	--output "$BINARY_DEST"

chmod +x "$BINARY_DEST"

if ! "$BINARY_DEST" --version; then
	echo "Binary does not appear to be successfully installed." >&2
	exit 1
fi

echo "Succesful! Binary installed at $BINARY_DEST"

if ! echo "$PATH" | tr ':' '\n' | grep -w -q "$BIN_DIR"; then
	echo ""
	echo "WARNING: $BIN_DIR does not appear to be in your \$PATH."
	echo ""
	echo "Add it and then restart your shell session to use chunk."
fi
