#!/usr/bin/env sh
# QuantumAtlas server installer
#
# Downloads the latest qatlasd binary from GitHub Releases and
# installs it to ~/.local/bin/qatlasd.
#
# POSIX sh (no bash-isms) so it runs on Alpine / BusyBox / macOS sh too.
# Strictly binary-install only — no `service install` chaining, no TTY
# reopen. The chaining was attempted earlier but couldn't be made safe
# under `curl | sh` on dash (Debian/Ubuntu /bin/sh): dash is a
# streaming parser that pre-buffers script bytes from stdin, and any
# subsequent `exec </dev/tty` either hangs waiting on tty input or
# corrupts the already-buffered slice. Bash sidesteps this by slurping
# the whole script, but we can't depend on bash on minimal targets.
# After install, the printed next-step calls `qatlasd service
# install` explicitly — that's a real cobra program with its own TTY
# handling, not a sub-shell, so it has none of the curl|sh problems.
#
# Usage:
#   curl -fsSL https://quantum-atlas.ai/install-server.sh | sh
#   curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- --version v0.2.5
#   curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- --dir /opt/qatlas/bin
#
# Env overrides (equivalent to flags):
#   QATLAS_INSTALL_DIR   target directory (default: $HOME/.local/bin)
#   QATLAS_VERSION       release tag to install (default: latest)
#   QATLAS_REPO          github owner/repo (default: IAI-USTC-Quantum/QuantumAtlas)

set -eu

REPO="${QATLAS_REPO:-IAI-USTC-Quantum/QuantumAtlas}"
INSTALL_DIR="${QATLAS_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${QATLAS_VERSION:-latest}"

# --- Argument parsing ------------------------------------------------------
while [ $# -gt 0 ]; do
    case "$1" in
        --version)        VERSION="$2"; shift 2 ;;
        --dir)            INSTALL_DIR="$2"; shift 2 ;;
        --help|-h)
            cat <<'EOF'
qatlasd installer

Usage:
  curl -fsSL https://quantum-atlas.ai/install-server.sh | sh
  curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- [options]

Options:
  --version <tag>   Install a specific release tag (default: latest)
  --dir <path>      Binary install directory (default: $HOME/.local/bin)
  -h, --help        Show this help

Environment overrides:
  QATLAS_INSTALL_DIR  Same as --dir
  QATLAS_VERSION      Same as --version
  QATLAS_REPO         GitHub owner/repo
                      (default: IAI-USTC-Quantum/QuantumAtlas)

After install, run `qatlasd service install` to register a
managed systemd / launchd / SCM service. That command has its own
interactive flow and supports --mode / --dotenv-path / --bind /
--force flags for unattended use.
EOF
            exit 0
            ;;
        *)
            echo "qatlas-installer: unknown argument: $1" >&2
            echo "(service install / TTY-driven flags moved to 'qatlasd service install')" >&2
            exit 2
            ;;
    esac
done

# --- Pretty printing -------------------------------------------------------
if [ -t 1 ]; then
    bold=$(printf '\033[1m')
    green=$(printf '\033[32m')
    yellow=$(printf '\033[33m')
    red=$(printf '\033[31m')
    reset=$(printf '\033[0m')
else
    bold=""; green=""; yellow=""; red=""; reset=""
fi

info()  { printf "%s==>%s %s\n" "$green" "$reset" "$1"; }
warn()  { printf "%s==>%s %s\n" "$yellow" "$reset" "$1" >&2; }
fail()  { printf "%serror:%s %s\n" "$red" "$reset" "$1" >&2; exit 1; }

# --- OS / arch detection ---------------------------------------------------
OS="$(uname -s)"
case "$OS" in
    Linux)  OS=linux ;;
    Darwin) OS=darwin ;;
    *) fail "unsupported OS: $OS (only Linux + macOS are published)" ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) fail "unsupported architecture: $ARCH (need x86_64 or aarch64/arm64)" ;;
esac

ASSET="qatlasd-${OS}-${ARCH}"
info "Target: ${bold}${OS}/${ARCH}${reset} (asset: ${ASSET})"

# darwin-amd64 isn't published because GitHub's free-tier macos-13 Intel
# runners spend 20-40 minutes in queue (blocking the entire release).
# Tell Intel Mac users explicitly so they don't see a generic 404.
if [ "$OS" = "darwin" ] && [ "$ARCH" = "amd64" ]; then
    fail "Intel Mac binary not published; use \`go install github.com/$REPO/cmd/qatlasd@latest\` (needs Go 1.26+) or build from source"
fi

# --- Required tools --------------------------------------------------------
have() { command -v "$1" >/dev/null 2>&1; }

