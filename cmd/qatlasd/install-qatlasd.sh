#!/usr/bin/env sh
# Install a GoReleaser qatlasd archive, without touching config or services.
# Use this script from the SAME release tag as --version during the archive
# format migration: an old running server still embeds the old installer.
# POSIX sh; curl (preferred) or GNU wget, tar, awk and sha256sum/shasum.
# Checksums detect damaged downloads; authenticity still relies on HTTPS and
# the release publisher. See release attestations for independent provenance.
set -eu
umask 077
LC_ALL=C
export LC_ALL
# Do not let tar/gzip options in the caller's environment alter validation or
# extraction (e.g. TAR_OPTIONS=--transform=...). Never extract an archive tree.
unset TAR_OPTIONS GZIP GZIP_OPT CDPATH

REPO="${QATLAS_REPO:-IAI-USTC-Quantum/QuantumAtlas}"
INSTALL_DIR="${QATLAS_INSTALL_DIR:-${HOME:?HOME is required}/.local/bin}"
VERSION="${QATLAS_VERSION:-latest}"
TIMEOUT="${QATLAS_INSTALL_TIMEOUT:-60}"
VERSION_TIMEOUT="${QATLAS_INSTALL_VERSION_TIMEOUT:-10}"
BASE=https://github.com
WORK=
STAGE=
command_pid=
watchdog_pid=

info() { printf '==> %s\n' "$1"; }
fail() { printf 'qatlas-installer: %s\n' "$1" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }
cleanup() {
    [ -z "$command_pid" ] || kill -KILL "$command_pid" 2>/dev/null || :
    [ -z "$watchdog_pid" ] || kill "$watchdog_pid" 2>/dev/null || :
    [ -z "$STAGE" ] || rm -rf "$STAGE"
    [ -z "$WORK" ] || rm -rf "$WORK"
}
trap cleanup 0
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

while [ $# -gt 0 ]; do
    case "$1" in
        --version|--dir)
            [ $# -ge 2 ] && [ -n "$2" ] || fail "$1 requires a value"
            case "$1" in --version) VERSION=$2 ;; --dir) INSTALL_DIR=$2 ;; esac
            shift 2 ;;
        --help|-h)
            cat <<'EOF'
qatlasd installer
Usage: sh install-qatlasd.sh [--version <tag>] [--dir <path>]

  --version <tag>  Release tag, with or without v (default: latest)
  --dir <path>     Install directory (default: $HOME/.local/bin)

Environment: QATLAS_VERSION, QATLAS_INSTALL_DIR, QATLAS_REPO (GitHub owner/repo).
QATLAS_INSTALL_TIMEOUT (default 60) and QATLAS_INSTALL_VERSION_TIMEOUT
(default 10) bound each download/archive operation and the --version check
in seconds (1..300). Needs curl or GNU wget, tar, awk, sha256sum or shasum.

Only linux/amd64, linux/arm64 and darwin/arm64 are published.
This installs only the binary. It does not sudo, initialize/overwrite config,
register/restart a service or migrate a database. Next: qatlasd config init.
EOF
            exit 0 ;;
        *) fail "unknown argument: $1" ;;
    esac
done

for seconds in "$TIMEOUT" "$VERSION_TIMEOUT"; do
    case "$seconds" in ''|*[!0-9]*|0*) fail "timeouts must be integers in 1..300" ;; esac
    [ "${#seconds}" -le 3 ] && [ "$seconds" -le 300 ] || fail "timeouts must be in 1..300"
