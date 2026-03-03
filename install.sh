#!/usr/bin/env bash
set -euo pipefail

BIN_DIR="$HOME/.local/bin"
BINARY_NAME="chunk"
BINARY_DEST="$BIN_DIR/$BINARY_NAME"

# Download correct release.
# GoReleaser names archives as: chunk-cli_{OS}_{Arch}.tar.gz
# e.g. chunk-cli_Darwin_arm64.tar.gz, chunk-cli_Linux_x86_64.tar.gz
OS="$(uname -s)"
ARCH="$(uname -m)"
RELEASE_NAME="chunk-cli_${OS}_${ARCH}.tar.gz"
ARCHIVE_DEST="/tmp/$RELEASE_NAME"

mkdir -p "$BIN_DIR"

if ! command -v "gh" >/dev/null 2>&1; then
	echo "Missing required command: gh" >&2
	echo "Install the Github CLI before proceeding." >&2
	exit 1
fi

CHECKSUMS_FILE="/tmp/chunk-checksums.txt"

echo "Downloading release $RELEASE_NAME with gh CLI..."

gh release download \
	--clobber \
	--repo CircleCI-Public/chunk-cli \
	--pattern "$RELEASE_NAME" \
	--output "$ARCHIVE_DEST"

gh release download \
	--clobber \
	--repo CircleCI-Public/chunk-cli \
	--pattern "checksums.txt" \
	--output "$CHECKSUMS_FILE"

echo "Verifying checksum..."
EXPECTED_CHECKSUM="$(grep "$RELEASE_NAME" "$CHECKSUMS_FILE" | awk '{print $1}')"
if [ -z "$EXPECTED_CHECKSUM" ]; then
	echo "Error: checksum for $RELEASE_NAME not found in checksums.txt" >&2
	rm -f "$ARCHIVE_DEST" "$CHECKSUMS_FILE"
	exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
	ACTUAL_CHECKSUM="$(sha256sum "$ARCHIVE_DEST" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
	ACTUAL_CHECKSUM="$(shasum -a 256 "$ARCHIVE_DEST" | awk '{print $1}')"
else
	echo "Error: cannot verify checksum: neither sha256sum nor shasum is available." >&2
	rm -f "$ARCHIVE_DEST" "$CHECKSUMS_FILE"
	exit 1
fi

if [ "$ACTUAL_CHECKSUM" != "$EXPECTED_CHECKSUM" ]; then
	echo "Error: checksum mismatch for $RELEASE_NAME" >&2
	echo "  expected: $EXPECTED_CHECKSUM" >&2
	echo "  actual:   $ACTUAL_CHECKSUM" >&2
	rm -f "$ARCHIVE_DEST" "$CHECKSUMS_FILE"
	exit 1
fi
rm -f "$CHECKSUMS_FILE"

tar -xzf "$ARCHIVE_DEST" -C "/tmp" chunk
mv "/tmp/chunk" "$BINARY_DEST"
rm -f "$ARCHIVE_DEST"

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
