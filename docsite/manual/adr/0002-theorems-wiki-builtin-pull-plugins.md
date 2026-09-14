# theorems / wiki 为 builtin 拉取插件，而不是 socket-push 守护进程

**theorems 插件**（从上游 Lean-content 仓库拉取已证明 Theorems——今天是
`agony/qatlas-lean`，最终会是专门的 `QuantumAtlas-Theorems` 内容仓）和 **wiki 插件**
（从 `QuantumAtlas-Wiki` 拉取 markdown 知识页）都作为 `kind=builtin` 插件集成。它们和
graph、rag 一样，把 `/api/theorems/*` 与 `/api/wiki/*` 暴露成对上游仓库的**服务端
`git pull --ff-only`** 检出目录的透传读取：theorems 插件读取 `registry.json`
（加上 `audit_records/certified.json`、证据档案，以及 `registry.json` 引用的
Lean 源文件）；wiki 插件读取 markdown wiki 缓存。wiki 已经按这种方式拉取
（`POST /api/wiki/sync/pull`）；我们把这个模式泛化。

我们**否决**早期树内分支（`qatlas-lean@timidly/ts-qa-plugin`）正在构建的 socket-push
模型：一个长期运行的 Lean 守护进程通过 `jsonrpc-ws` 拨入 host，消费 theorem 事件，再通过
`verifications/submit` 把结果推回。

## 插件命名：`theorems`，不是 `lean`

插件 id 是 **`theorems`**，不是 `lean`。理由：

- 它命名的是**插件服务的内容**（已经证明的 Theorems），而不是证明器语言。
- 它与最终的内容仓 `QuantumAtlas-Theorems` 对齐（这个过渡迭代仍然拉取更大的
  `qatlas-lean` 仓库，但那是实现细节——plugin id ≠ upstream repo name，正如 `graph` ≠ neo4j、
  `rag` ≠ qdrant）。
- 它避免把“具体是 Lean 4”编码进 URL 合约——未来的 Coq 证明器仍可发布到这个插件的命名空间。

## 为什么是 pull，不是 push

- theorems ↔ QA 是**围绕持久 git 制品的松耦合协作**，不是紧密的实时 RPC 循环。
  上游 Lean-content 仓库是已证明 Theorems 的事实来源；QA 负责拉取它。
- 与 graph/rag 一致（同语言 Go builtin，透传读取一个后端，`/api/*`）。
- 交接已经移除了编排守护进程：**Claim 起草和 issue 提交是人
  + localhost contrib agent webui**（`qatlas-lean contrib claim`——由 ADR `0008` 迁到上游 lean
  仓库），不是 host poll/push 循环。
  已经没有守护进程来持有 WS 连接。
- 每个插件一个 git-pull 端点，比运维一个 WS server + 连接生命周期少得多。

## 本迭代中 theorems 插件负责什么

- Lean-content 上游的服务端 git 检出目录，以及通过 `GitPullPlugin` 提供的 `git pull --ff-only`
  机制（挂载在 `POST /api/theorems/sync/pull`, `GET /api/theorems/sync/status`）。
- `registry.json` + `audit_records/certified.json` + 相关 Lean 源文本的内存缓存，
  每次成功 pull 后刷新（镜像 `wiki.Cache.Refresh`）。
- WebUI 使用的 `/api/theorems/*` 下只读 HTTP 路由：Theorems 列表/详情、dependency-DAG 视图、
  axiom/sorry-free 标记、按需源码、统计。

theorems 插件在本迭代中**刻意不** own 一个 PocketBase collection。已证明 Theorems 是从 git
透传读取，不持久化进 PocketBase；Claims（上游 `qatlas-lean` 仓库作为 gitea
issues 提交的证明前自然语言陈述）在本迭代不进入 WebUI 范围——见 ADR
`0004-no-claim-collection-this-iteration.md`。以后如果需要一个 PocketBase 持久化视图，再以单独 ADR 实施。

## 参考，不移植

`qatlas-lean@timidly/ts-qa-plugin` **仅作参考**：我们吸收其中的好思路，但基于 QuantumAtlas
`main` 现有插件平台（registry、manifest、graph/rag、保留的 external transport）实现——不 cherry-pick，也不基于该分支。

## 影响

- 已证明 Theorem 的结论在 pull 时从 `registry.json` 的 `audit_status` 读取，**不是**
  通过任何东西 POST 进来。早期提交添加的面向 push 的 `verifications` collection
  + lean-domain hostapi methods（`b79e622` + `160f806`）被移除（见 ADR `0004`）。
- `external`（socket/stdio）transport 继续留在树里，供未来三方插件使用；但本迭代没有一方插件使用它们。
- WebUI 增加 `/theorems` 页面（通过插件透传读取）以及 “Pull Now” 按钮 + webhook trigger，
  调用 `POST /api/theorems/sync/pull`——与 `/wiki` 对称。
