# syntax=docker/dockerfile:1.7
# ── stage 1: web SPA build ──────────────────────────────────────────────
# Embed-friendly Vite build of web/ → web/dist/. Pinned to Node 20 to
# match the version GitHub Actions release.yml uses, so what runs in CI
# also runs locally.
FROM node:20-alpine AS web
WORKDIR /src/web
# Copy lockfiles first for max layer cache reuse; `npm ci` redownloads
# only when these change, not on every source edit.
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ── stage 2: go binary ─────────────────────────────────────────────────
# CGO_ENABLED=0 + -extldflags=-static so the produced binary works under
# the distroless static base in stage 3 (no libc). Same flags
# .github/workflows/release.yml uses for the linux artifacts.
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Drop in the pre-built SPA so web/embed.go's `//go:embed all:dist`
# finds the asset tree.
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ENV CGO_ENABLED=0 GOOS=linux
RUN go build \
        -trimpath \
        -ldflags="-s -w -extldflags=-static -X main.Version=${VERSION}" \
        -o /out/qatlasd \
        ./cmd/qatlasd

# ── stage 3: runtime ───────────────────────────────────────────────────
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
# always provide at least pb_data for PocketBase + wiki for source-of-
# truth markdown.
VOLUME ["/data/raw", "/data/pb_data", "/data/wiki"]

# 4200 = the in-binary default for `serve --http=`. Override at runtime
# via QATLAS_HTTP_ADDR / QATLAS_SERVER_PORT or the explicit flag.
EXPOSE 4200

USER nonroot:nonroot
ENTRYPOINT ["/qatlasd"]
CMD ["serve", "--http=0.0.0.0:4200"]
