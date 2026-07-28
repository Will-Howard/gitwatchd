#!/bin/sh
# Install the gitwatchd binary for the current user (default: ~/.local/bin).
# Usage: ./install.sh [destination-dir]
set -eu

here=$(CDPATH= cd "$(dirname "$0")" && pwd)
bin="$here/build/gitwatchd"

# `make install` has already built this; building here too keeps the script
# usable on its own.
if [ ! -x "$bin" ]; then
  if command -v go >/dev/null 2>&1; then
    echo "building gitwatchd..."
    (cd "$here" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$bin" .)
  else
    echo "error: nothing built at $bin and no Go toolchain to build it" >&2
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
