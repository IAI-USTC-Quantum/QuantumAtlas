# QuantumAtlas

> A paper collection, multi-paradigm search, and registry database for quantum algorithm research.

[![Go 1.26.2+](https://img.shields.io/badge/go-1.26.2+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev/)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![PocketBase v0.38](https://img.shields.io/badge/PocketBase-v0.38-B8DBE4?style=flat&logo=pocketbase&logoColor=black)](https://pocketbase.io/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16+-336791?style=flat&logo=postgresql&logoColor=white)](https://www.postgresql.org/)
[![Documentation Status](https://app.readthedocs.org/projects/quantum-atlas/badge/?version=latest)](https://quantum-atlas.readthedocs.io/zh-cn/latest/)

> 📚 **Full documentation: <https://quantum-atlas.readthedocs.io>** — installation, architecture, contributing, deployment, and API reference.

QuantumAtlas collects quantum-algorithm papers from arXiv, parses them into structured assets, registers every paper and asset in a PostgreSQL database, and answers queries through a single search endpoint that fans out across multiple paradigms — the local catalog, arXiv, OpenAlex, and (optionally) semantic vector retrieval via Qdrant.

The core idea is simple: **collect once, register everything, search everywhere**. Raw assets (PDF / Markdown / images) live in S3-compatible object storage, the paper registry in PostgreSQL tracks metadata, identities, and asset state, and the search engine queries across all of it without making you pick a paradigm up front.

```text
arXiv / user uploads
    -> Object storage      Immutable raw assets (PDF / MD / images, per-kind buckets)
    -> Postgres registry   Papers, identities, asset state (goose-migrated at boot)
    -> Search engine       POST /api/search: catalog + arxiv + openalex (+ qdrant)
```

## What it does

- Fetch papers from arXiv and parse PDFs into Markdown (MinerU pipeline).
- Register every paper, identity (arXiv ID / DOI), and asset in a PostgreSQL paper registry — queryable with plain SQL.
- Search across paradigms from one endpoint (`POST /api/search`): local catalog, arxiv.org, OpenAlex, and optional Qdrant hybrid vector retrieval (dense+sparse, RRF + rerank).
- Ingest lazily: assets are fetched and converted on demand, with server-side dedupe and LRO-style status polling.
- Mirror the OpenAlex works corpus into Postgres for citation context and batch analysis.
- Collaborate remotely through the Web API, CLI, and share links — no server login required for contributors.

## Installation

QuantumAtlas has two independently maintained components:

- **`qatlasd`** (Go server with PocketBase + SQLite) — precompiled releases embed the complete UI; `go install` automatically fetches and caches the same UI from its exact version's GitHub Release on first start
- **`qatlas`** (Python CLI, [`qatlas-cli` package](https://pypi.org/project/qatlas-cli/)) — the daily-driver client, maintained and released in [IAI-USTC-Quantum/qatlas-cli](https://github.com/IAI-USTC-Quantum/qatlas-cli)

### Install the server (`qatlasd`)

Choose a published tag using the new release format. `vX.Y.Z` below is a placeholder, **not** an existing release; this change does not reissue `v0.34.0`.

```bash
TAG=vX.Y.Z # replace with the selected published release tag
# Download the installer from that SAME tag, review it, then run it.
curl -fL --proto '=https' --proto-redir '=https' \
  "https://raw.githubusercontent.com/IAI-USTC-Quantum/QuantumAtlas/$TAG/cmd/qatlasd/install-qatlasd.sh" \
  -o install-qatlasd.sh
sh install-qatlasd.sh --version "$TAG" # optionally: --dir /opt/qatlas/bin
```

The installer verifies the default GoReleaser tar.gz checksum and executable version before atomic replacement. Supported precompiled platforms: `linux/{amd64,arm64}` + `darwin/arm64`. They include the entire UI and documentation and need **no first-run UI download**. An old running server's `/install-qatlasd.sh` does not understand the new archive format; use the tag-pinned script during migration.

Alternatively, with Go matching `go.mod` (no Node or Sphinx required):

```bash
go install "github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@$TAG"
# Ensure $(go env GOPATH)/bin (or GOBIN) is on PATH.
```

On first `serve`, a source-installed binary downloads `qatlasd_<version>_web.zip` and the checksum list from its **exact** Release, validates them and atomically caches the bundle under the OS user cache directory's `qatlas/ui/v<version>`. Later starts use the verified cache offline; upgrades fetch a separate version. Download, validation and unsupported `dev`/pseudo-version errors stop startup—never silently fall back to `latest`. Both installation methods use the same Web service once resources are ready. See [installation and recovery](docs/server/install.md).

Prepare normal server configuration, then start or explicitly register a service (the installer does neither):

```bash
qatlasd --version
qatlasd config init # ~/.qatlas/config.yaml; refuses to overwrite an existing file
# Edit the YAML for your backing services and credentials.
qatlasd serve
# Or register the background service explicitly:
qatlasd service install --mode user --config "$HOME/.qatlas/config.yaml" --force
```

### Install the client (`qatlas` CLI)

> **Old package retirement:** the final [`quantum-atlas 0.21.0`](https://pypi.org/project/quantum-atlas/0.21.0/)
> has been published as a metadata-only migration notice. It contains no `qatlas`
> module, parser library, console entry point, or runtime dependencies, and does
> **not** automatically install `qatlas-cli`. There will be no further legacy releases.
> This repository is not a Python distribution and does not provide the `qatlas` command.
> For a new installation, choose one command below. Existing `quantum-atlas`
> users must [migrate first](#migrate-from-quantum-atlas).

```bash
# Recommended: uv global tool (isolated env + easy upgrades)
uv tool install qatlas-cli

# or pipx
pipx install qatlas-cli

# or plain pip
pip install qatlas-cli

qatlas --help
```

The `qatlas` CLI points at a remote server via its YAML config (`qatlas config set`). See [docs/client/cli-qatlas.md](docs/client/cli-qatlas.md).

### Migrate from `quantum-atlas`

Choose **only the installer you originally used**, in the same environment:

```bash
# uv tool users
uv tool uninstall quantum-atlas
uv tool install qatlas-cli

# OR pipx users
pipx uninstall quantum-atlas
pipx install qatlas-cli

# OR pip users (activate the original virtual environment first, if used)
pip uninstall quantum-atlas
pip install qatlas-cli
```

If both packages were already installed, uninstalling the old package can remove
shared module or command paths. **Reinstall `qatlas-cli` after uninstalling
`quantum-atlas`**, even if the installer says the new package is already present:

```bash
# Choose the matching installer again
uv tool install --reinstall qatlas-cli
# OR
pipx reinstall qatlas-cli
# OR
pip install --force-reinstall qatlas-cli

qatlas --help
```

**Do not delete `~/.config/qatlas`** (or the corresponding platform config
directory): keep your existing configuration and credentials. The old package's
remaining Python helpers are retired, not a supported library API in this repo.

## Quickstart

### Run the server locally

For a checkout (including unreleased commits), first build the complete UI using the pinned [development instructions](docs/contributing.md#full-ui-build), then:

```bash
CGO_ENABLED=0 go build -tags embedui -o build/qatlasd ./cmd/qatlasd
./build/qatlasd config init
# Edit ~/.qatlas/config.yaml, e.g. postgres.dsn and GitHub OAuth settings.
./build/qatlasd serve --http=127.0.0.1:4200
```

Plain `go build` and `go test` work without any generated frontend files. Local build info may contain a tag, pseudo-version, or `+dirty` marker; `dev` is the fallback when usable version metadata is absent. A binary without a corresponding public Release needs `embedui` to serve UI. Git stores only source; never commit `web/dist`, generated docs, caches or release archives.

Default entry points:

- Home / SPA: `http://localhost:4200`
- PocketBase admin UI: `http://localhost:4200/_/`
- PAT management: `http://localhost:4200/pat` (CLI bearer tokens use PATs, with finer scope/expiry/audit)

Or with Docker — Compose starts qatlasd and optional app profiles; PostgreSQL and object storage remain external:

```bash
cd deploy
cp .env.docker.example .env   # pin image versions only; app config is ~/.qatlas/config.yaml
docker compose up -d
```

Production deployment, systemd install, reverse proxy, and the auth boundary are covered in [docs/server/](docs/server/index.md).

## Common commands

```bash
# Install the client (qatlas CLI) as a global tool — from PyPI (recommended).
# Note: the package moved to `qatlas-cli` (repo IAI-USTC-Quantum/qatlas-cli);
# `quantum-atlas` no longer ships the CLI.
uv tool install qatlas-cli
# or install the qatlas-cli repo checkout in editable mode (for contributors)
# uv tool install /path/to/qatlas-cli --editable --force
qatlas --help

# Paper asset contribution (authenticated PDF upload)
qatlas contrib pdf quant-ph/9508027v1 --pdf paper.pdf

# Parse locally with your own MinerU token, then push to the server
qatlas contrib mineru 2501.00010v1 --push-pdf

# Fetch a paper's PDF / Markdown from the server
qatlas paper get pdf 2501.00010v1 -o paper.pdf
qatlas paper get markdown 2501.00010v1 -o paper.md
```

## Collaboration model

QuantumAtlas leans toward "research infrastructure" rather than a static archive. **Configuration is split in two**:

- **Client (Python `qatlas` CLI)**: YAML-only (`~/.config/qatlas/config.yaml` on Linux, resolved via [`platformdirs`](https://platformdirs.readthedocs.io/)). A template is auto-created on first run; manage it with `qatlas config set`.
- **Server (Go `qatlasd`)**: YAML-only (`~/.qatlas/config.yaml`, env-var configuration is rejected at startup). Initialise with `qatlasd config init`, inspect with `qatlasd config show`; see [config.example.yaml](config.example.yaml) for the full schema.

Content contribution has two parallel paths:

1. Server-side lazy ingestion: when a search hits a paper the registry has never seen (e.g. an unknown arXiv ID), the server mints a `paper_id` and fetches the PDF in the background — no client action needed.
2. Authenticated direct upload (`qatlas contrib pdf` → `POST /api/papers/{arxiv_id}/upload-pdf`), or local MinerU with your own token pushed back to the server (`qatlas contrib mineru`).

Full CLI options, auth details (PAT scopes / bearer tokens), and the recommended collaboration cadence are in [docs/client/contribute-content.md](docs/client/contribute-content.md).

## Documentation map

> Online version (recommended): <https://quantum-atlas.readthedocs.io>. Repository paths below.

- [docs/concepts/architecture.md](docs/concepts/architecture.md): the layered model, sources of truth, and storage boundaries.
- [docs/client/contribute-content.md](docs/client/contribute-content.md): the contribution paths, auth, and sync semantics.
- [docs/server/upload-api.md](docs/server/upload-api.md): `qatlas contrib pdf` / `POST /api/papers/.../upload-pdf` full API reference (sha256 dedup, idempotent retry, in-transit guard, conflict handling).
- [docs/concepts/storage-architecture.md](docs/concepts/storage-architecture.md): how raw assets, metadata, and the registry are split, and why; bucket layout; reconciliation and rebuild.
- [docs/server/rustfs.md](docs/server/rustfs.md): qatlas ↔ RustFS ops guide (env vars, IAM policy, bucket versioning, `qatlasd storage prune`, troubleshooting).
- [docs/server/](docs/server/index.md): local startup, single-host deployment, systemd, environment variables, reverse proxy, and auth examples.
- [docs/contributing.md](docs/contributing.md): dev commands, Conventional Commits, normal server releases, testing conventions, and the legacy package's retirement record.

## Repository overview

```text
QuantumAtlas/
├── cmd/                   Go server entry point
├── internal/              Go server internals (registry, search, ingest, objstore, ...)
├── web/                   React SPA frontend
├── deploy/                docker-compose templates
├── scripts/               bootstrap and maintenance scripts
├── tests/                 test suite
├── docs/                  documentation
├── go.mod / go.sum        Go module, pinned dependencies and tool declarations
└── docsite/               Sphinx user/developer documentation source
```

> State directories (`raw/`, `data/`, `pb_data/`) are **not** in the repo —
> they default to `${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/` or can be
> overridden via YAML `paths.*` to a mounted disk / `/var/lib/...`. See
> [docs/server/migration-storage-layout.md](docs/server/migration-storage-layout.md).

## Current status

The project is in alpha, and recently repositioned around three pillars:

- **Paper collection** — arXiv fetch, MinerU parsing, authenticated uploads.
- **Multi-paradigm search** — one endpoint over catalog / arXiv / OpenAlex / Qdrant providers.
- **Registry database** — PostgreSQL as the central store (paper registry + OpenAlex corpus).

It is best understood as "extensible research infrastructure", not a productized platform.

## Agent application direction

- Users / auth are handled by the Go server's embedded PocketBase (GitHub OAuth + PAT);
  both the CLI and the SPA call write endpoints with `Authorization: Bearer <token>`.
  The QuantumAtlas backend centrally provides the API and build-artifact hosting;
  all page design lives in the Vite + React workbench under `web/`.

## Contributing

Contributions welcome in these areas:

- Improving parsing, ingestion, search providers, and the API.
- Tests, documentation fixes, and collaboration UX.

Please use Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`); they do not automatically bump versions. All official main-repository versions come solely from Git tags: maintainers choose an unpublished SemVer, check/commit/review the candidate source, create an annotated `vX.Y.Z[-rc.N]` tag on the approved SHA, and push only that tag to the existing CI/GoReleaser workflow. No root version file, version-only bump commit, or Go source version edit is required. Use standard `vX.Y.Z[-rc.N]` tags, not PEP 440 or `+build` labels: removing the custom gate does not remove Go module, UI, or Docker tag constraints. Releases publish only server Go/GitHub/Docker artifacts, never PyPI packages. This workflow change does not publish a new release or alter historical `v0.34.0`. CLI development and releases belong in [qatlas-cli](https://github.com/IAI-USTC-Quantum/qatlas-cli). See [docs/contributing.md](docs/contributing.md) for the server release process.

GoReleaser owns GitHub and dual-architecture GHCR publication after reusable checks, with native Git/SemVer validation and default warning-only preflight. There is no custom draft/promotion gate, external smoke prerequisite, or automatic GitHub attestation. Each stable, non-snapshot image publication updates GHCR `latest`; prereleases do not. GitHub/GHCR are not atomic, and reruns are not guaranteed to reject every already-public release or be side-effect-free. For a local snapshot without Docker builds, use `goreleaser release --snapshot --clean --skip=docker` after preparing the UI; real images still need separate validation.

The [final `quantum-atlas 0.21.0` release](https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases/tag/quantum-atlas-v0.21.0) is complete. Its immutable [historical tag](https://github.com/IAI-USTC-Quantum/QuantumAtlas/tree/quantum-atlas-v0.21.0) retains the package metadata, one-time checker/tests, and publishing workflow for audit. They are not an ongoing main-branch workflow. The root Python/Pixi manifests and obsolete wiki batch tools have been removed; Python remains only for documentation and small CI helpers. GoReleaser explicitly ignores that historical Python tag, and no further legacy package versions will be published.

## Acknowledgements

QuantumAtlas builds on the Go, PocketBase, PostgreSQL, React, and arXiv / OpenAlex open ecosystems.

Full open-source credits, inspiration sources, and the maintainer list are in [Credits](https://quantum-atlas.readthedocs.io/zh-cn/latest/about/credits/).

## Data sources & attribution

QuantumAtlas's paper catalog builds on these open scholarly data sources:

- **Paper metadata** (titles / authors / DOIs / citations) from [OpenAlex](https://openalex.org/) and [Crossref](https://www.crossref.org/), both **[CC0 1.0](https://creativecommons.org/publicdomain/zero/1.0/)** public domain.
- **Paper records** from [arXiv](https://arxiv.org/), metadata mirrored per the [arXiv ToU](https://arxiv.org/help/license). **We do not hold or redistribute paper PDF bytes** — the public server exposes no PDF / Markdown download API; users fetch PDFs from arxiv.org themselves.

Full license / attribution / takedown process: [License & Attribution](https://quantum-atlas.readthedocs.io/zh-cn/latest/about/license-and-attribution/); terms of use: [Terms of Service](https://quantum-atlas.readthedocs.io/zh-cn/latest/about/terms-of-service/).

## License

[Apache-2.0 License](LICENSE) (code + documentation)

GitHub: https://github.com/IAI-USTC-Quantum/QuantumAtlas

<p align="center"><i>Collect the papers, register everything, search everywhere.</i></p>
