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

if [ "$BUILD_LOCAL" -eq 1 ]; then
  echo ">> copying prebuilt Sphinx sites from $REPO_DIR/web/public"
  for site in doc devdoc; do
    if [ ! -f "$REPO_DIR/web/public/$site/index.html" ] && [ ! -f "$REPO_DIR/web/public/$site/dev/index.html" ]; then
      echo "missing $REPO_DIR/web/public/$site; run uv run --locked --script .github/scripts/build_docs.py first" >&2
      exit 1
    fi
  done
  rm -rf "$DOCS_DIR/doc" "$DOCS_DIR/devdoc"
  cp -a "$REPO_DIR/web/public/doc" "$DOCS_DIR/doc"
  cp -a "$REPO_DIR/web/public/devdoc" "$DOCS_DIR/devdoc"
  git -C "$REPO_DIR" rev-parse HEAD > "$DOCS_DIR/VERSION" 2>/dev/null || true
else
  echo ">> pulling $DOCS_IMAGE:$DOCS_REF"
  # Tolerate a pull failure when the image already exists locally
  # (air-gapped deploy hosts, or a locally built stand-in for testing).
  docker pull "$DOCS_IMAGE:$DOCS_REF" || \
    docker image inspect "$DOCS_IMAGE:$DOCS_REF" >/dev/null
  # The scratch image has no CMD and is never STARTED — the dummy
  # command only satisfies `docker create`'s config validation; we
  # docker-cp the content out and discard the container.
  cid="$(docker create "$DOCS_IMAGE:$DOCS_REF" true)"
  trap 'docker rm "$cid" >/dev/null' EXIT
  rm -rf "$DOCS_DIR/doc" "$DOCS_DIR/devdoc"
  # Tar-stream extraction with --no-same-owner: files land owned by the
  # invoking user (docker cp would preserve the image's root ownership
  # and block the next refresh's rm -rf for non-root operators).
  docker cp "$cid:/doc" - | tar -x -C "$DOCS_DIR" --no-same-owner
  docker cp "$cid:/devdoc" - | tar -x -C "$DOCS_DIR" --no-same-owner
  docker cp "$cid:/VERSION" - | tar -x -C "$DOCS_DIR" --no-same-owner 2>/dev/null || true
fi

# qatlasd runs as the distroless nonroot user (UID 65532) and only READS
# the directory, so world-readable is sufficient.
chmod -R a+rX "$DOCS_DIR"

echo ">> docs refreshed in $DOCS_DIR (source: $(cat "$DOCS_DIR/VERSION" 2>/dev/null || echo unknown))"
echo ">> qatlasd keeps running; the next page load serves the new docs"
