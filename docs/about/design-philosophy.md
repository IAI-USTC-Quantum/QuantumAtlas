# 设计哲学

QuantumAtlas 的几个核心设计决定都不是凭直觉做的——每一条都有具体的"如果不这样会变成什么"理由。这一节把它们捋一遍，方便后来人理解项目边界。

## 决定 1：分类和关联是两回事

研究笔记容易长成一坨——一篇 paper 既是"被引用对象"又是"实现来源"又是"作者作品"，硬扔进一种结构会让查询和叙述互相干扰。

QuantumAtlas 把这两件事切开：

- **分类 / 叙述** → 留给 Wiki（人读 + LLM 读）
- **关联 / 查询** → 留给 Neo4j（图谱）

Wiki 写"这个算法解决什么问题、怎么解的、跟哪些原语相关"；Neo4j 回答"用过 QFT 的算法有哪些"——两个问题，两种结构，互不污染。

## 决定 2：Wiki 以 concept 为唯一可浏览单位（Wikipedia 风格）

区分 `entity`（算法/原语/人物）、`comparison`（对比页）、`source`（论文）等多种页面类型，会让同一件事散落在好几种结构里——读者要先理解"页面类型体系"才能浏览，作者要纠结"这该建 entity 还是 comparison"。

参照 Wikipedia，我们收敛成**一种**可浏览单位：**concept 词条**。

- **comparison 融进 concept**：对比不再是独立页面类型，而是 concept 正文里的一段 + 指向被对比双方的交叉链接。
- **source 是引用，不是条目**：处理过的论文（`source`）只在词条「参考文献」里被 `[[paper-arxiv-*]]` 引用，**默认从浏览 / 搜索里隐藏**（`/api/pages`、`/api/search`；逃生口 `?page_type=source` / `?include_sources=true`）。论文是证据，不是读者要逐篇翻的目录。
- **子类靠 `category` 而非 `type`**：`algorithm` / `primitive` / `technique` / `problem` / `framework` / … 用一个轴表达，列表按 category 分组。

追加内容时的**合并原则**（避免 concept 越长越乱）：

1. **概念相似或可视作同一概念**（如 variational ≈ parameterized quantum circuit）→ **合并**为一个词条，整合两边正文。
2. **A 是 B 的子概念 / 延伸**（如 hamiltonian simulation ⊂ quantum simulation）→ **不合并**，用双向 `[[...]]` 交叉链接点明上下位关系。

`entity` / `comparison` 的 type 常量在 Go 侧仍可解析（兼容未迁移的历史数据），但统计与 UI 一律按 concept 处理；新页面统一 `type: concept`。规模化追加见 [generate-wiki-content.md](../guides/generate-wiki-content.md) 的多 subagent 流水线。

> **Graph 暂时隐去**：图谱可视化尚未打磨好，前端入口（侧栏 + 首页 CTA）已隐藏，但路由与 `/api/graph/*` 后端保留，待成熟后重新开放。读者现在以词条为中心浏览，关系靠词条内的 `[[...]]` 交叉链接表达。

## 决定 3：Wiki 是真相，Neo4j 是索引

很多项目把图数据库当 source of truth——所有信息直接写进 Neo4j。但 Neo4j 的属性 / 关系**对人不友好**：

- 没法 git diff
- 没法 review PR
- 没法本地 grep
- 没法 LLM 友好读

所以我们让 **Wiki Markdown 是真相**——可以 review、可以 PR、可以离线读。Neo4j 是**从 Wiki 派生的副本**，挂了重 sync 即可，绝不依赖图谱有"独家信息"。

这意味着两条原则：

1. **Neo4j 节点 / 关系全部能从 Wiki 重建**——不能有"只存在于 Neo4j 的事实"。
2. **冲突时以 Wiki 为准**——sync 是 Wiki → Neo4j 单向的。

## 决定 4：Raw 是证据，不是工作区

Raw Sources 存：

- 原始 PDF（不可变）
- MinerU 解析的 Markdown（可重做但通常不动）
- arXiv 元数据 JSON
- MinerU 解析出的图片

它**不是工作区**：

- 不在这里改 PDF
- 不在这里编辑 Markdown（编辑发生在 Wiki repo）
- 不在这里 commit 笔记

Raw 的设计目标是**永远可追溯**——你随时能回到"原始论文是什么样的"。Wiki 是从 Raw 提炼出来的，但 Raw 不依赖 Wiki。

## 决定 5：write 鉴权，read 开放

