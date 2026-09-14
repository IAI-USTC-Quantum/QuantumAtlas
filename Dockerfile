# syntax=docker/dockerfile:1.7
# ── stage 1: go binary ─────────────────────────────────────────────────
# Prepare the COMPLETE web/dist (Sphinx public + dev docs, then npm build)
# before docker build. Release CI restores the already-validated UI zip;
# no Node/Sphinx build runs here, and no generated assets are committed.
FROM golang:1.26.2-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Explicit COPY fails early when a caller forgot the prebuilt UI tree.
COPY web/dist/ ./web/dist/
RUN test -s web/dist/index.html \
    && test -s web/dist/doc/index.html \
    && test -s web/dist/devdoc/dev/index.html
ARG VERSION=dev
ENV CGO_ENABLED=0 GOOS=linux
RUN go build \
        -tags=embedui \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/qatlasd \
        ./cmd/qatlasd

# ── stage 2: runtime ───────────────────────────────────────────────────
# distroless/static-debian12:nonroot is the smallest base that satisfies
# our needs:
#   * no shell — minimal attack surface (debug via `docker run --entrypoint`
#     or a sidecar; can't kubectl exec a shell)
#   * `nonroot` variant runs as UID 65532 — host-mounted /data/* paths
#     must be chowned to that UID/GID or the server can't write
#   * ~2 MB base; total image size ≈ 50 MB once the static binary lands
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/qatlasd /qatlasd

# Volume mount points the compose file / kubernetes spec is expected
# to back with persistent storage. `qatlasd` will happily run without
# them (LocalStore dev fallback) but production deployments should
# always provide at least pb_data for PocketBase's SQLite state; raw
# holds PDF / Markdown assets when the S3 backend is not configured.
VOLUME ["/data/raw", "/data/pb_data"]

# 4200 = the in-binary default for `serve --http=`. Configure deployments
# through mounted config.yaml and explicit serve flags, not legacy env vars.
EXPOSE 4200

USER nonroot:nonroot
ENTRYPOINT ["/qatlasd"]
CMD ["serve", "--http=0.0.0.0:4200"]
