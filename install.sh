#!/bin/bash
printBanner() {
cat << 'BANNER'
██╗░░██╗██╗░░░██╗██████╗░███████╗░██████╗███████╗███╗░░██╗░██████╗███████╗
██║░██╔╝██║░░░██║██╔══██╗██╔════╝██╔════╝██╔════╝████╗░██║██╔════╝██╔════╝
█████═╝░██║░░░██║██████╦╝█████╗░░╚█████╗░█████╗░░██╔██╗██║╚█████╗░█████╗░░
██╔═██╗░██║░░░██║██╔══██╗██╔══╝░░░╚═══██╗██╔══╝░░██║╚████║░╚═══██╗██╔══╝░░
██║░╚██╗╚██████╔╝██████╦╝███████╗██████╔╝███████╗██║░╚███║██████╔╝███████╗
╚═╝░░╚═╝░╚═════╝░╚═════╝░╚══════╝╚═════╝░╚══════╝╚═╝░░╚══╝╚═════╝░╚══════╝   

Get started: kubesense configure, then kubesense plan and kubesense apply
BANNER
}
# Check OS and architecture
OS=$(uname -s)
ARCH=$(uname -m)

# Define the binary name and the directory to install it to
BINARY_NAME="kubesense"
BINARY_BASE_URL=https://github.com/kubesense-ai/kubesense-cli/releases/latest/download
INSTALL_DIR="${KUBESENSE_INSTALL_DIR:-/usr/local/bin}"
PLATFORM=""
# Detect OS and architecture
case "$OS" in
  Linux)
    case "$ARCH" in
      x86_64)
        PLATFORM="_linux_amd64.tar.gz"
        ;;
      arm64|aarch64)
        PLATFORM="_linux_arm64.tar.gz"
        ;;
      *)
        echo "Unsupported architecture: $ARCH on Linux"
        exit 1
        ;;
    esac
    ;;
  Darwin)
    case "$ARCH" in
      x86_64)
        PLATFORM="_darwin_amd64.tar.gz"
        ;;
      arm64)
        PLATFORM="_darwin_arm64.tar.gz"
        ;;
      *)
        echo "Unsupported architecture: $ARCH on macOS"
        exit 1
        ;;
    esac
    ;;
  *)
    echo "Unsupported OS: $OS"
    exit 1
    ;;
esac
BINARY_URL="$BINARY_BASE_URL/$BINARY_NAME$PLATFORM"
echo "Detected OS: $OS, Architecture: $ARCH"
echo "Downloading binary from: $BINARY_URL"

fail() { echo "Error: $*" >&2; exit 1; }

# Work in a temp directory so nothing depends on (or litters) the current one.
TMP_DIR=$(mktemp -d) || fail "cannot create a temp directory"
trap 'rm -rf "$TMP_DIR"' EXIT

curl -fL -o "$TMP_DIR/$BINARY_NAME.tar.gz" "$BINARY_URL" || fail "download failed: $BINARY_URL"
tar -xzf "$TMP_DIR/$BINARY_NAME.tar.gz" -C "$TMP_DIR" "$BINARY_NAME" || fail "could not unpack $BINARY_NAME from the archive"
chmod +x "$TMP_DIR/$BINARY_NAME"

# Use sudo only when needed. New Apple Silicon Macs have no /usr/local/bin, so create it.
SUDO=""
EXISTING="$INSTALL_DIR"
while [ ! -d "$EXISTING" ]; do EXISTING=$(dirname "$EXISTING"); done
[ -w "$EXISTING" ] || SUDO="sudo"
echo "Installing $BINARY_NAME to $INSTALL_DIR"
$SUDO mkdir -p "$INSTALL_DIR" || fail "cannot create $INSTALL_DIR"
$SUDO install -m 0755 "$TMP_DIR/$BINARY_NAME" "$INSTALL_DIR/$BINARY_NAME" || fail "cannot install to $INSTALL_DIR"

# Only claim success once the installed binary actually runs.
"$INSTALL_DIR/$BINARY_NAME" version >/dev/null 2>&1 || fail "$INSTALL_DIR/$BINARY_NAME was installed but does not run"
"$INSTALL_DIR/$BINARY_NAME" version | head -1

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Note: $INSTALL_DIR is not on your PATH. Add it with:"
     echo "  echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.zshrc   # or ~/.bashrc" ;;
esac
FIRST=$(command -v "$BINARY_NAME" 2>/dev/null)
if [ -n "$FIRST" ] && [ "$FIRST" != "$INSTALL_DIR/$BINARY_NAME" ]; then
  echo "Note: $FIRST comes first on your PATH and will run instead; remove it, then run: hash -r"
fi

echo "$BINARY_NAME installed successfully!"
printBanner
