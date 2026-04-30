#!/bin/sh
# boozle install script — POSIX, dependency-free except curl + tar + shasum.
#
# Usage:
#   curl -fsSL https://github.com/gethash/boozle/releases/latest/download/install.sh | sh
#
# Env overrides:
#   BOOZLE_VERSION       release tag to install (default: latest)
#   BOOZLE_INSTALL_DIR   target dir (default: /usr/local/bin or ~/.local/bin)
set -eu

REPO="gethash/boozle"
VERSION="${BOOZLE_VERSION:-latest}"
INSTALL_DIR="${BOOZLE_INSTALL_DIR:-}"

err() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
log() { printf 'boozle: %s\n' "$*"; }

# Detect OS / arch.
case "$(uname -s)" in
  Darwin) GOOS=darwin ;;
  Linux)  GOOS=linux  ;;
  *) err "unsupported OS: $(uname -s) — see Releases for manual install" ;;
esac

case "$(uname -m)" in
  arm64|aarch64) GOARCH=arm64 ;;
  x86_64|amd64)  GOARCH=amd64 ;;
  *) err "unsupported arch: $(uname -m)" ;;
esac

TARGET="${GOOS}-${GOARCH}"

need() { command -v "$1" >/dev/null 2>&1 || err "missing required command: $1"; }
need curl
need tar
need shasum

# Resolve "latest" → tag.
if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
  [ -n "$VERSION" ] || err "could not resolve latest release tag"
fi

ARCHIVE="boozle_${VERSION}_${TARGET}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT INT HUP TERM

log "downloading ${ARCHIVE} (${VERSION}, ${TARGET})..."
curl -fsSL -o "${TMPDIR}/${ARCHIVE}" "${BASE_URL}/${ARCHIVE}"

log "verifying SHA256..."
curl -fsSL -o "${TMPDIR}/checksums.txt" "${BASE_URL}/checksums.txt"
EXPECTED="$(awk -v a="${ARCHIVE}" '$2 == a { print $1 }' "${TMPDIR}/checksums.txt")"
[ -n "$EXPECTED" ] || err "${ARCHIVE} not found in checksums.txt"
ACTUAL="$(shasum -a 256 "${TMPDIR}/${ARCHIVE}" | awk '{ print $1 }')"
[ "$EXPECTED" = "$ACTUAL" ] || err "checksum mismatch (expected ${EXPECTED}, got ${ACTUAL})"

log "extracting..."
tar -C "${TMPDIR}" -xzf "${TMPDIR}/${ARCHIVE}"
SRC="${TMPDIR}/boozle_${VERSION}_${TARGET}/boozle"
[ -x "$SRC" ] || err "binary missing inside archive: ${SRC}"

# Pick install dir.
SUDO=""
if [ -z "$INSTALL_DIR" ]; then
  if [ -w /usr/local/bin ] 2>/dev/null; then
    INSTALL_DIR=/usr/local/bin
  elif [ -d /usr/local/bin ] && command -v sudo >/dev/null 2>&1; then
    INSTALL_DIR=/usr/local/bin
    SUDO=sudo
    log "using sudo to write to /usr/local/bin (set BOOZLE_INSTALL_DIR to skip)"
  else
    INSTALL_DIR="${HOME}/.local/bin"
    mkdir -p "$INSTALL_DIR"
  fi
fi

DEST="${INSTALL_DIR}/boozle"
log "installing to ${DEST}..."
${SUDO} install -m 0755 "$SRC" "$DEST"

log "installed boozle ${VERSION} to ${DEST}"

case ":${PATH}:" in
  *:"${INSTALL_DIR}":*) ;;
  *) log "note: ${INSTALL_DIR} is not on your \$PATH" ;;
esac

if [ "$GOOS" = "darwin" ]; then
  cat <<EOF

macOS Gatekeeper note:
  If macOS blocks the unsigned binary on first run, strip the quarantine bit:

    xattr -d com.apple.quarantine "${DEST}"

EOF
fi
