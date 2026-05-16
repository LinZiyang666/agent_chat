#!/bin/sh
# agentchat install.sh — one-shot installer.
#
# Default behavior:
#   - downloads the latest release tarball for your OS/arch
#   - verifies it against the published SHA256SUMS
#   - drops the two binaries into ~/.agentchat/bin
#   - prints how to add that path to PATH if needed
#
# This script ONLY installs files. It does NOT start the daemon.
#
# Usage:
#   curl -fsSL https://github.com/LinZiyang666/agent_chat/releases/latest/download/install.sh | sh
#   curl -fsSL https://github.com/LinZiyang666/agent_chat/releases/latest/download/install.sh | sh -s -- --version v0.0.0
#   curl -fsSL https://github.com/LinZiyang666/agent_chat/releases/latest/download/install.sh | sh -s -- --uninstall
#
# Flags:
#   --version VER       pin a release tag (default: resolve latest)
#   --source-base URL   override release base URL (advanced)
#   --prefix DIR        override install root (default: $HOME/.agentchat)
#   --dry-run           print actions without changing anything
#   --skip-download     don't fetch; assume binaries already in <prefix>/bin
#   --uninstall         remove binaries and (optionally) data
#   --purge             with --uninstall: also delete <prefix> entirely
set -eu

REPO_OWNER="LinZiyang666"
REPO_NAME="agent_chat"
PROJECT="agentchat"

VERSION="${AGENTCHAT_VERSION:-latest}"
SOURCE_BASE=""
PREFIX="${AGENTCHAT_HOME:-$HOME/.agentchat}"
DRY_RUN=0
SKIP_DOWNLOAD=0
UNINSTALL=0
PURGE=0

usage() {
    cat <<EOF
agentchat install.sh

Flags:
  --version VER       pin a release tag (default: latest)
  --source-base URL   override release base URL
  --prefix DIR        install root (default: \$HOME/.agentchat)
  --dry-run
  --skip-download
  --uninstall         remove binaries (data preserved unless --purge)
  --purge             with --uninstall: also delete <prefix>
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --version)       VERSION="$2";       shift 2 ;;
        --source-base)   SOURCE_BASE="$2";   shift 2 ;;
        --prefix)        PREFIX="$2";        shift 2 ;;
        --dry-run)       DRY_RUN=1;          shift ;;
        --skip-download) SKIP_DOWNLOAD=1;    shift ;;
        --uninstall)     UNINSTALL=1;        shift ;;
        --purge)         PURGE=1;            shift ;;
        -h|--help)       usage; exit 0 ;;
        *)
            echo "install.sh: unknown flag: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

log() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

run() {
    if [ "$DRY_RUN" -eq 1 ]; then
        log "  + (dry-run) $*"
    else
        eval "$*"
    fi
}

detect_os_arch() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64|amd64)  ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *) die "unsupported arch: $ARCH (supported: amd64, arm64)" ;;
    esac
    case "$OS" in
        linux|darwin) ;;
        *) die "unsupported os: $OS (supported: linux, darwin)" ;;
    esac
}

resolve_latest_version() {
    eff=$(curl -fsSIL -o /dev/null -w '%{url_effective}' \
        "https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest" 2>/dev/null) || return 1
    case "$eff" in
        */releases/tag/*) printf '%s' "${eff##*/tag/}" | tr -d '\r\n ' ;;
        *) return 1 ;;
    esac
}

maybe_resolve_version() {
    [ "$DRY_RUN" -eq 1 ] && return 0
    [ "$SKIP_DOWNLOAD" -eq 1 ] && return 0
    [ -n "$SOURCE_BASE" ] && return 0
    case "$VERSION" in
        latest|"")
            v=$(resolve_latest_version) || die "could not resolve latest tag; pass --version vX.Y.Z explicitly"
            VERSION="$v"
            ;;
    esac
}

source_tarball_url() {
    v="${VERSION#v}"
    if [ -n "$SOURCE_BASE" ]; then
        printf '%s/%s_%s_%s_%s.tar.gz\n' "$SOURCE_BASE" "$PROJECT" "$v" "$OS" "$ARCH"
    else
        printf 'https://github.com/%s/%s/releases/download/%s/%s_%s_%s_%s.tar.gz\n' \
            "$REPO_OWNER" "$REPO_NAME" "$VERSION" "$PROJECT" "$v" "$OS" "$ARCH"
    fi
}