done
printf '%s\n' "$REPO" | awk '
    /^[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+$/ && $0 !~ /\/\.\.?$/ { good=1 }
    NR!=1 { bad=1 }
    END { exit !good || bad }
' || fail "invalid GitHub owner/repo"
[ -n "$INSTALL_DIR" ] || fail "empty install directory"
# An absolute path also prevents leading '-' from becoming a command option.
case "$INSTALL_DIR" in /*) ;; *) INSTALL_DIR="$PWD/$INSTALL_DIR" ;; esac

# A deliberately test-only seam, not an insecure mirror setting. Only numeric
# loopback + an explicit port is accepted, and EVERY redirect is checked too.
# The production default and all its redirects remain HTTPS-only.
if [ -n "${QATLAS_INSTALL_TEST_BASE_URL:-}" ]; then
    BASE=$QATLAS_INSTALL_TEST_BASE_URL
    printf '%s\n' "$BASE" | awk '
        /^http:\/\/127\.0\.0\.1:[0-9]+$/ {
            sub(/^.*:/, ""); if (length($0)<=5 && $0+0>0 && $0+0<=65535) good=1
        }
        NR!=1 { bad=1 }
        END { exit !good || bad }
    ' || fail "test endpoint must be http://127.0.0.1:<port>"
    PROTO='=http'
else
    PROTO='=https'
fi

OS=$(uname -s)
ARCH=$(uname -m)
case "$OS" in Linux) OS=linux ;; Darwin) OS=darwin ;; *) fail "unsupported OS: $OS" ;; esac
case "$ARCH" in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) fail "unsupported architecture: $ARCH" ;; esac
case "$OS/$ARCH" in
    linux/amd64|linux/arm64|darwin/arm64) ;;
    *) fail "unsupported platform: $OS/$ARCH (Intel Mac binaries are not published)" ;;
esac
for tool in tar awk mktemp chmod mv cmp; do
    have "$tool" || fail "need $tool on PATH"
done
if have curl; then
    CLIENT=curl
elif have wget; then
    CLIENT=wget
    # BusyBox wget lacks redirect control. Fail closed rather than following
    # an unchecked downgrade/remote redirect; curl works on BusyBox systems.
    wget --help 2>&1 | awk '/--max-redirect/ { found=1 } END { exit !found }' ||
        fail "need curl or GNU wget with --max-redirect support"
else
    fail "need curl or GNU wget on PATH"
fi
if have sha256sum; then HASH=sha256sum
elif have shasum; then HASH=shasum
else fail "need sha256sum or shasum on PATH"
fi

# POSIX watchdog: macOS does not ship timeout(1). Kill/reap the timer as well
# as the command, so successful installs do not leave sleeping children.
# Commands get no stdin: this remains safe when the installer is piped to sh.
bounded() {
    limit=$1
    shift
    "$@" </dev/null &
    command_pid=$!
    (
        sleep_pid=
        trap '[ -z "$sleep_pid" ] || kill "$sleep_pid" 2>/dev/null; exit 0' TERM INT HUP
        sleep "$limit" &
        sleep_pid=$!
        wait "$sleep_pid"
        kill -KILL "$command_pid" 2>/dev/null || :
    ) >/dev/null 2>&1 &
    watchdog_pid=$!
    if wait "$command_pid"; then result=0; else result=$?; fi
    command_pid=
    kill "$watchdog_pid" 2>/dev/null || :
    wait "$watchdog_pid" 2>/dev/null || :
    watchdog_pid=
    return "$result"
}

WORK=$(mktemp -d "${TMPDIR:-/tmp}/qatlasd-install.XXXXXX") || fail "cannot create download staging directory"

# Follow redirects ourselves, including wget's, so HTTPS cannot downgrade and
# a fixture cannot escape loopback. Configuration files are disabled for both
# clients. There are no retries and every hop has a finite wall-clock limit.
fetch() {
    FETCH_URL=$1
    output=$2
    hops=0
    while :; do
        if [ -n "${QATLAS_INSTALL_TEST_BASE_URL:-}" ]; then
            case "$FETCH_URL" in "$BASE/"*) ;; *) fail "redirect leaves test endpoint" ;; esac
        else
            case "$FETCH_URL" in https://*) ;; *) fail "refusing non-HTTPS download/redirect" ;; esac
        fi
        ok=0
        if [ "$CLIENT" = curl ]; then
            bounded "$TIMEOUT" curl -q --fail --silent --show-error \
                --proto "$PROTO" --connect-timeout "$TIMEOUT" --max-time "$TIMEOUT" \
                --dump-header "$WORK/headers" --output "$output" "$FETCH_URL" && ok=1
        else
            bounded "$TIMEOUT" wget --no-config --server-response --max-redirect=0 \
                --timeout="$TIMEOUT" --tries=1 -O "$output" "$FETCH_URL" 2>"$WORK/headers" && ok=1
        fi
        status=$(awk '$1 ~ /^HTTP\// { code=$2 } END { print code }' "$WORK/headers")
        case "$status" in
            301|302|303|307|308)
                location=$(awk -v client="$CLIENT" '
                    # wget repeats Location in an unindented diagnostic;
                    # only its indented server-response lines are headers.
                    tolower($1)=="location:" && (client!="wget" || $0 ~ /^[ \t]/) {
                        sub(/^[ \t]*[^:]+:[ \t]*/, ""); sub(/\r$/, "");
                        value=$0; count++
                    }
                    END { if (count==1) print value; else exit 1 }
                ' "$WORK/headers") || fail "missing/duplicate redirect location"
                [ -n "$location" ] || fail "empty redirect location"
                # GitHub also uses origin-relative redirects.
                case "$location" in
                    //*) fail "refusing scheme-relative redirect" ;;
                    /*) origin=${FETCH_URL#*://}; origin=${origin%%/*}; FETCH_URL="${FETCH_URL%%://*}://$origin$location" ;;
                    *) FETCH_URL=$location ;;
                esac
                hops=$((hops + 1))
                [ "$hops" -le 5 ] || fail "too many redirects" ;;
            200)
                [ "$ok" -eq 1 ] || fail "download failed or timed out: $FETCH_URL"
                return 0 ;;
            *) fail "download failed or timed out (HTTP ${status:-unknown}): $FETCH_URL" ;;
        esac
    done
}

if [ "$VERSION" = latest ]; then
    info "Resolving latest release for $REPO"
    fetch "$BASE/$REPO/releases/latest" "$WORK/latest"
    case "$FETCH_URL" in
        "$BASE/$REPO/releases/tag/"*) TAG=${FETCH_URL#"$BASE/$REPO/releases/tag/"} ;;
        *) fail "could not resolve latest release tag" ;;
    esac
else
    TAG=$VERSION
    case "$TAG" in v*) ;; *) TAG="v$TAG" ;; esac
fi
RELEASE=${TAG#v}
# Reject dev, paths, query strings and shell/URL metacharacters. SemVer build
# metadata and prereleases are retained, never substring-matched to --version.
printf '%s\n' "$RELEASE" | awk '
    /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$/ { good=1 }
    NR!=1 { bad=1 }
    END { exit !good || bad }
' || fail "invalid release version: $TAG"
ASSET="qatlasd_${RELEASE}_${OS}_${ARCH}.tar.gz"
CHECKSUMS="qatlasd_${RELEASE}_checksums.txt"
info "Installing $TAG for $OS/$ARCH"
fetch "$BASE/$REPO/releases/download/$TAG/$CHECKSUMS" "$WORK/checksums"
fetch "$BASE/$REPO/releases/download/$TAG/$ASSET" "$WORK/archive.tar.gz"

expected=$(awk -v asset="$ASSET" '
    { name=$2; sub(/^\*/, "", name) }
    name==asset {
        count++
        if (NF!=2 || length($1)!=64 || $1 ~ /[^0-9a-fA-F]/) bad=1
        hash=tolower($1)
    }
    END { if (count!=1 || bad) exit 1; print hash }
