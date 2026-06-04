# License & Attribution

> QuantumAtlas 自己的代码 / 文档 / Wiki 内容 license 见 [致谢 · 许可证](credits.md#许可证)
> 段。本文档讲**外部上游数据源**的 license 归属和我们的合规策略。

## 我们怎么处理"PDF + metadata + 全文"三类数据

| 数据类别 | 来源 | 我们持有 / 分发？ | 用户拿到什么 |
|---|---|---|---|
| **论文 PDF 字节** | arxiv.org（作者保留版权） | ⚠️ **默认不分发** | qatlasd **默认无 PDF 下载 API**（`QATLAS_ASSET_DOWNLOADS_ENABLED=false`）；从 [arxiv.org](https://arxiv.org/) 自行下载。Self-hosted 部署可在受控范围内启用对内下载，详见下文「资产下载开关」 |
| **论文 metadata**（标题 / 作者 / DOI / 引用 / 发表日期 等） | OpenAlex（CC0）+ Crossref（CC0） | ✅ 镜像 + Neo4j MERGE | 公开 API 返回，CC0 transitively 公开 |
| **MinerU 解析后的 Markdown 全文** | 由部署方用自己的 MinerU quota 从 PDF 转换 | ✅ 缓存在 `qatlas-md` 桶 | 同上开关控制：默认仅供 server 内部检索；启用后可对持 `papers:read` 的客户端 serve markdown 字节 |
| **Wiki 知识页面**（概念 / 算法 / paper 笔记） | 团队 + 贡献者撰写 | ✅ 在独立 [QuantumAtlas-Wiki repo](https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki) | require login（同上） |

**核心合规设计**：

```
默认不分发 PDF 字节 + metadata 来自 CC0 上游 + Markdown 默认不通过 API 外发
  → 论文 license 风险在 quantum-atlas.ai 这类公开实例上天然规避
  → self-hosted 部署若开启资产下载开关，由部署方自行承担分发义务
```

这意味着 [quantum-atlas.ai](https://quantum-atlas.ai) 这类公开实例**不需要**在
ingest 时检查每篇论文的 license（许多 arxiv 论文是"作者保留版权 + 授予 arxiv
永久非排他分发权"，license 字段在 OpenAlex 里**不**统一记录）。靠"默认不持
字节面向公网 + 默认不外发全文"两条规则在源头规避，比 per-paper license
匹配可靠得多。

> **Self-hosters 注意**：若你把 `QATLAS_ASSET_DOWNLOADS_ENABLED` 设为
> `true`（见 [资产下载开关](#资产下载开关-self-hosted)），server 会开始通过
> HTTP API 对外 serve PDF / Markdown 字节，原生的"源头规避"就**不再适用**——
> 此时部署方需要自己评估对外受众范围、上游 ToS 与适用法域的合规要求。
> 我们维护的公开实例（quantum-atlas.ai）保持默认关闭。

## 上游数据源 license 汇总

各数据源的详细介绍见 [外部数据源](external-data-sources.md)。下表只列 license
和我们的归属义务。

| 数据源 | License | 商业可用 | 强制归属 | 备注 |
|---|---|---|---|---|
| **OpenAlex** | [CC0 1.0 Universal](https://creativecommons.org/publicdomain/zero/1.0/) | ✅ | ❌（强烈推荐但不强制） | 创作者放弃所有权利，可任意使用。OpenAlex 仍**请求**显示归属，我们照做 |
| **Crossref metadata** | [CC0 1.0](https://creativecommons.org/publicdomain/zero/1.0/) | ✅ | ❌ | 2017 起 Crossref 全部 metadata 转 CC0 |
| **arXiv metadata**（via OAI-PMH） | [arXiv ToU](https://arxiv.org/help/license) | ✅（read access） | ✅ "Source: arXiv" | 元数据可读、可镜像；归属到 arxiv.org |
| **arXiv 论文全文（PDF）** | **作者保留版权** | ❌ 不可随意再分发 | — | 公开实例**不**镜像 PDF 字节；用户从 arxiv 自己下。Self-hosted + 开关启用后，由部署方承担分发义务 |
| **MinerU 解析输出** | 衍生作品 — 受**原 PDF 版权**约束 | ⚠️ 仅 fair-use / 研究教育 | — | 同上：公开实例无字节下载 API；self-hosted 自负责任 |
| **Semantic Scholar Open Research Corpus** | [ODC-BY 1.0](https://opendatacommons.org/licenses/by/1-0/) | ✅ | ✅ "Data provided by Semantic Scholar" | 当前未使用，预留 |
| **ORCID public profile** | CC0 | ✅ | ❌ | 当前未使用，预留 |

## 我们的归属（按 CC0 推荐做法）

**SPA 详情页脚 + 公开 API 响应**会显示：

```
Metadata from OpenAlex (https://openalex.org), released under CC0.
Article source and full text: arXiv (https://arxiv.org).
```

**API 响应 header**（在所有 `/api/*` 路径上自动注入，由
`cmd/qatlasd/main.go` 的 router-level middleware 实现）：

```
X-Attribution: OpenAlex (CC0), Crossref (CC0), arXiv
```

**README + 项目文档**（本节及 [致谢](credits.md)）显示完整 attribution 链。

## 用户责任

如果你**复用**从 QuantumAtlas API 拿到的内容：

- **Metadata（CC0）**：随便用，**仍请**归属到 OpenAlex（公益项目，归属能帮它们拿持续资助）
- **PDF**：公开实例不提供 PDF 下载——请自行到 [arxiv.org](https://arxiv.org/) 拉，按原作者声明使用
- **Wiki 内容**：Apache-2.0（与 [QuantumAtlas-Wiki repo](https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki) LICENSE 一致），归属到本项目即可

## 资产下载开关 (self-hosted)

`QATLAS_ASSET_DOWNLOADS_ENABLED` 是 qatlasd 上的**单一 master 开关**，
默认 `false`。开关 OFF 时（quantum-atlas.ai 等公开实例的默认状态）：

- `GET /api/papers/{id}/markdown` / `markdown/status` 端点**不注册**；客户端拿到 404
- server 不读 `MINERU_*` 字段；不会代客户端做 server-side MinerU 转换
- Contributor 仍可走 `qatlas mineru`（拿自己的 MinerU quota 在本地跑），通过
  `POST /api/papers/{id}/upload-mineru` 把成品 markdown 推到 server——这条
  路径**与开关无关**

开关 ON 时：

- 上述 `/markdown` 系列端点注册，受 `papers:read` 保护
- server 启用 on-demand server-side conversion：客户端 GET `/markdown`，server
  从 `qatlas-pdf` 桶找到 PDF → 用部署方配置的 `MINERU_API_TOKEN` 跑 MinerU
  → markdown 写回 `qatlas-md` 桶 → 后续请求直接缓存命中
- 部署方对**对外受众范围**与**适用法域 ToS**自负责任

详细 env 字段见 [Env Vars 参考 · 资产下载开关](../reference/env-vars.md#server-资产下载开关-self-hosted-可选)。
实施进度跟踪：[#8](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues/8)。

## 撤稿 / 删除请求

如果你是论文作者或权利持有人，希望我们：

- 从 Neo4j catalog 移除某 paper 节点
- 从 `qatlas-md` 桶删除某 paper 的解析 markdown

请提 [GitHub issue](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues) 或邮件
联系维护者（见 [致谢](credits.md#维护者)）。我们会在合理时间内处理（不保证 SLA，
这是一个研究项目）。

注意：metadata 来自 CC0 上游，我们删除自己 catalog 不会让 metadata 从
OpenAlex / Crossref 消失；要从那里删，请直接联系上游。