source_sha_url() {
    if [ -n "$SOURCE_BASE" ]; then
        printf '%s/SHA256SUMS\n' "$SOURCE_BASE"
    else
        printf 'https://github.com/%s/%s/releases/download/%s/SHA256SUMS\n' \
            "$REPO_OWNER" "$REPO_NAME" "$VERSION"
    fi
}

fetch() {
    if [ "$DRY_RUN" -eq 1 ] || [ "$SKIP_DOWNLOAD" -eq 1 ]; then
        log "  + (skip) fetch $1 -> $2"
        return 0
    fi
    curl -fsSL --retry 3 "$1" -o "$2" || die "download failed: $1"
}

verify_sha() {
    if [ "$DRY_RUN" -eq 1 ] || [ "$SKIP_DOWNLOAD" -eq 1 ]; then
        log "  + (skip) verify_sha $1"
        return 0
    fi
    have=""
    if command -v sha256sum >/dev/null 2>&1; then
        have=$(sha256sum "$1" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        have=$(shasum -a 256 "$1" | awk '{print $1}')
    else
        die "neither sha256sum nor shasum found; cannot verify download"
    fi
    if [ "$have" != "$2" ]; then
        die "sha256 mismatch: got $have, want $2"
    fi
}

install_binaries() {
    detect_os_arch
    log "agentchat install (version=$VERSION, os=$OS, arch=$ARCH, prefix=$PREFIX)"

    BIN_DIR="$PREFIX/bin"
    run "mkdir -p '$PREFIX' '$BIN_DIR'"
    run "chmod 700 '$PREFIX'"

    if [ "$SKIP_DOWNLOAD" -eq 1 ] || [ "$DRY_RUN" -eq 1 ]; then
        log "  + (skip) binary install"
        return 0
    fi

    TARBALL_URL=$(source_tarball_url)
    SHA_URL=$(source_sha_url)
    TARBALL=$(mktemp)
    SHA_FILE=$(mktemp)

    log "  + downloading $TARBALL_URL"
    fetch "$TARBALL_URL" "$TARBALL"
    fetch "$SHA_URL"     "$SHA_FILE"

    expect=$(grep "$(basename "$TARBALL_URL")" "$SHA_FILE" | awk '{print $1}')
    [ -n "$expect" ] || die "no sha256 entry for $(basename "$TARBALL_URL") in SHA256SUMS"
    verify_sha "$TARBALL" "$expect"

    tmp=$(mktemp -d)
    tar -xzf "$TARBALL" -C "$tmp"

    for b in agentchatd agentchat; do
        [ -f "$tmp/$b" ] || die "binary $b not found in tarball"
        mv "$tmp/$b" "$BIN_DIR/$b"
        chmod +x "$BIN_DIR/$b"
    done

    rm -rf "$tmp" "$TARBALL" "$SHA_FILE"
    log "  installed $BIN_DIR/agentchatd"
    log "  installed $BIN_DIR/agentchat"
}

print_post_install() {
    BIN_DIR="$PREFIX/bin"
    cat <<EOF

agentchat installed.

  data root: $PREFIX
  binaries:  $BIN_DIR/{agentchatd,agentchat}

EOF
    case ":${PATH:-}:" in
        *":$BIN_DIR:"*) ;;
        *)
            cat <<EOF
$BIN_DIR is not on your PATH. Add this to your shell rc:

    export PATH="$BIN_DIR:\$PATH"

EOF
            ;;
    esac
    cat <<EOF
Start the daemon (first run prints a one-time admin token):

    agentchatd serve

See docs/ in the repo for the full guide.
EOF
}

uninstall_all() {
    BIN_DIR="$PREFIX/bin"
    log "agentchat uninstall (prefix=$PREFIX)"
    run "rm -f '$BIN_DIR/agentchatd' '$BIN_DIR/agentchat'"
    run "rmdir '$BIN_DIR' 2>/dev/null || true"
    if [ "$PURGE" -eq 1 ]; then
        log "  + --purge: removing $PREFIX entirely"
        run "rm -rf '$PREFIX'"
    else
        log "  data preserved at $PREFIX (run with --purge to delete)"
    fi
}

if [ "$UNINSTALL" -eq 1 ]; then
    uninstall_all
    exit 0
fi

maybe_resolve_version
install_binaries
print_post_install
