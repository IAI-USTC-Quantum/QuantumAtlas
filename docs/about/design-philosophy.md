# 设计哲学

QuantumAtlas 的几个核心设计决定都不是凭直觉做的——每一条都有具体的"如果不这样会变成什么"理由。这一节把它们捋一遍，方便后来人理解项目边界。

## 决定 1：分类和关联是两回事

研究笔记容易长成一坨——一篇 paper 既是"被引用对象"又是"实现来源"又是"作者作品"，硬扔进一种结构会让查询和叙述互相干扰。

QuantumAtlas 把这两件事切开：

- **分类 / 叙述** → 留给 Wiki（人读 + LLM 读）
- **关联 / 查询** → 留给 Neo4j（图谱）

Wiki 写"这个算法解决什么问题、怎么解的、跟哪些原语相关"；Neo4j 回答"用过 QFT 的算法有哪些"——两个问题，两种结构，互不污染。

## 决定 2：Wiki 是真相，Neo4j 是索引

很多项目把图数据库当 source of truth——所有信息直接写进 Neo4j。但 Neo4j 的属性 / 关系**对人不友好**：

- 没法 git diff
- 没法 review PR
- 没法本地 grep
- 没法 LLM 友好读

所以我们让 **Wiki Markdown 是真相**——可以 review、可以 PR、可以离线读。Neo4j 是**从 Wiki 派生的副本**，挂了重 sync 即可，绝不依赖图谱有"独家信息"。

这意味着两条原则：

1. **Neo4j 节点 / 关系全部能从 Wiki 重建**——不能有"只存在于 Neo4j 的事实"。
2. **冲突时以 Wiki 为准**——sync 是 Wiki → Neo4j 单向的。

## 决定 3：Raw 是证据，不是工作区

Raw Sources 存：

- 原始 PDF（不可变）
- MinerU / PyMuPDF 解析的 Markdown（可重做但通常不动）
- arXiv 元数据 JSON
- MinerU 解析出的图片

它**不是工作区**：

- 不在这里改 PDF
- 不在这里编辑 Markdown（编辑发生在 Wiki repo）
- 不在这里 commit 笔记

Raw 的设计目标是**永远可追溯**——你随时能回到"原始论文是什么样的"。Wiki 是从 Raw 提炼出来的，但 Raw 不依赖 Wiki。

## 决定 4：write 鉴权，read 开放

Wiki 是**公开仓库**（GitHub 上人人可见）。论文本身也是公开的（arXiv preprint）。所以读接口**不需要 auth**——任何人都能查 wiki / 图谱 / 论文资产。

防御只针对**写**：

- 上传论文 → 需要 PAT `papers:write`
- 创建分享链接 → 需要 PAT `shares:write`
- 改 Wiki → git PR，server 端的 `POST /api/wiki/sync/pull` 只接受 fast-forward

这极大简化了用户体验：不需要为了**只是看看**而注册 / 拿 token。同时也避免了"私有但被反代外泄"的隐私风险——本来就没机密。

## 决定 5：客户端和服务端独立可演化

`qatlas` Python 客户端和 `qatlas-server` Go 服务端在**两个不同的 release artifact**里。意味着：

- 客户端在 PyPI 滚 `0.2.x`
- 服务端在 GitHub Release 滚 `v0.2.x` binary
- 同一份代码 repo（QuantumAtlas）出，version 同步 bump

但是**API 是稳定的契约**——升级一个不强制升级另一个。CI / 长跑 agent 可以钉死 server 用 `v0.2.7`，client 用 `0.2.9`，照样工作。

## 决定 6：多边缘 active-active

QuantumAtlas 的生产部署横跨海外 + 国内两条线路。两边都是完整 qatlas-server，共享底层的 RustFS + Neo4j（通过 EasyTier mesh）。

**为什么不是 anycast / Cloudflare front**：

- PocketBase session token cookie 是按 domain 颁发的
- RustFS SigV4 presigned URL 不能跨 host 共享（Host header 进 canonical request）
- LE 真证书 + 阿里云 IP 自签 是两种 TLS 模型

所以选了 active-active：**每条线路独立 SSL endpoint，user 显式选**。RackNerd 默认走 DNS，阿里云走直 IP，client 自己根据 `QATLAS_SERVER_URL` 决定。

不优雅，但**比起 anycast 配的复杂度，可控性更高**。

## 决定 7：scope 是显式 opt-in，不是隐式

PAT scope 词表（`papers:write` / `shares:read` / `shares:write`）默认**空集**。新建 PAT 不勾任何 scope = 这个 PAT 啥都干不了。

灵感来自 GitHub fine-grained PAT：**显式 opt-in 总好过 over-grant**。

- 不会"我只想让 CI 上传论文，结果 PAT 顺手能改 Wiki"
- 不会"share 链接 leak 出去结果能看到任何文件"
- 不会"老 PAT 还在但 scope 体系演进了，意外获得了新权限"

scope 是**编译时静态**的（`internal/pat/scopes.go`）——加新 scope 需要改代码 + 重新部署。让权限模型变成 review 标的，而不是 server admin 后台静默改。

## 决定 8：默认 dry-run

破坏性操作（`qatlas-server storage prune`）**默认 dry-run**，必须 `--yes` 才真执行。借鉴 `rclone` / `terraform plan`。

理由：

- 让"我只是看看"和"我真要删"分开
- 让脚本误调不会立即灾难（缺 `--yes` 就空跑）
- 让 review 友好（diff 看到 `--yes` 就知道要慎重）

## 决定 9：所有路径相对 .env 解析

`.env` 里的 `WIKI_DIR=../QuantumAtlas-Wiki` **相对 .env 文件所在目录**解析，而不是 systemd `WorkingDirectory` 或 shell CWD。

这样：

- `.env` 挪了路径，相对 dir 跟着走，行为不变
- systemd 启动跟手工启动语义一致
- 不需要在 systemd unit 里写 `WorkingDirectory=`

实现见 `internal/config/config.go::expandPath`。

## 决定 10：HTTP status code 是传输层，body 是真相

`/api/health` 即使 dependency 全挂了也返回 **HTTP 200**。`/api/graph/*` Neo4j 故障也返回 200 + `{"error":...}`。

理由：

- HTTP 5xx 在 reverse proxy / load balancer 链路上会被层层 trip 成 "down"，让一个小依赖挂掉变成全站不可用
- monitor 看 body status 更准确，因为它体现**真实业务状态**而不是 transport 错误
- 504 / 502 是 transport 真挂时才发——业务层挂跟传输层挂不该混

如果想"degraded 时 LB 把流量切走"，把它放到监控层做（监控读 body status 决定）。

---

这些决定背后都有 trade-off。看到任何一处觉得"应该反过来做"，去翻 issue 或开新 issue，多半已经讨论过。
