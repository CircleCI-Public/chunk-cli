#!/usr/bin/env bash
set -euo pipefail

BIN_DIR="$HOME/.local/bin"
BINARY_NAME="chunk"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Detect platform
PLATFORM=$(uname -s)-$(uname -m)
case "$PLATFORM" in
    Darwin-arm64) PLATFORM="darwin-arm64" ;;
    Darwin-x86_64) PLATFORM="darwin-x64" ;;
    Linux-aarch64) PLATFORM="linux-arm64" ;;
    Linux-x86_64) PLATFORM="linux-x64" ;;
    *)
        echo -e "${RED}Error: Unsupported platform $PLATFORM${NC}"
        echo "Supported platforms: macOS (arm64/x86_64), Linux (arm64/x86_64)"
        exit 1
        ;;
esac

echo "Installing chunk for $PLATFORM..."
echo ""

# Ensure Bun is available
ensure_bun() {
    # Check if bun is already in PATH
    if command -v bun &> /dev/null; then
        echo -e "${GREEN}✓${NC} Bun found: $(bun --version)"
        return 0
    fi

    echo -e "${RED}Bun not found in PATH${NC}"
    exit 1
}

# Install dependencies
install_dependencies() {
    echo "Installing dependencies..."
    bun install
    echo -e "${GREEN}✓${NC} Dependencies installed"
}

# Build binary
build_binary() {
    echo "Building binary for $PLATFORM..."

    bun run "build:${PLATFORM}"

    # Verify binary was created
    BINARY_PATH="dist/$BINARY_NAME-$PLATFORM"
    if [ ! -f "$BINARY_PATH" ]; then
        echo -e "${RED}Error: Build failed - binary not created at $BINARY_PATH${NC}"
        exit 1
    fi

    # Test binary
    if ! "$BINARY_PATH" --version &> /dev/null; then
        echo -e "${RED}Error: Built binary failed version check${NC}"
        exit 1
    fi

    echo -e "${GREEN}✓${NC} Binary built successfully"
}

# Install binary from build
install_binary() {
    echo "Installing binary to $BIN_DIR..."

    mkdir -p "$BIN_DIR"
    rm -f "$BIN_DIR/$BINARY_NAME"
    cp "dist/$BINARY_NAME-$PLATFORM" "$BIN_DIR/$BINARY_NAME"
    chmod +x "$BIN_DIR/$BINARY_NAME"

    echo -e "${GREEN}✓${NC} Binary installed"
}

build_from_source() {
    ensure_bun
    install_dependencies
    build_binary
    install_binary
}

# --- Dev installation flow ---
build_from_source

echo "Run 'chunk --version' to verify installation."
echo "Run 'chunk upgrade' to update to latest version."

# Check if BIN_DIR is in PATH
if [[ ":$PATH:" != *":$BIN_DIR:"* ]]; then
    echo -e "${YELLOW}NOTE:${NC} Add $BIN_DIR to your PATH:"
    echo "  echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.zshrc"
    echo ""
fi
