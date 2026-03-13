#!/usr/bin/env bash
set -euo pipefail

# === Config ===
REPO="CircleCI-Public/chunk-cli"
BIN_DIR="${CHUNK_BIN_DIR:-$HOME/.local/bin}"
BINARY_NAME="chunk"
BASE_URL="https://github.com/$REPO/releases/latest/download"

# Embedded GPG public key used to sign chunk releases.
# To update: gpg --armor --export <FINGERPRINT>
GPG_PUBLIC_KEY="-----BEGIN PGP PUBLIC KEY BLOCK-----
# TODO: paste the release signing public key here
-----END PGP PUBLIC KEY BLOCK-----"

# === Colors ===
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}✓${NC} $*"; }
warn()  { echo -e "${YELLOW}!${NC} $*"; }
error() { echo -e "${RED}✗${NC} $*" >&2; exit 1; }

# === Detect platform → sets $ARCHIVE_NAME ===
detect_platform() {
	local os arch
	os="$(uname -s)"
	arch="$(uname -m)"
	case "$os-$arch" in
		Darwin-arm64)  ARCHIVE_NAME="chunk-cli_Darwin_arm64.tar.gz" ;;
		Darwin-x86_64) ARCHIVE_NAME="chunk-cli_Darwin_x86_64.tar.gz" ;;
		Linux-aarch64) ARCHIVE_NAME="chunk-cli_Linux_arm64.tar.gz" ;;
		Linux-x86_64)  ARCHIVE_NAME="chunk-cli_Linux_x86_64.tar.gz" ;;
		*) error "Unsupported platform: $os-$arch. Supported: macOS (arm64/x86_64), Linux (arm64/x86_64)" ;;
	esac
}

# === Download with retry ===
download() {
	local url="$1" dest="$2"
	curl -fsSL --retry 3 --retry-delay 2 --location "$url" -o "$dest" \
		|| error "Download failed: $url"
}

# === GPG signature check on checksums.txt ===
verify_signature() {
	local file="$1" sig="$2"

	if ! command -v gpg &>/dev/null; then
		warn "gpg not found — skipping signature verification"
		warn "Install gpg to enable this check: brew install gnupg  (macOS) or apt install gnupg (Linux)"
		return 0
	fi

	# Sanity-check that the embedded key is real
	if echo "$GPG_PUBLIC_KEY" | grep -q "TODO:"; then
		warn "Release signing key not yet configured in this installer — skipping signature verification"
		return 0
	fi

	# Import key into an isolated temp keyring (does not touch ~/.gnupg)
	local tmp_keyring="$TMP_DIR/chunk-keyring.gpg"
	echo "$GPG_PUBLIC_KEY" | gpg --batch --no-default-keyring \
		--keyring "$tmp_keyring" --import 2>/dev/null \
		|| error "Failed to import chunk release signing key"

	gpg --batch --no-default-keyring --keyring "$tmp_keyring" \
		--verify "$sig" "$file" 2>/dev/null \
		|| error "Signature verification FAILED — the release may be corrupted or tampered with"

	info "GPG signature verified"
}

# === SHA256 checksum check ===
verify_checksum() {
	local archive="$1" checksums="$2"

	local expected
	expected="$(grep "$(basename "$archive")" "$checksums" | awk '{print $1}')"
	[ -n "$expected" ] || error "No checksum entry for $(basename "$archive") in checksums.txt"

	local actual
	if command -v sha256sum &>/dev/null; then
		actual="$(sha256sum "$archive" | awk '{print $1}')"
	elif command -v shasum &>/dev/null; then
		actual="$(shasum -a 256 "$archive" | awk '{print $1}')"
	else
		error "Cannot verify checksum: neither sha256sum nor shasum found in PATH"
	fi

	if [ "$actual" != "$expected" ]; then
		error "Checksum mismatch for $(basename "$archive")
  expected: $expected
  actual:   $actual"
	fi

	info "SHA256 checksum verified"
}

# === Main ===
main() {
	command -v curl &>/dev/null || error "curl is required but not found in PATH"

	detect_platform

	TMP_DIR="$(mktemp -d)"
	trap 'rm -rf "$TMP_DIR"' EXIT

	local archive="$TMP_DIR/$ARCHIVE_NAME"
	local checksums="$TMP_DIR/checksums.txt"
	local sig="$TMP_DIR/checksums.txt.sig"

	echo "Downloading chunk ($ARCHIVE_NAME)..."
	download "$BASE_URL/$ARCHIVE_NAME"      "$archive"
	download "$BASE_URL/checksums.txt"      "$checksums"
	download "$BASE_URL/checksums.txt.sig"  "$sig"

	verify_signature "$checksums" "$sig"
	verify_checksum  "$archive"  "$checksums"

	tar -xzf "$archive" -C "$TMP_DIR" chunk

	mkdir -p "$BIN_DIR"
	install -m 755 "$TMP_DIR/chunk" "$BIN_DIR/$BINARY_NAME"

	if ! "$BIN_DIR/$BINARY_NAME" --version &>/dev/null; then
		error "Installed binary failed smoke test — installation may be incomplete"
	fi

	info "chunk $("$BIN_DIR/$BINARY_NAME" --version) installed to $BIN_DIR/$BINARY_NAME"

	if ! echo "$PATH" | tr ':' '\n' | grep -qx "$BIN_DIR"; then
		echo ""
		warn "$BIN_DIR is not in your \$PATH"
		echo "  Add it with:  echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.zshrc"
		echo "  Then restart your shell."
	fi
}

main
