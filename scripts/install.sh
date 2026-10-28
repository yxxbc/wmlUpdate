#!/usr/bin/env sh
# Install the latest (or requested) wml-update release for Linux/macOS.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/yxxbc/wmlUpdate/main/scripts/install.sh | sh
#   VERSION=v1.1.0 INSTALL_DIR="$HOME/bin" sh install.sh
#
# Verified against the release checksums.txt. Ed25519 verification requires the
# release public key and is deliberately left to the release documentation.
set -eu

REPO="yxxbc/wmlUpdate"
VERSION="${VERSION:-}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
INSTALL_DIR="${INSTALL_DIR%/}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "error: $1 is required" >&2; exit 1; }
}
need curl
need tar
need uname

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux) os=linux ;;
  darwin) os=darwin ;;
  *) echo "error: unsupported OS: $os" >&2; exit 1 ;;
esac
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "error: unsupported architecture: $arch" >&2; exit 1 ;;
esac

if [ -z "$VERSION" ]; then
  api="https://api.github.com/repos/$REPO/releases/latest"
  VERSION="$(curl -fsSL "$api" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
  [ -n "$VERSION" ] || { echo "error: could not determine latest release" >&2; exit 1; }
fi
bare="${VERSION#v}"
asset="wml-update_${bare}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"

echo "Downloading $asset"
curl -fsSL "$base/$asset" -o "$TMP/$asset"
curl -fsSL "$base/checksums.txt" -o "$TMP/checksums.txt"

expected="$(awk -v name="$asset" '$2 == name { print $1; exit }' "$TMP/checksums.txt")"
[ -n "$expected" ] || { echo "error: $asset is not listed in checksums.txt" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$TMP/$asset" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$TMP/$asset" | awk '{print $1}')"
else
  echo "error: sha256sum or shasum is required" >&2
  exit 1
fi
[ "$actual" = "$expected" ] || { echo "error: checksum mismatch" >&2; exit 1; }

tar -xzf "$TMP/$asset" -C "$TMP"
bin="$TMP/wml-update"
[ -x "$bin" ] || { echo "error: archive does not contain wml-update" >&2; exit 1; }
"$bin" version >/dev/null

mkdir -p "$INSTALL_DIR"
tmp_install="$INSTALL_DIR/.wml-update.$$"
cp "$bin" "$tmp_install"
chmod 0755 "$tmp_install"
mv -f "$tmp_install" "$INSTALL_DIR/wml-update"
doc_dir="${DOC_DIR:-$(dirname "$INSTALL_DIR")/share/doc/wml-update}"
mkdir -p "$doc_dir"
for f in LICENSE NOTICE; do
  if [ -f "$TMP/$f" ]; then
    cp "$TMP/$f" "$doc_dir/$f"
  fi
done
echo "Installed $INSTALL_DIR/wml-update ($VERSION)"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Add $INSTALL_DIR to PATH to run wml-update." ;;
esac
