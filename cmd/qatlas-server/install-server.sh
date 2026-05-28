#!/usr/bin/env sh
# QuantumAtlas server installer
#
# Downloads the latest qatlas-server binary from GitHub releases,
# installs to ~/.local/bin/qatlas-server, and (optionally) chains into
# `qatlas-server service install` so you get a managed systemd / launchd
# / SCM unit in one go.
#
# POSIX sh (no bash-isms) so it runs on minimal Alpine / BusyBox / macOS
# sh too. Mirrors the rustup / claude-code / oh-my-zsh "curl | sh"
# UX while still being safe to run under `set -eu`.
#
# Usage:
#   # Interactive (TTY) — installs binary, then asks if you want a service
#   curl -fsSL https://quantum-atlas.ai/install-server.sh | sh
#
#   # Fully automatic (CI / agent) — install binary AND register service,
#   # no prompts. Need --mode + --dotenv-path because service install
#   # cannot guess them without a TTY.
#   curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- \
#       --yes --service --mode user --dotenv-path ~/QuantumAtlas/.env
#
#   # Binary only, skip service install entirely
#   curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- --no-service
#
# Env overrides (equivalent to flags):
#   QATLAS_INSTALL_DIR   target directory (default: $HOME/.local/bin)
#   QATLAS_VERSION       release tag to install (default: latest)
#   QATLAS_REPO          github owner/repo (default: IAI-USTC-Quantum/QuantumAtlas)

set -eu

REPO="${QATLAS_REPO:-IAI-USTC-Quantum/QuantumAtlas}"
INSTALL_DIR="${QATLAS_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${QATLAS_VERSION:-latest}"

# Tri-state SERVICE_MODE: "auto" (offer when interactive), "yes" (--service),
# "no" (--no-service). "auto" + no TTY collapses to "no".
SERVICE_MODE="auto"
ASSUME_YES=0
SVC_MODE=""        # --mode for service install (user|system)
SVC_DOTENV=""      # --dotenv-path for service install
SVC_BIND=""        # --bind for service install (HTTP listen addr)

# --- Argument parsing ------------------------------------------------------
while [ $# -gt 0 ]; do
    case "$1" in
        --version)        VERSION="$2"; shift 2 ;;
        --dir)            INSTALL_DIR="$2"; shift 2 ;;
        --service)        SERVICE_MODE="yes"; shift ;;
        --no-service)     SERVICE_MODE="no"; shift ;;
        --yes|-y)         ASSUME_YES=1; shift ;;
        --mode)           SVC_MODE="$2"; shift 2 ;;
        --dotenv-path)    SVC_DOTENV="$2"; shift 2 ;;
        --bind)           SVC_BIND="$2"; shift 2 ;;
        --help|-h)
            cat <<'EOF'
qatlas-server installer

Usage:
  curl -fsSL https://quantum-atlas.ai/install-server.sh | sh
  curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- [options]

Install options:
  --version <tag>      Install a specific release tag (default: latest)
  --dir <path>         Binary install directory (default: $HOME/.local/bin)

Service install (after binary):
  --service            Run `qatlas-server service install` after download.
                       In non-TTY contexts must be paired with --mode and
                       --dotenv-path so the install can run unattended.
  --no-service         Skip the service install step entirely.
                       (default in non-TTY contexts)
  --yes, -y            Answer yes to all prompts; in TTY contexts this
                       implies --service unless --no-service is given.
  --mode user|system   Passed through to `service install --mode`
  --dotenv-path PATH   Passed through to `service install --dotenv-path`
  --bind HOST:PORT     Passed through to `service install --bind`
                       (default: 127.0.0.1:4200)

Misc:
  -h, --help           Show this help

Environment overrides:
  QATLAS_INSTALL_DIR   Same as --dir
  QATLAS_VERSION       Same as --version
  QATLAS_REPO          GitHub owner/repo
                       (default: IAI-USTC-Quantum/QuantumAtlas)
EOF
            exit 0
            ;;
        *)
            echo "qatlas-installer: unknown argument: $1" >&2
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

# --- TTY detection ---------------------------------------------------------
# Standard curl|sh has stdin = pipe (not a TTY) but the user is at a real
# terminal. Reopen stdin from /dev/tty if available so we can prompt and
# so the chained `service install` sees a real TTY for its own prompts.
# Matches the rustup / nvm / oh-my-zsh pattern.
#
# Caveat: in sandboxed environments (some CI runners, containers without
# /dev/tty bind-mounted, Docker without -t) the file may exist but not
# be openable. Probe first in a subshell to avoid `exec` blowing up.
has_tty=0
if [ -t 0 ]; then
    has_tty=1
elif [ -c /dev/tty ] && (true </dev/tty) 2>/dev/null; then
    # /dev/tty is a character device AND can be opened — safe to reopen
    # the script's stdin from it. After this, [ -t 0 ] returns true so
    # downstream isatty() checks (incl. the chained binary's) work.
    exec </dev/tty
    has_tty=1
fi

