# 入门

## 安装

QuantumAtlas 分两个交付物：

- **`qatlas-server`**（Go 二进制，单文件 ~30 MB，自带前端 SPA + PocketBase + SQLite）—— 部署服务端用
- **`qatlas`**（Python CLI，`quantum-atlas` 包）—— 日常用户调用 server API 用

### 装 server (`qatlas-server`)

```bash
# 一行装：自动检测 OS/arch、下载最新 binary 到 ~/.local/bin、
# 在 TTY 下问你要不要一并注册成 systemd 服务
curl -fsSL https://quantum-atlas.ai/install-server.sh | sh

# CI / agent 全自动模式（不问任何问题，装完直接 systemd enable）
curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- \
    --yes --service --mode user --dotenv-path ~/QuantumAtlas/.env

# 只装 binary，跳过 service install
curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- --no-service
```

支持 `linux/{amd64,arm64}` + `darwin/arm64` 三个平台（Intel Mac 用 [`go install`](deployment.md#b-go-install) 路径）。脚本会自动校验 SHA256。

### 装 client (`qatlas` CLI)

```bash
# 推荐：uv 全局工具（隔离环境 + 升级方便）
uv tool install quantum-atlas

# 或 pipx
pipx install quantum-atlas

# 或 pip 直接装
pip install quantum-atlas

qatlas --help
```

`qatlas` CLI 默认通过 `QATLAS_SERVER_URL` 指向远端 server。配置详见仓库根目录的 [`.env.example`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.env.example)。

## 快速开始

### 1. 先跑一个不依赖外部服务的 demo

```bash
git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas.git
cd QuantumAtlas
uv sync
uv run --script examples/demo_pipeline.py --algorithm qft --backend qiskit --save-code
```

这个 demo 不需要 LLM API key，也不需要 Neo4j。它会直接走完“设计 -> 生成代码 -> 验证 -> 资源估计”的主流程。

### 2. 本地启动 Web 服务

```bash
# 一次性同步 Python + npm 依赖 + 前端 build + Go build
pixi run build

cp .env.example .env
# 编辑 .env，填上 NEO4J_URI / NEO4J_USERNAME / NEO4J_PASSWORD 指向
# 你自己准备的 Neo4j 实例（团队共享 / 自起 / 托管均可）。
# 如需 GitHub OAuth 登录，再填 GITHUB_CLIENT_ID / GITHUB_CLIENT_SECRET。
./build/qatlas-server serve --http=0.0.0.0:4200
```

默认入口：

- 首页 / SPA：`http://localhost:4200`
- PocketBase admin UI：`http://localhost:4200/_/`
- PAT 管理页：`http://localhost:4200/pat`（CLI bearer 走 PAT，更细的 scope/过期/审计）

生产部署、systemd 安装、反向代理和鉴权边界请看 [Deployment](deployment.md)。

> `atlas/server/` 下还保留着旧的 FastAPI 入口（`uv run -m atlas.server`），
> 已不是生产路径，仅作为本地兼容性测试用，命令见 [Python legacy server](python-legacy.md)。

## 常用命令

```bash
# 作为全局工具安装 client（qatlas CLI）— 从 PyPI（推荐）
uv tool install quantum-atlas
# 或开发模式装本地 checkout（贡献时用）
# uv tool install . --editable --force
qatlas --help

# 论文摄入（服务器侧抓取 + 解析）
qatlas ingest quant-ph/9508027 --no-extract --no-sync-neo4j

# 论文资产贡献（鉴权用户上传）
qatlas upload pdf quant-ph/9508027v1 --pdf paper.pdf --metadata meta.json
qatlas upload markdown 2501.00010v1 --markdown out.md --source mineru

# 本地用自己的 MinerU token 解析后推给服务器
qatlas mineru 2501.00010v1 --push-pdf

# 电路工具链
qatlas designer <kg_algorithm_id> -o circuit_ir.json
qatlas codegen circuit_ir.json --backend qiskit -o output.py
qatlas validator circuit_ir.json --compare-with qft
qatlas estimator circuit_ir.json --format markdown
```

如果不做全局安装，也可以继续使用 `uv run -m atlas.parser`、`uv run -m atlas.wiki`、
`uv run -m atlas.cli` 这些模块入口。

## 协作模型

QuantumAtlas 偏向「研究基础设施」而不是静态资料库。所有配置由 [pydantic-settings](https://docs.pydantic.dev/latest/concepts/pydantic_settings/)（Python client）和 godotenv（Go server）自动从仓库根目录的 `.env` 加载；项目自有字段统一用 `QATLAS_` 前缀（旧名作 alias 保留）。多人协作时通常什么都不用写——所有存储路径都有默认：`QATLAS_WIKI_DIR` 默认是兄弟 checkout `../QuantumAtlas-Wiki`；`QATLAS_RAW_DIR` / `QATLAS_DATA_DIR` / `QATLAS_PB_DATA_DIR` 默认走 `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/{raw,data,pb_data}`，跟 git checkout 完全分离。需要存到挂载盘或 `/var/lib/` 时再显式覆盖。

内容贡献分两条并列路径：

- **Raw 资产**走 `QATLAS_RAW_DIR`（默认 `$XDG_DATA_HOME/quantum-atlas/raw`），按 YYMM 分片存储（如 `<raw_dir>/pdf/9508/9508027v1.pdf`、`<raw_dir>/pdf/2501/2501.00010v1.pdf`）。三种方式都会落到同一布局：
    1. 服务器侧按 arXiv ID 抓取（`qatlas ingest`）。
    2. 鉴权用户直接上传 PDF / Markdown（`qatlas upload pdf|markdown`，对应 `POST /api/papers/{arxiv_id}/upload-*`）。
    3. 本地用自己的 `MINERU_API_TOKEN` 跑 MinerU 后推回云端（`qatlas mineru`）。
- **Wiki 内容**走独立 Git 仓库（推荐作为应用仓库的兄弟目录 checkout），任何人都可以 clone / commit / PR；服务器侧的 Wiki checkout 只接受 fast-forward 拉取，通过 `POST /api/wiki/sync/pull` 触发，无需 SSH 上服务器。

完整 CLI 选项、鉴权说明（`QATLAS_USER_HEADER` / bearer token）、ff-only 同步语义和推荐协作节奏见 [Contribution workflow](contribution-workflow.md)。
