# QuantumAtlas

> A paper collection, multi-paradigm search, and registry database for quantum algorithm research.

[![Go 1.23+](https://img.shields.io/badge/go-1.23+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev/)
[![Python 3.11+](https://img.shields.io/badge/python-3.11+-blue.svg)](https://www.python.org/downloads/)
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

QuantumAtlas ships two deliverables:

- **`qatlasd`** (Go binary, single file ~30 MB, embeds the frontend SPA + PocketBase + SQLite) — the server
- **`qatlas`** (Python CLI, `quantum-atlas` package) — the daily-driver client that talks to the server API

### Install the server (`qatlasd`)

```bash
# One-liner: detects OS/arch, downloads the latest binary to ~/.local/bin,
# verifies SHA256. This step only installs the binary itself.
curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh

# Pin a version / change the install dir
curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh -s -- --version v0.2.5
curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh -s -- --dir /opt/qatlas/bin
```

Supported platforms: `linux/{amd64,arm64}` + `darwin/arm64` (Intel Macs can use the [`go install`](docs/server/install.md) path).

Then register it as a systemd service **manually** (the script deliberately doesn't chain this, so it stays stable on dash / busybox streaming parsers):

```bash
qatlasd service install                    # interactive: asks for mode + .env path
# or fully non-interactive:
qatlasd service install \
    --mode user --dotenv-path ~/QuantumAtlas/.env --force
```

### Install the client (`qatlas` CLI)

> **Install-method change**: since 0.22.0 the `qatlas` CLI lives in its own
> repository, [IAI-USTC-Quantum/qatlas-cli](https://github.com/IAI-USTC-Quantum/qatlas-cli),
> and the PyPI package is renamed from `quantum-atlas` to **`qatlas-cli`**.
> If you previously installed `quantum-atlas`, switch to the commands below
> (the version lineage continues from 0.21.0a3; your CLI config is unaffected).

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

## Quickstart

### Run the server locally

```bash
# One-shot: sync Python + npm deps, build the frontend, build the Go binary
pixi run build

cp .env.example .env
# Edit .env and set QATLAS_POSTGRES_DSN to your PostgreSQL instance
# (team-shared / local / hosted all work). goose migrations apply
# automatically at boot. Optionally tune QATLAS_SEARCH_PROVIDERS
# (default: catalog,arxiv,openalex).
# For GitHub OAuth login, also set GITHUB_CLIENT_ID / GITHUB_CLIENT_SECRET.
./build/qatlasd serve --http=0.0.0.0:4200
```

Default entry points:

- Home / SPA: `http://localhost:4200`
- PocketBase admin UI: `http://localhost:4200/_/`
- PAT management: `http://localhost:4200/pat` (CLI bearer tokens use PATs, with finer scope/expiry/audit)

Or with Docker — the compose file brings up PostgreSQL + qatlasd (object storage stays external, e.g. RustFS on a NAS):

```bash
cd deploy
cp .env.docker.example .env   # fill POSTGRES_PASSWORD + S3 + GitHub OAuth
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
- [docs/contributing.md](docs/contributing.md): dev commands, Conventional Commits, Commitizen release flow, testing conventions.

## Repository overview

```text
QuantumAtlas/
├── qatlas/                Python client (CLI + contrib workflows)
├── cmd/                   Go server entry point
├── internal/              Go server internals (registry, search, ingest, objstore, ...)
├── web/                   React SPA frontend
├── deploy/                docker-compose templates
├── scripts/               bootstrap and maintenance scripts
├── tests/                 test suite
├── docs/                  documentation
└── pyproject.toml         project config
```

> State directories (`raw/`, `data/`, `pb_data/`) are **not** in the repo —
> they default to `${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/` or can be
> overridden via `.env` to a mounted disk / `/var/lib/...`. See
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

Please use Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`). Releases are managed with Commitizen — see [docs/contributing.md](docs/contributing.md).

## Acknowledgements

QuantumAtlas builds on the Go, PocketBase, PostgreSQL, Pydantic, and arXiv / OpenAlex open ecosystems.

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
