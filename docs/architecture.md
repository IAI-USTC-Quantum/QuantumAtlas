# Architecture

## 项目分层

QuantumAtlas 的核心设计不是“把所有东西都塞进一个仓库”，而是明确区分不同层级的 source of truth。

```text
QuantumAtlas app repo      QuantumAtlas-Wiki repo      RAW_DIR/{pdf,markdown,json,images}      Neo4j / 任务记录
应用代码与工具        <->   可审阅知识页面        <->    canonical paper assets            <->   派生查询与运行时层
```

建议这样理解：

- 应用仓库负责代码、模板、CLI、API、测试和脚本。
- `WIKI_DIR` 指向可审阅、可追踪的 Markdown 知识库，生产环境推荐单独放在 `QuantumAtlas-Wiki` 这类普通 Git 仓库里。
- `RAW_DIR` 保存 PDF、解析 Markdown、元数据 JSON 和图片等论文资产，是 canonical paper asset store。
- Neo4j、share 记录、ingest 状态、临时任务属于派生或运行时层，不是长期主数据源。

## 为什么要把 Wiki 和论文资产分开

Wiki 负责回答“这是什么”，图数据库负责回答“它和什么有关”。

这带来几个好处：

- Wiki 页面可以像普通文档一样审阅、修改、回滚。
- 大文件资产不会污染应用仓库和知识仓库。
- 应用代码可以按 release tag 固定，Wiki 内容可以独立高频更新。
- Neo4j 只是查询层，不会反向定义知识边界。

## Wiki 结构

Wiki 不必放在应用仓库里。推荐作为独立 Git 仓库维护，并通过 `QATLAS_WIKI_DIR`（旧名 `WIKI_DIR` 仍作 alias）接入 QuantumAtlas。

```env
QATLAS_WIKI_DIR=../QuantumAtlas-Wiki
```

推荐目录结构：

```text
QuantumAtlas-Wiki/
├── index.md
├── concepts/
├── entities/
│   ├── algorithms/
│   ├── primitives/
│   └── people/
├── sources/
│   └── papers/
└── comparisons/
```

页面是带 YAML frontmatter 的 Markdown 文件，例如：

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

页面之间通过 `[[page-id]]` 互相引用。内置 linter 会检查 frontmatter、断链、孤立页面和部分知识冲突。

## Primitive 的三层表示

与 primitive 相关的内容实际分成三层：

- `atlas/knowledge_graph/primitives/*.yaml`: 程序侧定义源，供 loader、designer 和初始化脚本使用。
- `$WIKI_DIR/entities/primitives/*.md`: 面向知识协作的 Wiki 页面。
- Neo4j 里的 Primitive 节点: 面向查询和关系遍历的图谱层。

这三层的职责不同：

- YAML 更偏“程序定义”。
- Wiki 更偏“知识页面”。
- 图数据库更偏“关系查询”。

新增或修改 primitive 时，应该判断哪几层需要同步更新，而不是只改其中一层。

## Source 页面与 RAW 资产

`$WIKI_DIR/sources/papers/*.md` 是正式知识内容，不是临时缓存。它们应该保存：

- 论文摘要与来源链接。
- 论文相关补充笔记。
- 被其他页面引用的来源页关系。

而 PDF、解析 Markdown、JSON 和图片等大文件，应放到 `RAW_DIR`，不要直接塞进 Wiki 页面目录。

## Share 机制

QuantumAtlas 对外分享原始资源时统一走 `/api/shares` 和 `/share/{token}`。

这意味着：

- 外部调用方拿到的是 share URL，而不是服务器本地路径。
- 公开访问的是 share token，而不是用户身份。
- share 只负责“哪些资源可访问”，不负责“谁是调用者”。

## Client / Server 边界

QuantumAtlas 既可以作为服务端运行，也可以作为远程客户端使用。

- server 模式负责读取本机 `WIKI_DIR`，读写 `RAW_DIR` / `DATA_DIR`，并提供 Wiki 浏览、share、图谱和摄入能力。服务端不会生成或修改 Wiki 页面；如果启用 Wiki 同步接口，它只对 clean checkout 执行 fast-forward 更新。
- client 模式通过 HTTP API 使用这些能力，不要求拿到服务器文件系统权限。

协作时的推荐主边界不是服务器 shell，而是 `QuantumAtlas-Wiki` 仓库本身：

- LLM、脚本、人工编辑都围绕同一个 Wiki Git 仓库工作。
- server 侧的 Wiki checkout 应保持干净，不提供 push API，也不通过 Web UI 直接创建或编辑页面。
- 只有在需要服务器上的搜索结果、页面展示或 Neo4j 同步时，才让 server 去快进自己的 Wiki checkout。
- server 的 Wiki 同步只执行 `git fetch --prune` 和 `git pull --ff-only`；如果本地 checkout 有修改、不是 Git 仓库、不能 fast-forward 或远端不可达，API 会失败并返回对应错误码。
- 如果 server 的 Wiki checkout 不在 `main` 或 `master`，同步状态响应会带 warning，提醒维护者检查部署分支。

应用仓库内不再保留任何 `wiki/`、`raw/`、`data/`、`pb_data/` 目录——所有
状态目录都有内置默认值，落到 git checkout 之外：

```env
# 所有这些都有内置默认；不写就走默认（无需在 .env 里出现）：
# QATLAS_WIKI_DIR    -> <.env 目录>/../QuantumAtlas-Wiki  （兄弟 checkout）
# QATLAS_RAW_DIR     -> ${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/raw
# QATLAS_DATA_DIR    -> ${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/data
# QATLAS_PB_DATA_DIR -> ${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/pb_data
#
# 想搬到挂载盘 / FHS 路径时再显式覆盖：
# QATLAS_RAW_DIR=/mnt/team/QuantumAtlas/raw
# QATLAS_PB_DATA_DIR=/var/lib/quantum-atlas/pb_data
```

> 旧名 `WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` 仍作 alias
> 兼容，新写法推荐 `QATLAS_*` 前缀。从仓库内布局迁移到 XDG 默认的
> 步骤见 [migration-storage-layout.md](migration-storage-layout.md)。

把状态目录放在 git checkout 外的好处：fresh clone 永远干净（不需要维护
长串 `.gitignore` 规则），`go ./...` 不会撞到 FUSE 挂载，并且符合 XDG /
FHS / 12-factor 的常规约定。

## 设计上的取舍

- QuantumAtlas 不把浏览器 OAuth 登录流程内置进应用本体。
- QuantumAtlas 不绑定特定反向代理、SSO 或存储产品。
- `RAW_DIR`、`WIKI_DIR`、`DATA_DIR`、`PB_DATA_DIR` 是显式边界，而不是隐含在仓库结构里的假设。
- 应用代码版本和 Wiki 内容版本可以分离演进。

## 延伸阅读

- [storage-design.md](storage-design.md) — 当 RAW 资产体量上 TB / 引入对象存储（RustFS @ `raw.quantum-atlas.ai`）/ 用 Neo4j 装 paper 引用图时，三层（raw / metadata / graph）怎么分工、怎么对账、怎么重建。
- [migration-storage-layout.md](migration-storage-layout.md) — 把 wiki / raw / data / pb_data 从仓库内搬到 XDG / 挂载点的实操步骤。
- [graph-visualization-research.md](graph-visualization-research.md) — 前端图谱库选型调研（Cytoscape.js / Sigma.js / ...）。
