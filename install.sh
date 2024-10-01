#!/bin/bash
printBanner() {
cat << 'BANNER'       

██╗░░██╗██╗░░░██╗██████╗░███████╗░██████╗███████╗███╗░░██╗░██████╗███████╗
██║░██╔╝██║░░░██║██╔══██╗██╔════╝██╔════╝██╔════╝████╗░██║██╔════╝██╔════╝
█████═╝░██║░░░██║██████╦╝█████╗░░╚█████╗░█████╗░░██╔██╗██║╚█████╗░█████╗░░
██╔═██╗░██║░░░██║██╔══██╗██╔══╝░░░╚═══██╗██╔══╝░░██║╚████║░╚═══██╗██╔══╝░░
██║░╚██╗╚██████╔╝██████╦╝███████╗██████╔╝███████╗██║░╚███║██████╔╝███████╗
╚═╝░░╚═╝░╚═════╝░╚═════╝░╚══════╝╚═════╝░╚══════╝╚═╝░░╚══╝╚═════╝░╚══════╝   

BANNER
}
# Check OS and architecture
OS=$(uname -s)
ARCH=$(uname -m)

# Define the binary name and the directory to install it to
BINARY_NAME="kubesense"
BINARY_BASE_URL=https://github.com/kubesense-ai/kubesense-cli/releases/latest/download
INSTALL_DIR="/usr/local/bin"
# Detect OS and architecture
case "$OS" in
  Linux)
    case "$ARCH" in
      x86_64)
        BINARY_URL="https://example.com/$BINARY_NAME-linux-amd64.tar.gz"
        ;;
      arm64|aarch64)
        BINARY_URL="https://example.com/$BINARY_NAME-linux-arm64.tar.gz"
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
        BINARY_URL="https://example.com/$BINARY_NAME-darwin-amd64.tar.gz"
        ;;
      arm64)
        BINARY_URL="https://example.com/$BINARY_NAME-darwin-arm64.tar.gz"
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

echo "Detected OS: $OS, Architecture: $ARCH"
echo "Downloading binary from: $BINARY_URL"

# Download the binary
curl -L -o "$BINARY_NAME".tar.gz "$BINARY_URL" | tar -xzvf -
if [ $? -ne 0 ]; then
  echo "Failed to download binary"
  exit 1
fi

# Make the binary executable
chmod +x "$BINARY_NAME"

# Move the binary to the installation directory
echo "Installing $BINARY_NAME to $INSTALL_DIR"
# sudo mv "$BINARY_NAME" "$INSTALL_DIR/"
rm "$BINARY_NAME".tar.gz
# Add to PATH if not already
if ! echo "$PATH" | grep -q "$INSTALL_DIR"; then
  echo "Adding $INSTALL_DIR to PATH"
  echo "export PATH=\$PATH:$INSTALL_DIR" >> ~/.bashrc
  source ~/.bashrc
fi

echo "$BINARY_NAME installed successfully!"
printBanner