if have curl; then
    fetch() { curl -fsSL "$1" -o "$2"; }
    fetch_stdout() { curl -fsSL "$1"; }
elif have wget; then
    fetch() { wget -qO "$2" "$1"; }
    fetch_stdout() { wget -qO- "$1"; }
else
    fail "need curl or wget on PATH"
fi

have install || fail "need the 'install' command (coreutils)"

# --- Resolve release tag ---------------------------------------------------
if [ "$VERSION" = "latest" ]; then
    info "Resolving latest release tag from github.com/$REPO ..."
    # GitHub redirects /releases/latest to /releases/tag/<v...>; cheaper
    # than the JSON API and avoids the unauthenticated rate limit.
    #
    # When the repo doesn't exist or has no releases the redirect chain
    # stops at the same /releases/latest URL — the resolver leaves no
    # "/tag/" segment to strip. Detect that and fail loud rather than
    # constructing a garbage download URL out of the URL itself.
    LATEST_URL="https://github.com/$REPO/releases/latest"
    if have curl; then
        RESOLVED="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
            "$LATEST_URL" 2>/dev/null || true)"
    else
        RESOLVED="$(wget -S --max-redirect=0 -O /dev/null \
            "$LATEST_URL" 2>&1 \
            | sed -n 's#.*Location: \(.*\)#\1#p' | head -n1)"
    fi
    case "$RESOLVED" in
        *"/tag/"*) TAG="${RESOLVED##*/tag/}" ;;
        *) fail "could not resolve latest release for $REPO (404? no releases? network?)" ;;
    esac
    [ -n "$TAG" ] || fail "could not resolve latest release tag for $REPO"
else
    TAG="$VERSION"
fi
info "Release: ${bold}${TAG}${reset}"

URL="https://github.com/$REPO/releases/download/$TAG/$ASSET"
info "Downloading $URL ..."

TMPDIR="$(mktemp -d 2>/dev/null || mktemp -d -t qatlas)"
trap 'rm -rf "$TMPDIR"' EXIT INT TERM
TMPBIN="$TMPDIR/$ASSET"

fetch "$URL" "$TMPBIN" || fail "download failed; the release may not include $ASSET"

# Optional checksum verification — only enforced when checksums.txt is
# published for this release (Go cross-compile job in release.yml writes
# one). Older Python-only releases skip silently.
CHECKSUMS_URL="https://github.com/$REPO/releases/download/$TAG/checksums.txt"
if have sha256sum || have shasum; then
    if fetch_stdout "$CHECKSUMS_URL" > "$TMPDIR/checksums.txt" 2>/dev/null; then
        EXPECTED="$(grep " $ASSET\$" "$TMPDIR/checksums.txt" | awk '{print $1}')"
        if [ -n "$EXPECTED" ]; then
            if have sha256sum; then
                ACTUAL="$(sha256sum "$TMPBIN" | awk '{print $1}')"
            else
                ACTUAL="$(shasum -a 256 "$TMPBIN" | awk '{print $1}')"
            fi
            if [ "$EXPECTED" != "$ACTUAL" ]; then
                fail "sha256 mismatch! expected=$EXPECTED actual=$ACTUAL"
            fi
            info "sha256 verified: ${ACTUAL}"
        fi
    fi
fi

# --- Install binary --------------------------------------------------------
mkdir -p "$INSTALL_DIR" || fail "cannot create $INSTALL_DIR"
DEST="$INSTALL_DIR/qatlasd"

# Atomic install: install(1) fsyncs and chmods 0755.
install -m 0755 "$TMPBIN" "$DEST" || fail "install to $DEST failed"
info "Installed ${bold}${DEST}${reset}"

# Quick sanity check.
if ! "$DEST" --version >/dev/null 2>&1; then
    warn "Binary installed but --version exited non-zero. May be incompatible with this glibc."
fi

# --- PATH hint -------------------------------------------------------------
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
        warn "$INSTALL_DIR is not on your PATH. Add it to your shell rc:"
        printf '    export PATH="%s:$PATH"\n' "$INSTALL_DIR"
        ;;
esac

# --- Next steps ------------------------------------------------------------
cat <<EOF

${bold}Next steps${reset}

  1. Create a .env (see https://github.com/$REPO/blob/main/.env.example):
       mkdir -p ~/QuantumAtlas
       curl -fsSL https://raw.githubusercontent.com/$REPO/main/.env.example \\
            -o ~/QuantumAtlas/.env

  2. Install as a systemd service (Linux) / launchd (macOS):
       $DEST service install
       # …or fully unattended:
       $DEST service install --mode user --dotenv-path ~/QuantumAtlas/.env --force

  3. Or run in the foreground for a smoke test:
       $DEST serve

  Documentation: https://github.com/$REPO

EOF
