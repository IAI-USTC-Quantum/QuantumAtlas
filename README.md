# QuantumAtlas

> 把量子算法论文从"PDF 和笔记"推进到"可查询的知识、可浏览的 Wiki、可同步的图谱，以及可生成的实现代码"。

[![Python 3.11+](https://img.shields.io/badge/python-3.11+-blue.svg)](https://www.python.org/downloads/)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](https://opensource.org/licenses/MIT)
[![Neo4j](https://img.shields.io/badge/Neo4j-5.15+-008CC1?style=flat&logo=neo4j&logoColor=white)](https://neo4j.com/)
[![FastAPI](https://img.shields.io/badge/FastAPI-0.115+-009688?style=flat&logo=fastapi&logoColor=white)](https://fastapi.tiangolo.com/)

---

## 这个项目在做什么

量子算法的知识散落在论文、教科书和各种笔记里。读完一篇论文，你可能知道了 QFT 是什么，但很难回答"哪些算法依赖 QFT""QFT 和 QPE 的关系是什么""从这篇论文到能跑的电路要经过几步"。

QuantumAtlas 试图解决这个问题。它不是论文下载器，也不是量子电路生成器——而是一个面向量子算法研究的**工作台**：

1. **从 arXiv 摄入论文**，下载 PDF、解析为 Markdown、用 LLM 抽取算法结构
2. **生成 Wiki 页面**，用人和 LLM 都能编辑的 Markdown 管理知识
3. **同步到 Neo4j 图数据库**，用图谱回答"它和什么有关"
4. **沿着流水线继续走**：线路设计 → 代码生成 → 验证 → 资源估计

整条链路已经打通，你可以跑一个完整的 demo 看看效果：

```bash
uv run --script examples/demo_pipeline.py --algorithm qft --backend qiskit --save-code
```

这个 demo 不需要 LLM API key 也不需要 Neo4j。它使用脚本内置的算法数据直接构造 `QuantumCircuit` 和 `QuantumIR`，走完"设计 → 生成代码 → 验证 → 资源估计"全流程；这和 `qatlas designer <algorithm_id>` 从 Neo4j 读取算法定义是两条入口。

---

## 核心思路：把“论文资产”和“知识页面”分开

项目架构建立在两个判断上：

> **Wiki 负责回答“这是什么”，图数据库负责回答“它和什么有关”。**
>
> **论文资产和知识页面不是同一种 source of truth。**

QuantumAtlas 现在按四层来理解更准确：

```
QuantumAtlas app repo      QuantumAtlas-Wiki repo        RAW_DIR/{pdf,markdown,json,images}      Neo4j / 任务记录
应用代码与工具        ←→    可审阅知识页面          ←→      canonical paper assets        ←→    派生查询与运行时层
代码、模板、测试            Markdown Wiki 页面               PDF / Markdown / JSON / Images      图谱、share、ingest
```

- 仓库里的 `atlas/`、`tests/`、`scripts/`、`examples/` 是应用层。
- `WIKI_DIR` 指向可审阅、可追踪的知识页面层；生产部署推荐使用单独的普通 Git 仓库，例如 `QuantumAtlas-Wiki`。
- `$RAW_DIR/{pdf,markdown,json,images}` 是 canonical 论文资产层，放原始 PDF 和处理后的大文件资产；开发可设 `RAW_DIR=raw`，生产应指到仓库外。
- Neo4j、分享记录、ingest 记录属于派生/运行时层，不是长期主数据源。

因此，这里的主规则是：

- 论文资产以外部 paper asset store 为准。
- 知识页面以 `WIKI_DIR` 指向的 Wiki 仓库为准；它可以独立于应用代码发版周期频繁更新。
- Neo4j 只做派生查询层，不反向定义知识边界。
- `RAW_DIR` 是唯一的论文资产根配置；仓库内 `raw/` 只是一种本地开发取值。

这个设计仍然继承了 [Karpathy 的 LLM Wiki 思路](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f)，只是把“原始论文资产”和“可编辑知识页面”的物理边界写得更清楚了。

---

## 快速开始

### 最快的方式：只看流水线

```bash
git clone https://github.com/Agony5757/QuantumAtlas.git
cd QuantumAtlas
uv sync                       # 或 pip install .
uv run --script examples/demo_pipeline.py --algorithm grover --backend qiskit
```

这就够了。Bell State、QFT、Grover 三个算法都有预定义数据，不依赖任何外部服务。

### CLI 安装与使用

如果只想把 QuantumAtlas 当作客户端/操作者命令使用，可以用 `uv tool`
安装全局 `qatlas` 入口。全局 tool 只包含 Python 包本身，不携带仓库顶层的
Wiki 内容；需要读写 Wiki 或运行 `parser --wiki` 时，请显式配置
`WIKI_DIR` / `RAW_DIR` 指向你的知识库和资源目录。`WIKI_DIR` 可以是
单独 clone 的 `QuantumAtlas-Wiki` 仓库。

```bash
# 从 Git 仓库安装
uv tool install "git+https://github.com/Agony5757/QuantumAtlas.git"

# 开发本仓库时，可安装为 editable tool
uv tool install . --editable --force

# 查看入口和版本
qatlas --help
qatlas --version
```

常用命令：

```bash
# 电路工具链；designer 的字符串 ID 需要 Neo4j 中已有 algorithm
qatlas designer <kg_algorithm_id> -o circuit_ir.json
qatlas codegen circuit_ir.json --backend qiskit -o output.py
qatlas validator circuit_ir.json --compare-with qft
qatlas estimator circuit_ir.json --format markdown
```

`qatlas` 会转发到现有模块 CLI；不做全局安装时，也可以继续使用
`uv run -m atlas.parser`、`uv run -m atlas.wiki` 这类模块入口。`parser --wiki`、
`wiki ...`、`server`、`service`、`wiki sync`、本地 Wiki 读写等命令仍然需要在有相应
文件系统、配置或 Neo4j 访问权限的机器上运行；纯远程协作场景优先走 HTTP API。

### 完整体验：启动 Web 服务

```bash
# 安装（推荐 uv）
uv sync
# 或者用 pip
pip install -e ".[dev]"

# 启动 Neo4j（可选，图谱功能需要）
docker-compose up -d
export NEO4J_PASSWORD=quantum-atlas
uv run --script scripts/init_primitives.py

# 启动 Web 服务（默认读取仓库根目录 .env；进程环境变量优先）
cp .env.example .env  # 如已存在 .env，可直接编辑现有文件
uv run -m atlas.server
```

也可以安装成 systemd 服务。用户级服务会直接写入
`~/.config/systemd/user/quantum-atlas.service`：

```bash
uv run -m atlas.server.service install --scope user --enable --now
```

如果希望用户级服务在未登录时也能随机器启动，需要额外执行一次：

```bash
loginctl enable-linger "$USER"
```

系统级服务不会直接在程序里执行 sudo，而是先生成 unit 文件，再打印需要人工确认执行的 sudo 命令（**默认包含** `systemctl daemon-reload` 之后的 `enable --now`；若只要 install + reload、不要第三行，可加 `--no-enable --no-now`）：

```bash
uv run -m atlas.server.service install \
  --scope system \
  --run-as "$USER" \
  --output /tmp/quantum-atlas.service
```

安装器默认自动探测运行方式：有 uv 时生成 `uv run uvicorn atlas.server.main:app --host ... --port ...`；没有 uv 时回退到 `.venv/bin/python -m uvicorn ...`，再不行才用当前 Python。`.env` 仍会由 systemd unit 加载，供服务读取 Neo4j、MinerU、API key 等运行配置；但 uvicorn 的 host/port 会在生成 unit 时按当前 `.env` 或 `--host/--port` 固定写入 `ExecStart`。如果需要修改监听地址或端口，请先更新 `.env`，或显式传 `--host/--port`，然后重新生成并安装 service 文件。如果想走封装入口可加 `--app-runner module`，如果确实要固定到某个 Python，可显式加 `--runner python --python /path/to/python`。

然后打开浏览器：

| 页面 | 地址 | 你能做什么 |
|------|------|-----------|
| 首页 | http://localhost:4200 | 看 Wiki 统计，快速操作 |
| Wiki | http://localhost:4200/wiki | 浏览、搜索、编辑页面，查看反向链接 |
| 图谱 | http://localhost:4200/graph | D3.js 力导向图，节点展开 1-3 跳，按类型过滤 |
| API 文档 | http://localhost:4200/api/docs | 交互式 REST API |
| Neo4j | http://localhost:7474 | 直接写 Cypher 查询 |

### 摄入一篇论文试试

CLI 方式（推荐初次体验）。`qatlas ingest` 是 HTTP 客户端：默认读取 `.env` 里的 `PUBLIC_BASE_URL`，也可以用 `--base-url` 临时指定服务器。

```bash
# 下载 + 解析 + 生成 Wiki 页面；不跑服务端 LLM/Neo4j
uv run qatlas ingest quant-ph/9508027 --no-extract --no-sync-neo4j

# 明确请求公网服务，例如 47 服务器或 HTTPS 反代
uv run qatlas ingest quant-ph/9508027 \
  --base-url http://47.102.36.175 \
  --no-extract --no-sync-neo4j

# 只跑到 parse，便于把 Markdown 交给客户端/用户侧 LLM 处理
uv run qatlas ingest quant-ph/9508027v1 \
  --stop-after parse --no-sync-neo4j

# 查看任务状态
uv run qatlas ingest status <task_id>
```

用户侧 LLM 处理并审阅完成后，把算法结果保存成 JSON，再从原任务继续创建/更新 Wiki：

```json
{
  "id": "reviewed_search",
  "name": "Reviewed Quantum Search",
  "description": "Client-reviewed algorithm description.",
  "problem_type": "unstructured_search",
  "primitives": ["prim-qft", "primitive_amplitude_amplification"],
  "complexity": {
    "time": "O(sqrt(N))",
    "space": "O(log N)",
    "gate_count": "O(sqrt(N))",
    "circuit_depth": "O(sqrt(N))",
    "qubit_count": "O(log N)"
  },
  "pseudocode": "prepare superposition\nrepeat amplitude amplification"
}
```

```bash
uv run qatlas ingest continue <task_id> \
  --reviewed-json reviewed.json \
  --reviewed-by alice \
  --no-sync-neo4j
```

如果不依赖已有任务，也可以直接提交审阅后的抽取结果：

```bash
uv run qatlas ingest reviewed quant-ph/9508027v1 \
  --reviewed-json reviewed.json \
  --no-sync-neo4j
```

使用 `https://...` 作为 `--base-url` 时，Python requests 会校验证书链；如果是自签名或中间证书缺失，优先把服务端证书链配置完整，或在客户端系统信任对应 CA。临时连接自签名 HTTPS 服务时，可以显式加 `--insecure` 跳过 TLS 证书校验：

```bash
uv run qatlas ingest quant-ph/9508027 \
  --base-url https://atlas.example \
  --insecure \
  --no-extract --no-sync-neo4j
```

API 方式：

```bash
curl -X POST http://localhost:4200/api/ingest/paper \
  -H "Content-Type: application/json" \
  -d '{"arxiv_id":"quant-ph/9508027","extract":false,"sync_neo4j":false}'
```

摄入流程会依次执行：下载 PDF → 解析为 Markdown → （可选）LLM 提取 → 创建 Wiki 页面 → （可选）同步到 Neo4j。每一步的状态都可以通过 API 查询。
对于早期 arXiv 论文，请使用带分类前缀的旧式 ID，例如 `quant-ph/9508027`，不要只写裸 `9508027`。
注意：`qatlas ingest` 和 `POST /api/ingest/paper` 的默认行为会执行 LLM 提取，需要 `OPENAI_API_KEY` 或 `ANTHROPIC_API_KEY`；初次体验或只想生成论文 Wiki 页面时，请显式加 `--no-extract` 或在 API 请求里传 `"extract": false`。

如果网络不稳定，可以把这条流水线拆开跑。先只下载 PDF 和元数据：

```bash
curl -X POST http://localhost:4200/api/ingest/paper \
  -H "Content-Type: application/json" \
  -d '{
    "arxiv_id":"quant-ph/9508027v1",
    "stop_after":"fetch"
  }'
```

轮询 `GET /api/ingest/{task_id}`，`steps.fetch.progress` 会返回下载阶段、已下载字节数、总字节数和百分比。也可以停在解析阶段，把 Markdown 交给客户端或人工审阅：

```bash
curl -X POST http://localhost:4200/api/ingest/paper \
  -H "Content-Type: application/json" \
  -d '{
    "arxiv_id":"quant-ph/9508027v1",
    "stop_after":"parse"
  }'
```

阶段顺序可以通过 `GET /api/ingest/stages` 获取。`stop_after` 表示从前往后跑到某一步为止；也可以用 `stages:["parse","wiki"]` 精确选择要跑的阶段，服务端会优先复用本地已有的 PDF、JSON、Markdown。

如果 arXiv 元数据或 PDF 下载遇到临时网络错误，服务端会最多尝试 3 次；如果 MinerU 返回处理失败或解析任务异常，服务端会再提交 1 次。超过上限后当前 step 会标记为 `failed`，`progress` 里保留 `attempt`、`max_attempts`、`last_error` 和 `will_retry:false`，后续阶段会跳过，用户可以修复问题后用 `POST /api/ingest/{task_id}/continue` 继续。

默认解析器是本地 PyMuPDF。也可以在 `.env` 配好 MinerU 后使用 MinerU 解析：

```env
PUBLIC_BASE_URL=http://47.102.36.175
SHARE_ACCESS_TOKEN=replace-with-a-long-random-string
MINERU_API_TOKEN=your-mineru-token
MINERU_MODEL_VERSION=vlm
MINERU_IS_OCR=false
```

其中 `PUBLIC_BASE_URL` 是 QuantumAtlas 对外唯一根地址，反向代理应把 `/api`、`/share`、`/health` 等路径转到服务端。MinerU 会拿到 `/share/{token}/...` 形式的带 token URL 拉取本机 PDF。请求时指定：

```bash
curl -X POST http://localhost:4200/api/ingest/paper \
  -H "Content-Type: application/json" \
  -d '{
    "arxiv_id":"quant-ph/9508027v1",
    "parser":"mineru",
    "extract":false,
    "sync_neo4j":false
  }'
```

QuantumAtlas 会把本地 PDF 暴露成 `PUBLIC_BASE_URL/share/{token}/...` 形式的 token 链接提交给 MinerU，默认 `MINERU_IS_OCR=false`。轮询 ingest 任务时，`steps.parse.progress` 会包含 MinerU 的 `state`、`mineru_task_id` 和 `extract_progress`。

客户端也可以替代服务端完成 LLM 抽取。典型流程是：服务端先跑到 `stop_after:"parse"`；客户端通过 `/api/papers/{id}/resources` 拿到 Markdown 分享链接；客户端自己调 LLM、让用户审阅修改；最后从原 ingest 任务继续：

```bash
curl -X POST http://localhost:4200/api/ingest/{task_id}/continue \
  -H "Content-Type: application/json" \
  -d '{
    "reviewed_by":"alice",
    "algorithm":{
      "id":"reviewed_search",
      "name":"Reviewed Quantum Search",
      "description":"Client-reviewed algorithm description.",
      "problem_type":"unstructured_search",
      "primitives":["prim-qft","primitive_amplitude_amplification"],
      "complexity":{
        "time":"O(sqrt(N))",
        "space":"O(log N)",
        "gate_count":"O(sqrt(N))",
        "circuit_depth":"O(sqrt(N))",
        "qubit_count":"O(log N)"
      },
      "pseudocode":"prepare superposition\nrepeat amplitude amplification"
    },
    "sync_neo4j":false
  }'
```

这个接口不会调用服务端 LLM。它会复用本地 `RAW_DIR/json` 元数据，或者使用请求里的 `metadata`，然后创建/更新论文 source page 和算法 entity page；`sync_neo4j=true` 时会继续同步到图数据库。也可以直接调用 `POST /api/ingest/paper/reviewed-extraction`，它不依赖已有 task，但不会记录从哪个 task 继续。

---

## 项目里有什么

### 模块

```
atlas/
├── parser/           从 arXiv 获取论文，PDF 解析为 Markdown
├── extractor/        用 LLM（OpenAI / Anthropic）抽取算法结构信息
├── wiki/             Wiki 引擎：页面 CRUD、搜索、lint、Neo4j 同步
├── server/           FastAPI Web 服务 + REST API + Jinja2 模板
├── knowledge/        Neo4j 客户端和数据模型
├── designer/         从算法定义生成 Quantum IR（中间表示）
├── codegen/          从 Quantum IR 生成 Qiskit 或 QPanda 代码
├── validator/        电路验证：等价性检查、测试套件、参考实现对比
└── estimator/        资源估计：门数、深度、qubit 数、执行时间
```

所有模块都有完整实现和测试覆盖。其中 `extractor` 需要 LLM API key 才能真正工作，`knowledge` 和 Wiki 的图谱同步需要运行中的 Neo4j。

### Wiki 结构

Wiki 内容不必放在应用仓库里。推荐把它作为单独的普通 Git 仓库维护，
并让 QuantumAtlas 通过 `WIKI_DIR` 读取和写入。若应用仓库和 Wiki 仓库是同级目录，可以写成：

```env
WIKI_DIR=../QuantumAtlas-Wiki
```

```
QuantumAtlas-Wiki/
├── index.md                     主目录（自动更新统计）
├── concepts/                    概念定义（量子门、纠缠、纠错…）
├── entities/
│   ├── algorithms/              算法实例
│   ├── primitives/              量子原语（QFT、QPE、振幅放大…）
│   └── people/                  研究者
├── sources/
│   └── papers/                  论文摘要
└── comparisons/                 算法对比
```

每个页面都是一个 Markdown 文件，带 YAML frontmatter：

```yaml
---
id: prim-qft
title: Quantum Fourier Transform
type: entity
category: primitive
tags: [transformation, fourier, fundamental]
status: published
related: [paper-arxiv-9508027]
---
```

页面之间用 `[[page-id]]` 互相引用。内置的 Linter 会检查：缺失的 frontmatter 字段、断裂的 `[[wiki-links]]`、没有入站链接的孤立页面、以及同一算法在不同页面出现的复杂度矛盾。

### Primitive 架构

项目里和 primitive 相关的内容分三层：

- `atlas/knowledge_graph/primitives/*.yaml`：程序侧的 primitive 定义源，供 `PrimitiveLoader`、designer 和 `scripts/init_primitives.py` 使用。
- `$WIKI_DIR/entities/primitives/*.md`：面向 Wiki 的 primitive 页面，使用 `prim-*` 形式的 page id，例如 `prim-qft`。
- Neo4j 中的 Primitive 节点：由脚本或 Wiki 同步生成，沿用 `primitive_*` 形式的实体 id，例如 `primitive_qft`。

也就是说：YAML 更偏“程序定义”，Wiki 更偏“知识页面”，图数据库更偏“查询与关系层”。新增或修改 primitive 时，应同时考虑这三层是否需要保持一致。

### Source 页面维护

`$WIKI_DIR/sources/papers/*.md` 是可追踪的论文来源页，属于 Wiki 的正式内容层，不是临时导入缓存。它们用于：

- 保留论文摘要、来源链接和补充笔记；
- 作为 `[[page-id]]` 被 primitive、algorithm 等页面引用；
- 作为 Wiki/搜索/图谱同步中的 `source` 类型页面维护。

因此，新增论文来源、补充论文笔记或修正论文元数据时，应维护 `$WIKI_DIR/sources/papers/` 中对应页面；原始 PDF、解析 Markdown、元数据 JSON、提取图片等 paper-specific 大文件，应统一放在 `RAW_DIR`（默认 `raw`，可改成外部目录）下，而不是直接塞进这些页面目录。

### Share 访问

对外分享原始资源时统一走 `/api/shares` + `/share/{token}` 机制。`/api/papers/{id}/resources` 返回 share URL，而不是本地文件路径。

TODO:
- 提供一个面向用户代码的 share helper / client 封装，避免调用方手写 `/api/shares` 请求。

### CLI

下面这些命令会直接读写本地文件系统。Wiki 相关命令默认使用 `WIKI_DIR=wiki`
和 `RAW_DIR=raw`；生产或协作环境应显式配置到真实的 `WIKI_DIR` / `RAW_DIR`
目录。通过 `uv tool install` 安装的全局工具不会自带 Wiki 内容。

```bash
# 论文摄入
qatlas parser <arxiv_id> --wiki            # 下载 + 解析 + Wiki + Neo4j
qatlas parser <arxiv_id> --wiki-only        # 跳过 Neo4j 同步
qatlas parser <arxiv_id> --wiki --extract   # 额外用 LLM 提取算法信息

# Wiki 操作
qatlas wiki list [--type concept] [--status published]
qatlas wiki show prim-qft [--raw]
qatlas wiki search "quantum fourier"
qatlas wiki links prim-qft --backlinks      # 查看反向链接
qatlas wiki stats                            # Wiki 统计
qatlas wiki lint -v [--fix]                  # 质量检查
qatlas wiki sync [page_id]                   # 同步到 Neo4j
qatlas wiki ingest <arxiv_id> --no-extract   # 从 Wiki CLI 直接摄入；不跑 LLM
qatlas wiki create <id> --title "..."        # 创建新页面

# 电路设计 → 代码生成 → 验证 → 估计；designer 的字符串 ID 来自 Neo4j
qatlas designer <kg_algorithm_id> -o circuit_ir.json
qatlas codegen circuit_ir.json --backend qiskit -o output.py
qatlas validator circuit_ir.json --reference ref_ir.json --compare-with qft
qatlas estimator circuit_ir.json --format markdown --hardware-params '{"gate_time":50}'

# 端到端 demo（不需要 LLM / Neo4j）
uv run --script examples/demo_pipeline.py --algorithm bell_state --backend qiskit --save-code
```

`qatlas designer <kg_algorithm_id>` 会到 Neo4j 里读取已同步的 algorithm 和 primitive 关系；没有 Neo4j 时会失败。离线演示请使用 `examples/demo_pipeline.py`，它不查图谱，而是使用脚本内置的 Bell State、QFT、Grover 数据。

如果没有安装 `qatlas`，上面的子命令也可以改成对应模块入口，例如
`uv run -m atlas.parser <arxiv_id> --wiki`。

---

## 协作者工作流

这是 QuantumAtlas 和一般"本地知识库"最不一样的地方。

很多协作者不能 SSH 到你的机器，但他们需要：提交论文摄入任务、看到每个阶段进度、拿到论文资源的可访问 URL、生成不需要登录的分享链接。这些都通过 API 完成。

### Client / Server 边界

QuantumAtlas 的代码既可以作为 **server** 跑在有文件系统权限的机器上，也可以作为 **client** 被远程用户或脚本调用。两种模式的边界要分清：

- **server 模式**：负责读写 `WIKI_DIR`、`RAW_DIR`、`DATA_DIR`，也负责 Git 同步、摄入论文资源、创建 share token、连接 Neo4j。
- **client 模式**：只通过 HTTP API 和 server 交互，不直接假设自己能访问服务器上的 `WIKI_DIR`、`RAW_DIR` 或 Git 仓库。

也就是说，远程用户不需要知道 Wiki 是怎么被 Git 追踪的；他们只需要调用 API 查询页面、提交或继续 ingest、获取资源分享链接。Git 是 server 侧的同步和审计机制。

开发时可以继续使用默认目录，也可以直接指向单独 clone 的 Wiki 仓库：

```env
WIKI_DIR=../QuantumAtlas-Wiki
RAW_DIR=raw
DATA_DIR=data
```

生产部署更推荐把这些运行时目录移到应用仓库外面，例如：

```env
WIKI_DIR=/srv/quantumatlas-wiki
RAW_DIR=/srv/quantumatlas-raw
DATA_DIR=/srv/quantumatlas-data
```

这里的 `/srv/quantumatlas-wiki` 可以是一份普通 Git checkout，例如 `QuantumAtlas-Wiki` 仓库，不需要 submodule。这样 API 触发 Wiki 更新时，只会影响知识库内容，不会把应用代码也一起更新；应用升级和 Wiki 内容同步也可以分开管理。生产可以把应用仓库固定在 release tag，同时让 Wiki 仓库停留在自己的 `main` 分支并频繁更新。

服务端状态接口 `GET /api/server/info` 只暴露安全摘要，例如版本、Wiki 是否外置、Git 分支/commit、`RAW_DIR` 是否公开等信息；不会把服务器绝对路径作为普通 API 返回给公网用户。

**摄入论文资源**

独立下载 API 已取消。论文 PDF、Markdown、元数据都通过 `POST /api/ingest/paper` 的 `fetch` / `parse` 阶段产生，并通过 `GET /api/ingest/{task_id}` 查看进度。需要停在某一步时使用 `stop_after`；需要接着旧任务往后跑时使用 `POST /api/ingest/{task_id}/continue`。

**创建分享链接**

```bash
curl -X POST http://localhost:4200/api/shares \
  -H "Content-Type: application/json" \
  -d '{"paths":["papers/pdf"],"label":"paper pdfs","expires_in":604800}'
```

返回一个 token，任何人用 `/share/{token}` 就能访问，不需要登录。过期自动失效。

**异步摄入论文**

```bash
curl -X POST http://localhost:4200/api/ingest/paper \
  -H "Content-Type: application/json" \
  -d '{"arxiv_id":"quant-ph/9508027","extract":false}'
# 返回 task_id，轮询 /api/ingest/{task_id} 查看每一步的状态
```

### 设计上的刻意取舍

- **不内置鉴权，也不把鉴权策略放在 QuantumAtlas 层设计**。公网部署时，请务必在反向代理、SSO、API gateway 或内网访问控制里保护管理型 API。
- **不绑定特定反向代理或存储产品**。`RAW_DIR` 可以指向本地目录、NAS 或任何其他服务端可读写的位置。
- **`USER_HEADER`** 是 server 侧审计日志配置：默认读取上游反向代理、SSO 或 API gateway 常用的 `X-Forwarded-User` 请求头。client 不需要、也不应该手动填写这个请求头；它不是鉴权依据，也不会拒绝请求。

单机部署需要设的环境变量（全部有默认值，按需覆盖）：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `WIKI_DIR` | `wiki` | Wiki 知识库目录；生产推荐指向单独的 `QuantumAtlas-Wiki` Git checkout |
| `RAW_DIR` | `raw` | canonical 论文资产根目录；生产部署建议显式设为外部绝对路径 |
| `DATA_DIR` | `data` | 任务、share、ingest 的 JSON 存储 |
| `DEFAULT_SHARE_EXPIRES_IN` | `600` | 自动创建分享链接时的默认过期秒数 |
| `PUBLIC_BASE_URL` | 空 | QuantumAtlas 对外唯一根地址；client、share 链接和 MinerU URL 都基于它 |
| `SHARE_ACCESS_TOKEN` | 空 | 可选的内置不过期 share token；用于 `/share/{token}/papers/...` |
| `USER_HEADER` | `X-Forwarded-User` | Server 从这个请求头读取上游注入的用户标识，仅用于审计日志 |
| `QUANTUMATLAS_REQUIRE_RELEASE_TAG` | `false` | 生产保护开关；设为 `true` 时要求当前 checkout 正好在 Commitizen 发版对应的 `v*.*.*` tag 上 |
| `NEO4J_URI` | `bolt://localhost:7687` | 服务端连接 Neo4j 的 Bolt 地址 |
| `NEO4J_USER` | `neo4j` | Neo4j 用户名 |
| `NEO4J_PASSWORD` | 空 | Neo4j 密码 |
| `OPENAI_API_KEY` | 空 | OpenAI API key（LLM 提取需要） |
| `ANTHROPIC_API_KEY` | 空 | Anthropic API key（LLM 提取的备选） |
| `MINERU_API_TOKEN` | 空 | MinerU 精准解析 API token；`parser:"mineru"` 时需要 |
| `MINERU_API_BASE_URL` | `https://mineru.net` | MinerU API 根地址 |
| `MINERU_MODEL_VERSION` | `vlm` | MinerU 精准解析模型；官网当前推荐 `vlm`，也可设为 `pipeline` 或 `MinerU-HTML` |
| `MINERU_LANGUAGE` | `ch` | MinerU 文档语言参数 |
| `MINERU_IS_OCR` | `false` | 是否让 MinerU 开启 OCR；默认关闭 |
| `MINERU_ENABLE_FORMULA` | `true` | 是否让 MinerU 开启公式识别 |
| `MINERU_ENABLE_TABLE` | `true` | 是否让 MinerU 开启表格识别 |
| `MINERU_POLL_INTERVAL` | `3` | 轮询 MinerU 解析任务的间隔秒数 |
| `MINERU_TIMEOUT` | `1800` | 单个 MinerU 解析任务的最长等待秒数 |
| `SERVER_HOST` | `127.0.0.1` | 服务监听地址 |
| `SERVER_PORT` | `4200` | 服务监听端口 |

对外访问统一走 `PUBLIC_BASE_URL`。论文 PDF / Markdown / JSON / 图片通过 `/share/{token}/...` 暴露；如果没有配置 `SHARE_ACCESS_TOKEN`，服务端会按需创建有过期时间的 share token。

公网单机部署时，通常只需要先设置：

```env
SERVER_HOST=0.0.0.0
SERVER_PORT=4200
PUBLIC_BASE_URL=http://47.102.36.175
SHARE_ACCESS_TOKEN=replace-with-a-long-random-string
```

反向代理需要把 `PUBLIC_BASE_URL` 下的 `/api`、`/share`、`/health` 等路径转到 QuantumAtlas。Neo4j 推荐只给后端服务使用，例如 `NEO4J_URI=bolt://127.0.0.1:7687`。

默认值示例也同步写在仓库根目录的 `.env.example`，其内容应与 `atlas/server/config.py` 中 `ServerConfig` 的真实默认值保持一致；配置解析由 `pydantic-settings` 负责读取 `.env`、进程环境变量和类型转换。

**版本号**：只由 **Commitizen** 在发版时写入 `pyproject.toml`（不要手改 `version`）；`qatlas --version`、`/health`、OpenAPI 等都从同一处读。服务启动时会在 `RAW_DIR`、`DATA_DIR` 各写一份 `.quantumatlas-code-version.json`，方便对照 raw/data 与当时跑的代码。发版流程见 **[开发 → 版本与 GitHub Release](#版本与-github-release)**。

---

## API 概览

```
Wiki & 知识
  GET  /health                     健康检查
  GET  /api/server/info            Server 配置安全摘要
  GET  /api/pages                  列出页面
  GET  /api/pages/{page_id}        获取单个页面
  GET  /api/search?q=...           全文搜索
  GET  /api/stats                  Wiki 统计
  GET  /api/lint                   质量检查
  GET  /api/wiki/sync/status       Wiki Git 同步状态
  POST /api/wiki/sync/pull         拉取并快进 Wiki 仓库

图谱
  GET  /api/graph/stats            Neo4j 统计
  GET  /api/graph/schema           图结构
  POST /api/graph/query            Cypher 查询

摄入
  GET  /api/ingest/stages          摄入阶段说明和顺序
  POST /api/ingest/paper           异步摄入论文；支持 stop_after/stages，可选 parser=mineru
  POST /api/ingest/{task_id}/continue
                                   复用旧任务产物继续后续阶段
  POST /api/ingest/paper/reviewed-extraction
                                   提交客户端 LLM + 人工审阅后的抽取结果并生成 Wiki/图谱
  GET  /api/ingest/{task_id}       查看摄入状态、步骤消息、下载进度、解析进度
  GET  /api/ingests                摄入任务列表

协作
  GET  /api/papers/{id}/resources  查询论文本地资源
  POST /api/shares                 创建分享链接
  GET  /api/shares                 分享列表
  DELETE /api/shares/{token}       撤销分享
  GET  /share/{token}              访问分享内容
  GET  /share/{token}/{path}       按路径访问分享文件
```

完整交互式文档：`http://localhost:4200/api/docs`

---

## 开发

```bash
# 安装开发依赖
uv sync --extra dev

# 测试
uv run pytest                     # 全部
uv run pytest tests/wiki -v       # Wiki 模块
uv run pytest tests/server -v     # Web 服务
uv run pytest -m integration      # 集成测试

# 代码质量
uv run black atlas tests
uv run isort atlas tests
uv run ruff check atlas tests --select E9,F63,F7,F82
uv run mypy atlas
```

### 仓库结构

```
QuantumAtlas/
├── atlas/                 核心代码（10 个模块）
├── raw/                   兼容期 / 开发用原始资料（非 canonical paper store）
├── wiki/                  本地开发默认 Wiki；生产推荐改用外置 QuantumAtlas-Wiki 仓库
├── data/                  本地运行时状态（任务、分享、ingest 记录）
├── tests/                 测试套件
├── scripts/               初始化和检查脚本
├── examples/              demo_pipeline.py
├── QUANTUM_ATLAS.md       Wiki 页面编写规范
├── docker-compose.yml     Neo4j 开发环境
└── pyproject.toml         项目配置
```

### 版本与 GitHub Release

日常提交用 **Conventional Commits**（`feat:`、`fix:` 等），也可带 **scope**，如 **`fix(server):`**、**`feat(api):`**。发版交给 **Commitizen** 统一改版本号、写 changelog、打 tag，不要手改 `pyproject.toml` 里的 `version`。

`[tool.commitizen]` 与 [QuantumAlgorithm (`qalgo`)](https://github.com/TMYTiMidlY/QuantumAlgorithm) 对齐：`cz_conventional_commits`、`pep621` + `pep440`、`tag_format = "v$version"`。正式发版走两段式 GitHub Actions，避免 release bot 直推受保护的 `main`：

1. 手动触发 **Open version bump PR**，由 bot 运行 Commitizen，只修改 `pyproject.toml` 和 `CHANGELOG.md` 并打开 release PR。
2. maintainer 审核并合并该 PR 到 `main`。
3. **Tag and publish release** 在 `main` HEAD 读取版本号，创建缺失的 `v*.*.*` tag，构建 wheel/sdist 并创建 GitHub Release（**暂未**接 PyPI）。

如果配置了分支保护，建议把 `RELEASE_BOT_TOKEN` 设为 GitHub App token 或 fine-grained PAT，用于创建 bump PR；tag 和 release 仍由合并后的 `main` workflow 统一完成。

本地维护者如需预演版本变化，可以运行 `uv run --with commitizen cz bump --dry-run`；不要在本地手动推 release tag。

生产若要与发版 tag 对齐，设 `QUANTUMATLAS_REQUIRE_RELEASE_TAG=true`。Wiki 内容仍可独立于应用 tag 更新。

---

## 当前状态

项目处于 alpha 阶段，但主线骨架完整。10 个模块全部实现，400+ 项测试通过。

| 阶段 | 目标 | 状态 |
|------|------|------|
| Phase 1 | MVP 验证：端到端链路打通 | ✅ 完成 |
| Phase 2 | 规模化：覆盖 50+ 算法 | 🚧 进行中 |
| Phase 3 | 生态化：社区贡献与多后端 | 📋 规划中 |

目前有 7 个原语定义（QFT、QPE、振幅放大、Block Encoding、哈密顿量模拟、量子行走、变分电路），15+ 个 API 端点，Web 界面和协作者工作流都已可用。

更适合把它看作一个**可继续扩展的研究基础设施**，而不是一个已经产品化的平台。接下来的方向是覆盖更多算法、完善 Wiki 内容、以及让 LLM 提取的质量更稳定。

## 贡献

欢迎以下类型的贡献：

- 新增 primitive、algorithm、paper 页面
- 完善 Wiki 内容和 frontmatter 规范
- 改进解析、提取、图谱同步和 API
- 补充测试、修复文档

提交说明请使用 **Conventional Commits**，与 Commitizen 一致，例如：`feat:`、`fix:`、`docs:`、`refactor:`、`test:`、`chore:`；需要写清范围时用 **`fix(server):`**、**`feat(api):`** 等形式。

**维护者**：发版只走 **`cz bump`**（说明见 **[开发 → 版本与 GitHub Release](#版本与-github-release)**），不要手改版本号。

## 致谢

- [Neo4j](https://neo4j.com/) — 图数据库
- [FastAPI](https://fastapi.tiangolo.com/) — Web 框架
- [Pydantic](https://docs.pydantic.dev/) — 数据验证
- [Qiskit](https://qiskit.org/) — 量子计算 SDK
- [Karpathy's LLM Wiki](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f) — 架构灵感

## 许可证

[MIT License](LICENSE)

---

GitHub: https://github.com/Agony5757/QuantumAtlas

<p align="center"><i>构建量子算法的活文档，让知识持续增值。</i></p>
