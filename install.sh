#!/bin/sh
# gitwatchd installer. Run with:
#   curl -fsSL https://raw.githubusercontent.com/Will-Howard/gitwatchd/main/install.sh | sh

set -u

REPO=https://github.com/Will-Howard/gitwatchd

say() { echo "$1" >&2; }
warn() { echo "  ⚠ $1" >&2; }
err() { echo "  ⚠ $1" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }
ensure() { "$@" || err "command failed: $*"; }

cleanup() {
	[ -n "${TMP:-}" ] && rm -rf "$TMP"
	[ -n "${STAGED:-}" ] && rm -f "$STAGED"
	return 0
}

detect_platform() {
	os=$(uname -s)
	arch=$(uname -m)
	case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	esac
	if [ "$os" = Darwin ]; then
		say "gitwatchd for macOS installs via Homebrew for the moment:"
		say "    brew tap will-howard/tap"
		say "    brew trust --cask will-howard/tap/gitwatchd"
		say "    brew install --cask gitwatchd"
		exit 1
	fi
	if [ "$os" = Linux ] && [ "$(getconf LONG_BIT 2>/dev/null || echo 64)" = 32 ]; then
		err "gitwatchd needs a 64-bit userland, this system reports 32-bit."
	fi
	case "$os-$arch" in
	Linux-amd64 | Linux-arm64) ARTIFACT="gitwatchd-linux-$arch" ;;
	*) err "no gitwatchd build for $os-$arch yet. Please open an issue: $REPO/issues" ;;
	esac
}

resolve_source() {
	if have curl; then
		DOWNLOADER=curl
	elif have wget; then
		DOWNLOADER=wget
	else
		err "gitwatchd needs curl or wget to download itself. Install one and re-run."
	fi
	if [ -n "${GITWATCHD_DOWNLOAD_URL:-}" ]; then
		BASE=${GITWATCHD_DOWNLOAD_URL%/}
	elif [ -n "${GITWATCHD_VERSION:-}" ]; then
		BASE="$REPO/releases/download/$GITWATCHD_VERSION"
	else
		BASE="$REPO/releases/latest/download"
	fi
}

download() {
	if [ "$DOWNLOADER" = curl ]; then
		curl -fsSL "$1" -o "$2"
	else
		wget -q -O "$2" "$1"
	fi
}

verify_checksum() {
	if have sha256sum; then
		got=$(sha256sum "$TMP/$ARTIFACT" | cut -d' ' -f1)
	elif have shasum; then
		got=$(shasum -a 256 "$TMP/$ARTIFACT" | cut -d' ' -f1)
	else
		warn "no sha256sum or shasum on this system, skipping checksum verification"
		return 0
	fi
	if ! download "$BASE/SHA256SUMS" "$TMP/SHA256SUMS"; then
		warn "could not download $BASE/SHA256SUMS, so the binary cannot be verified"
		say "    press Enter to install it unverified, Ctrl-C to abort (30s)"
		timeout 30 head -n1 /dev/tty >/dev/null 2>&1 || err "not confirmed, aborting"
		return 0
	fi
	want=$(awk -v a="$ARTIFACT" '$2 == a || $2 == "*" a {print $1}' "$TMP/SHA256SUMS")
	[ -n "$want" ] || err "$ARTIFACT is not listed in SHA256SUMS"
	if [ "$want" != "$got" ]; then
		warn "checksum mismatch for $ARTIFACT, refusing to install:"
		say "    want $want"
		say "    got  $got"
		exit 1
	fi
}

# Same search order as make install.
choose_dest() {
	if [ -n "${GITWATCHD_INSTALL_DIR:-}" ]; then
		ensure mkdir -p "$GITWATCHD_INSTALL_DIR"
		DEST=${GITWATCHD_INSTALL_DIR%/}
		return 0
	fi
	for d in /usr/local/bin "$HOME/.local/bin" "$HOME/bin"; do
		mkdir -p "$d" 2>/dev/null || true
		if [ -w "$d" ]; then
			DEST=$d
			return 0
		fi
	done
	err "no writable bin dir found. Retry with GITWATCHD_INSTALL_DIR=<dir> set."
}

install_binary() {
	[ -x "$DEST/gitwatchd" ] && "$DEST/gitwatchd" stop >/dev/null 2>&1
	STAGED="$DEST/.gitwatchd.$$"
	ensure cp "$TMP/$ARTIFACT" "$STAGED"
	ensure chmod 0755 "$STAGED"
	ensure mv -f "$STAGED" "$DEST/gitwatchd"
	STAGED=
	say "✓ binary → $DEST/gitwatchd"
}

start_daemon() {
	case ":$PATH:" in
	*":$DEST:"*) ;;
	*) warn "$DEST is not on your PATH. Add:  export PATH=\"$DEST:\$PATH\"" ;;
	esac
	[ -n "${GITHUB_PATH:-}" ] && echo "$DEST" >>"$GITHUB_PATH"
	"$DEST/gitwatchd" start || err "$DEST/gitwatchd start failed"
	say "  Next:  gitwatchd .        to watch the current repo"
	say "         gitwatchd status   to see what it is doing"
}

main() {
	TMP=
	STAGED=
	trap cleanup EXIT
	detect_platform
	resolve_source
	choose_dest
	TMP=$(mktemp -d) || err "could not create a temporary directory"
	download "$BASE/$ARTIFACT" "$TMP/$ARTIFACT" ||
		err "could not download $BASE/$ARTIFACT"
	[ -s "$TMP/$ARTIFACT" ] || err "$BASE/$ARTIFACT downloaded empty"
	verify_checksum
	install_binary
	start_daemon
}

main "$@"
