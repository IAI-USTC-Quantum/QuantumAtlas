#!/usr/bin/env bash
# update-docs.sh — refresh the qatlasd docs override directory
# (~/.qatlas/docs/{doc,devdoc}) WITHOUT restarting qatlasd.
#
# qatlasd serves /doc and /devdoc from that directory when it is
# populated, falling back to the copies embedded in the binary
# otherwise (see internal/routes/docs.go). This script is the "deploy"
# half of the docs pipeline; .github/workflows/docs.yml is the "build"
# half.
#
# Two modes:
#
#   default          Pull the content-only qatlas-docs image built by CI
#                    and copy the sites out of it (deploy host: pull only,
#                    never build):
#
#                      ./deploy/update-docs.sh
#                      DOCS_REF=<sha> ./deploy/update-docs.sh   # pin/rollback
#
#   --build-local    Copy already-built Sphinx sites from a LOCAL checkout
#                    (web/public/doc and web/public/devdoc). Build first with
#                    uv run --locked --script .github/scripts/build_docs.py;
#                    this mode does not run
#                    Sphinx again:
#
#                      ./deploy/update-docs.sh --build-local
#
# Environment:
#   DOCS_DIR     target directory          (default: ~/.qatlas/docs)
#   DOCS_REF     image tag to pull         (default: latest)
#   DOCS_IMAGE   image reference           (default: ghcr.io/iai-ustc-quantum/qatlas-docs)
#   REPO_DIR     checkout for --build-local (default: repo root of this script)
#
# Remote docs directory? Run --build-local on any dev machine with a
# local DOCS_DIR, then:  rsync -a "$DOCS_DIR/" host:~/.qatlas/docs/
set -euo pipefail

DOCS_DIR="${DOCS_DIR:-$HOME/.qatlas/docs}"
DOCS_REF="${DOCS_REF:-latest}"
DOCS_IMAGE="${DOCS_IMAGE:-ghcr.io/iai-ustc-quantum/qatlas-docs}"
REPO_DIR="${REPO_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"

BUILD_LOCAL=0
for arg in "$@"; do
  case "$arg" in
    --build-local) BUILD_LOCAL=1 ;;
    -h|--help) sed -n '2,40p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "unknown argument: $arg (try --help)" >&2; exit 2 ;;
  esac
done

mkdir -p "$DOCS_DIR"
chmod a+rx "$DOCS_DIR"
stage="$(mktemp -d "$DOCS_DIR/.update.XXXXXXXX")"
backup=""
cid=""
moved=()
cleanup() {
  result=$?
  trap - EXIT
  if [ "$result" -ne 0 ] && [ -n "$backup" ]; then
    for name in "${moved[@]}"; do
      if [ -e "$DOCS_DIR/$name" ]; then mv "$DOCS_DIR/$name" "$stage/failed-$name"; fi
      if [ -e "$backup/$name" ]; then mv "$backup/$name" "$DOCS_DIR/$name"; fi
    done
    echo "docs refresh failed; previous content restored" >&2
  fi
  if [ -n "$cid" ]; then docker rm "$cid" >/dev/null || true; fi
  rm -rf "$stage"
  exit "$result"
}
trap cleanup EXIT

if [ "$BUILD_LOCAL" -eq 1 ]; then
  echo ">> staging prebuilt Sphinx sites from $REPO_DIR/web/public"
  cp -a "$REPO_DIR/web/public/doc" "$stage/doc"
  cp -a "$REPO_DIR/web/public/devdoc" "$stage/devdoc"
  git -C "$REPO_DIR" rev-parse HEAD > "$stage/VERSION"
else
  echo ">> pulling $DOCS_IMAGE:$DOCS_REF"
  # An existing exact local image can still be used on an offline host.
  docker pull "$DOCS_IMAGE:$DOCS_REF" || \
    docker image inspect "$DOCS_IMAGE:$DOCS_REF" >/dev/null
  cid="$(docker create "$DOCS_IMAGE:$DOCS_REF" true)"
  # Extract both sites BEFORE touching the serving tree. Never preserve root
  # ownership from the scratch image when running as a deployment account.
  for name in doc devdoc VERSION; do
    docker cp "$cid:/$name" - | tar -x -C "$stage" --no-same-owner
  done
fi

test -s "$stage/doc/index.html"
test -s "$stage/devdoc/dev/index.html"
[[ "$(cat "$stage/VERSION")" =~ ^[0-9a-f]{40}$ ]]
chmod -R a+rX "$stage/doc" "$stage/devdoc" "$stage/VERSION"
backup="$(mktemp -d "$DOCS_DIR/.previous.XXXXXXXX")"
for name in doc devdoc VERSION; do
  if [ -e "$DOCS_DIR/$name" ]; then mv "$DOCS_DIR/$name" "$backup/$name"; fi
  moved+=("$name")
  mv "$stage/$name" "$DOCS_DIR/$name"
done

echo ">> docs refreshed in $DOCS_DIR (source: $(cat "$DOCS_DIR/VERSION"))"
echo ">> previous content retained in $backup"
echo ">> qatlasd keeps running; the next page load serves the new docs"