' "$WORK/checksums") || fail "missing, duplicate or malformed checksum for $ASSET"
if [ "$HASH" = sha256sum ]; then
    bounded "$TIMEOUT" sha256sum "$WORK/archive.tar.gz" >"$WORK/hash" || fail "checksum calculation failed"
else
    bounded "$TIMEOUT" shasum -a 256 "$WORK/archive.tar.gz" >"$WORK/hash" || fail "checksum calculation failed"
fi
actual=$(awk 'NR==1 { print tolower($1) }' "$WORK/hash")
[ "$actual" = "$expected" ] || fail "archive checksum mismatch"

# Inspect names and types separately: verbose layouts differ on GNU/BSD tar,
# but their first type character is stable. The conservative ASCII path set
# rejects escaped/control/newline names on GNU, BSD and BusyBox tar alike.
# No . or .. components, absolute paths, links, devices, duplicate paths or
# nested qatlasd are allowed. Extra ordinary release docs are never extracted.
bounded "$TIMEOUT" tar -tzf "$WORK/archive.tar.gz" >"$WORK/names" || fail "cannot list archive names"
bounded "$TIMEOUT" tar -tvzf "$WORK/archive.tar.gz" >"$WORK/types" || fail "cannot list archive types"
awk '
    NR==FNR {
        name=$0
        if (name=="qatlasd/") bad=1
        if (name !~ /^[A-Za-z0-9_][A-Za-z0-9_.\/-]*$/ || name ~ /\/\//) bad=1
        sub(/\/$/, "", name)
        n=split(name, parts, "/")
        for (i=1; i<=n; i++) if (parts[i]=="." || parts[i]==".." || (parts[i]=="qatlasd" && n!=1)) bad=1
        if (seen[name]++) bad=1
        names[FNR]=name
        if (name=="qatlasd") binary++
        count++
        next
    }
    {
        type=substr($0, 1, 1)
        if (type!="-" && type!="d") bad=1
        if (names[FNR]=="qatlasd" && type!="-") bad=1
        types++
    }
    END { if (bad || binary!=1 || count!=types) exit 1 }
' "$WORK/names" "$WORK/types" || fail "unsafe archive: require one ordinary qatlasd and unique safe paths"

mkdir -p "$INSTALL_DIR" || fail "cannot create install directory: $INSTALL_DIR"
INSTALL_DIR=$(cd "$INSTALL_DIR" && pwd -P) || fail "cannot access install directory"
DEST="$INSTALL_DIR/qatlasd"
[ ! -L "$DEST" ] && { [ ! -e "$DEST" ] || [ -f "$DEST" ]; } || fail "destination is not an ordinary file: $DEST"
STAGE=$(mktemp -d "$INSTALL_DIR/.qatlasd-install.XXXXXX") || fail "cannot stage binary in install directory"
bounded "$TIMEOUT" tar -xOzf "$WORK/archive.tar.gz" qatlasd >"$STAGE/qatlasd" || fail "binary extraction failed"
[ -s "$STAGE/qatlasd" ] || fail "archive binary is empty"
chmod 0755 "$STAGE/qatlasd" || fail "cannot make staged binary executable"
bounded "$VERSION_TIMEOUT" "$STAGE/qatlasd" --version >"$WORK/version" 2>"$WORK/version-error" || fail "staged binary --version failed or timed out"
# Compare bytes, including the final newline; shell substitutions strip it
# and some awk implementations truncate lines containing a NUL byte.
printf 'qatlasd version %s\n' "$RELEASE" >"$WORK/expected-version"
cmp -s "$WORK/version" "$WORK/expected-version" || fail "staged binary version does not exactly match $RELEASE"

# Same-filesystem rename: never truncate the previous executable. Passing the
# PARENT directory also prevents mv from treating a qatlasd directory as a
# destination directory and silently putting the candidate inside it.
[ ! -L "$DEST" ] && { [ ! -e "$DEST" ] || [ -f "$DEST" ]; } || fail "destination changed during install"
mv -f "$STAGE/qatlasd" "$INSTALL_DIR/." || fail "atomic replacement failed; previous binary retained"
info "Installed $DEST ($RELEASE)"
case ":$PATH:" in *":$INSTALL_DIR:"*) ;; *) info "Add $INSTALL_DIR to your PATH" ;; esac
cat <<EOF

Next: qatlasd config init
  Then review your config before running qatlasd serve or qatlasd service install.
  Existing config and services were not changed. No sudo was run.
  Replacing a binary does not roll back database migrations; keep backups.
  Documentation: https://github.com/$REPO
EOF