Wiki 是**公开仓库**（GitHub 上人人可见）。论文本身也是公开的（arXiv preprint）。所以读接口**不需要 auth**——任何人都能查 wiki / 图谱 / 论文资产。

防御只针对**写**：

- 上传论文 → 需要 PAT `papers:write`
- 改 Wiki → git PR，server 端的 `POST /api/wiki/sync/pull` 只接受 fast-forward

这极大简化了用户体验：不需要为了**只是看看**而注册 / 拿 token。同时也避免了"私有但被反代外泄"的隐私风险——本来就没机密。

## 决定 6：客户端和服务端独立可演化

`qatlas` Python 客户端和 `qatlasd` Go 服务端在**两个不同的 release artifact**里。意味着：

- 客户端在 PyPI 滚 `0.2.x`
- 服务端在 GitHub Release 滚 `v0.2.x` binary
- 同一份代码 repo（QuantumAtlas）出，version 同步 bump

但是**API 是稳定的契约**——升级一个不强制升级另一个。CI / 长跑 agent 可以钉死 server 用 `v0.2.7`，client 用 `0.2.9`，照样工作。

## 决定 7：多边缘 active-active

QuantumAtlas 的生产部署可在多个 region active-active —— 每条线路一个完整 qatlasd，
共享底层的 RustFS + Neo4j（通过 EasyTier mesh）。

**为什么不是 anycast / Cloudflare front**：

- PocketBase session token cookie 是按 domain 颁发的
- RustFS SigV4 presigned URL 不能跨 host 共享（Host header 进 canonical request）
- LE 真证书与自签证书是两种 TLS 模型，难以靠 anycast 统一

所以选了 active-active：**每条线路独立 SSL endpoint，user 显式选**，client 自己
根据 `~/.config/qatlas/config.yaml` 的 `server_url:` 字段（或 `--server-url` CLI flag 临时覆盖）决定走哪条线路。

不优雅，但**比起 anycast 配的复杂度，可控性更高**。

## 决定 8：scope 是显式 opt-in，不是隐式

PAT scope 词表（`papers:write` / `papers:read` / `graph:read` / `wiki:write` / `wiki:read`）默认**空集**。新建 PAT 不勾任何 scope = 这个 PAT 啥都干不了。

灵感来自 GitHub fine-grained PAT：**显式 opt-in 总好过 over-grant**。

- 不会"我只想让 CI 上传论文，结果 PAT 顺手能改 Wiki"
- 不会"老 PAT 还在但 scope 体系演进了，意外获得了新权限"

scope 是**编译时静态**的（`internal/pat/scopes.go`）——加新 scope 需要改代码 + 重新部署。让权限模型变成 review 标的，而不是 server admin 后台静默改。

## 决定 9：默认 dry-run

破坏性操作（`qatlasd storage prune`）**默认 dry-run**，必须 `--yes` 才真执行。借鉴 `rclone` / `terraform plan`。

理由：

- 让"我只是看看"和"我真要删"分开
- 让脚本误调不会立即灾难（缺 `--yes` 就空跑）
- 让 review 友好（diff 看到 `--yes` 就知道要慎重）

## 决定 10：所有路径相对 .env 解析

`.env` 里的 `WIKI_DIR=../QuantumAtlas-Wiki` **相对 .env 文件所在目录**解析，而不是 systemd `WorkingDirectory` 或 shell CWD。

这样：

- `.env` 挪了路径，相对 dir 跟着走，行为不变
- systemd 启动跟手工启动语义一致
- 不需要在 systemd unit 里写 `WorkingDirectory=`

实现见 `internal/config/config.go::expandPath`。

## 决定 11：HTTP status code 是传输层，body 是真相

`/api/health` 即使 dependency 全挂了也返回 **HTTP 200**。`/api/graph/*` Neo4j 故障也返回 200 + `{"error":...}`。

理由：

- HTTP 5xx 在 reverse proxy / load balancer 链路上会被层层 trip 成 "down"，让一个小依赖挂掉变成全站不可用
- monitor 看 body status 更准确，因为它体现**真实业务状态**而不是 transport 错误
- 504 / 502 是 transport 真挂时才发——业务层挂跟传输层挂不该混

如果想"degraded 时 LB 把流量切走"，把它放到监控层做（监控读 body status 决定）。

---

这些决定背后都有 trade-off。看到任何一处觉得"应该反过来做"，去翻 issue 或开新 issue，多半已经讨论过。