# Compose a prompt that respects --yes. Default answer ('y' / 'n') is used
# in non-TTY mode and is treated as the user's answer under --yes.
prompt_yes_no() {
    question="$1"
    default="$2"   # "y" or "n"
    if [ "$ASSUME_YES" = 1 ]; then
        [ "$default" = "y" ] || return 1
        return 0
    fi
    if [ "$has_tty" != 1 ]; then
        [ "$default" = "y" ] || return 1
        return 0
    fi
    hint="[Y/n]"
    [ "$default" = "n" ] && hint="[y/N]"
    printf "%s %s " "$question" "$hint"
    read ans || ans=""
    case "$(echo "$ans" | tr '[:upper:]' '[:lower:]' | tr -d ' ')" in
        y|yes) return 0 ;;
        n|no)  return 1 ;;
        "")    [ "$default" = "y" ] && return 0 || return 1 ;;
        *)     [ "$default" = "y" ] && return 0 || return 1 ;;
    esac
}

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

ASSET="qatlas-server-${OS}-${ARCH}"
info "Target: ${bold}${OS}/${ARCH}${reset} (asset: ${ASSET})"

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
    # stops at the same /releases/latest URL — sed then leaves the full
    # URL in TAG (no "/tag/" to strip). We validate by re-checking that
    # the URL was actually rewritten to a tag form, so a 404 yields a
    # clean fail() instead of a bogus download URL constructed below.
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
DEST="$INSTALL_DIR/qatlas-server"

# Atomic install: install(1) fsyncs and chmods 0755.
install -m 0755 "$TMPBIN" "$DEST" || fail "install to $DEST failed"
info "Installed ${bold}${DEST}${reset}"

# Quick sanity check.
if ! "$DEST" --help >/dev/null 2>&1; then
    warn "Binary installed but --help exited non-zero. May be incompatible with this glibc."
fi

# --- PATH hint -------------------------------------------------------------
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
        warn "$INSTALL_DIR is not on your PATH. Add it to your shell rc:"
        printf '    export PATH="%s:$PATH"\n' "$INSTALL_DIR"
        ;;
esac

# --- Service install (chained, optional) -----------------------------------
# Decision matrix:
#   SERVICE_MODE=no              -> skip, print pointer
#   SERVICE_MODE=yes             -> run service install (need --mode + --dotenv-path
#                                   when non-TTY; binary will reject otherwise)
#   SERVICE_MODE=auto + TTY      -> ask
#   SERVICE_MODE=auto + non-TTY  -> skip, print pointer
#
# Under --yes, the prompt collapses to "yes". The chained `service install`
# command then runs its own interactive flow (mode confirmation, .env
# auto-detect, unit preview), reusing whatever flags we pass through.

do_service_install=0
case "$SERVICE_MODE" in
    yes)
        do_service_install=1
        ;;
    no)
        do_service_install=0
        ;;
    auto)
        if [ "$has_tty" = 1 ]; then
            echo
            if prompt_yes_no "Install qatlas-server as a managed service now?" "y"; then
                do_service_install=1
            fi
        fi
        ;;
esac

if [ "$do_service_install" = 1 ]; then
    set -- "service" "install"
    [ -n "$SVC_MODE" ]   && set -- "$@" "--mode" "$SVC_MODE"
    [ -n "$SVC_DOTENV" ] && set -- "$@" "--dotenv-path" "$SVC_DOTENV"
    [ -n "$SVC_BIND" ]   && set -- "$@" "--bind" "$SVC_BIND"
    # --yes from install-server.sh implies --force on the inner command:
    # the user has already opted into "no more prompts".
    if [ "$ASSUME_YES" = 1 ]; then
        set -- "$@" "--force"
        # And if non-TTY, --mode is mandatory — fail loud rather than
        # let the binary's "non-interactive install requires --mode and
        # --force" error confuse the user.
        if [ "$has_tty" != 1 ] && [ -z "$SVC_MODE" ]; then
            fail "--yes --service in non-interactive context requires --mode (user|system)"
        fi
    fi
    info "Chaining into: ${bold}$DEST $*${reset}"
    # The binary's own isTTY() check inspects stdin; since we already
    # `exec </dev/tty` above when a TTY was available, it will see one.
    exec "$DEST" "$@"
fi

# --- Next steps (when service install was skipped) -------------------------
cat <<EOF

${bold}Next steps${reset}

  1. Create a .env (see https://github.com/$REPO/blob/main/.env.example):
       mkdir -p ~/QuantumAtlas
       curl -fsSL https://raw.githubusercontent.com/$REPO/main/.env.example \\
            -o ~/QuantumAtlas/.env

  2. Install as a systemd service (Linux) / launchd (macOS):
       $DEST service install            # interactive, asks for mode + .env path
       # or fully unattended:
       $DEST service install --mode user --dotenv-path ~/QuantumAtlas/.env --force

  3. Or run in the foreground for a smoke test:
       $DEST serve

  Documentation: https://github.com/$REPO

EOF
