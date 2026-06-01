# 致谢

## 灵感来源

QuantumAtlas 最初的**三层知识库设计**受 [Karpathy's LLM Wiki](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f) 启发——把 LLM 知识库切成可读 + 可结构化的层。

**fine-grained 权限 / scope 模型**抄了 [GitHub fine-grained PAT](https://github.blog/2022-10-18-introducing-fine-grained-personal-access-tokens-for-github/)：

- 强制过期
- 显式 scope opt-in
- "PAT 不能管理 PAT" 设计

## 上游数据源（学术 metadata）

QuantumAtlas 的论文 catalog / 引用图 / DOI 解析依赖以下开放学术数据源——
license 与归属义务详见 [License & Attribution](license-and-attribution.md)。

- [**OpenAlex**](https://openalex.org/)（OurResearch 非营利）— 论文 metadata
  主源，含反向引用 `cited_by` 列表 / topics / 机构 ROR / 摘要倒排索引；license
  [CC0 1.0](https://creativecommons.org/publicdomain/zero/1.0/)。我们镜像
  bulk dump 到 `qatlas-openalex` 桶做离线 join + 增量同步。
- [**Crossref**](https://www.crossref.org/) — DOI 注册中心，metadata 自 2017
  起 [CC0 1.0](https://creativecommons.org/publicdomain/zero/1.0/)。负责正向
  reference 列表（含 unstructured 引文文本，比 OpenAlex 更全）。
- [**arXiv**](https://arxiv.org/) — preprint 源头。我们通过 OAI-PMH 镜像 metadata
  ([arXiv ToU](https://arxiv.org/help/license))；**PDF 字节不镜像、不再分发**，
  share 接口 `307` 跳 arxiv 原 URL。

OpenAlex / Crossref 是 CC0 ⇒ 无强制归属，但作为非营利基础设施仍需要可见归属帮它
们持续争取资助。本项目除本节外，在 SPA 全局页脚、`/api/*` 响应 `X-Attribution`
header、以及 [README.md](https://github.com/IAI-USTC-Quantum/QuantumAtlas#数据来源与归属attribution)
均显式声明。

## 关键依赖（开源生态）

### Server

- [Go](https://go.dev/) 1.23+ — 主语言
- [PocketBase](https://pocketbase.io/) v0.38 — 内嵌 BaaS (SQLite + Auth + Realtime + Admin UI)
- [Neo4j Go driver](https://github.com/neo4j/neo4j-go-driver) v5 — 图数据库 client
- [minio-go](https://github.com/minio/minio-go) v7 — S3 兼容存储 client
- [casbin/v2](https://github.com/casbin/casbin) — PAT scope enforcer
- [godotenv](https://github.com/joho/godotenv) — .env 加载
- [kardianos/service](https://github.com/kardianos/service) — 跨平台 systemd / launchd / SCM 安装
- [DuckDB](https://duckdb.org/) — paperindex 索引引擎
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) — 纯 Go SQLite（CGO-free）

### Client

- [Python](https://www.python.org/) 3.11+
- [Pydantic](https://docs.pydantic.dev/) v2 + pydantic-settings — 数据模型 / 配置
- [Requests](https://requests.readthedocs.io/) — HTTP client
- [PyYAML](https://pyyaml.org/) — Wiki frontmatter
- [Qiskit](https://qiskit.org/) — 量子电路（codegen / validator）
- [QPanda](https://github.com/OriginQ/QPanda-2) — 同上
- [MinerU](https://mineru.net/) — PDF → Markdown 解析

### Frontend

- [React](https://react.dev/) 19
- [Vite](https://vitejs.dev/) — build tool
- [TanStack Router](https://tanstack.com/router/) — 路由
- [Tailwind CSS](https://tailwindcss.com/) v4
- [shadcn/ui](https://ui.shadcn.com/) — 组件库
- [PocketBase JS SDK](https://github.com/pocketbase/js-sdk) — 浏览器登录 / realtime

### Tooling

- [uv](https://github.com/astral-sh/uv) / [pixi](https://pixi.sh/) — 包管理
- [Commitizen](https://commitizen-tools.github.io/commitizen/) — 版本管理 / Conventional Commits
- [pytest](https://docs.pytest.org/) + [Go testing](https://pkg.go.dev/testing) — 测试
- [mkdocs](https://www.mkdocs.org/) + [mkdocs-material](https://squidfunk.github.io/mkdocs-material/) — 这份文档

### 基础设施

- [Caddy](https://caddyserver.com/) — 反向代理 / TLS 终结
- [RustFS](https://rustfs.com/) — 对象存储后端
- [EasyTier](https://easytier.cn/) — 跨地域 mesh networking
- [Read the Docs](https://readthedocs.org/) — 文档托管
- [GitHub Actions](https://github.com/features/actions) — CI / release pipeline
- [PyPI](https://pypi.org/) — Python 包分发

## 学术参考

QuantumAtlas 的 Wiki 模板和 schema 受这几篇影响：

- Karpathy 的 *LLM Knowledge Base* gist（前面已引用）
- Material for MkDocs 的 *Reference architecture*
- arXiv 的 [bulk data layout](https://info.arxiv.org/help/bulk_data.html)（我们抄了它的 YYMM 前缀目录约定）

## 维护者

主要协作者（按提交量、按字母序）：

- [@Agony5757](https://github.com/Agony5757) (USTC)
- [@TMYTiMidlY](https://github.com/TMYTiMidlY) (USTC)
- [@YunJ1e](https://github.com/YunJ1e) (USTC)
- [@gausshj](https://github.com/gausshj) (USTC)
- [@qsxustc](https://github.com/qsxustc)
- [@yowakkojay](https://github.com/yowakkojay)

完整 contributor 列表见 [GitHub graphs](https://github.com/IAI-USTC-Quantum/QuantumAtlas/graphs/contributors)。

## 想被加进 contributors？

提 PR + 合并即可——GitHub 自动算。也欢迎在 issue 里指出"这里不对/这里没说清楚"。

## 许可证

- **代码**：[Apache-2.0 License](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/LICENSE)
- **文档**：Apache-2.0（同 repo）
- **Wiki 内容**：[QuantumAtlas-Wiki repo](https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki) 自己的 LICENSE（同样 Apache-2.0）
- **第三方依赖**：各自原 license（看 `go.sum` / `pyproject.toml` / `package.json`）

---

<p align="center"><i>构建量子算法的活文档，让知识持续增值。</i></p>
