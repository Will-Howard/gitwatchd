#!/bin/sh
# Install the gitwatchd binary for the current user (default: ~/.local/bin).
# Usage: ./install.sh [destination-dir]
set -eu

arch=$(uname -m)
here=$(CDPATH= cd "$(dirname "$0")" && pwd)
bin="$here/dist/gitwatchd-linux-$arch"

if [ ! -x "$bin" ]; then
  if command -v go >/dev/null 2>&1; then
    echo "building gitwatchd for $arch..."
    (cd "$here" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$bin" .)
  else
    echo "error: no prebuilt binary at $bin and no Go toolchain to build one" >&2
    exit 1
  fi
fi

dest="${1:-$HOME/.local/bin}"
mkdir -p "$dest"
install -m 0755 "$bin" "$dest/gitwatchd"
echo "installed $dest/gitwatchd"

case ":$PATH:" in
  *":$dest:"*) ;;
  *) echo "note: $dest is not on your PATH; add it to your shell profile" ;;
esac

echo ""
echo "next steps:"
echo "  gitwatchd <path-to-repo>   watch a repo"
echo "  gitwatchd autostart on     run at boot (systemd user unit)"
echo "  gitwatchd status           see everything watched"
