# 插件端到端 owning 自己领域，host 核心去领域词

host 核心（qatlasd 中与插件无关的主干）不应携带任何单个插件的领域词或领域表。每个插件都要
**端到端 own 自己的领域**：自己的词表、自己的 PocketBase 集合（由插件自己的 Go package
注册迁移）、`/api/<plugin_id>/*` 下的 REST 路由、插件命名空间下的 hostapi 方法
（例如 `lean.verifications.submit`），以及自己的内存缓存。

## 为什么

早期接入（commits `b79e622` + `160f806`）把 lean-domain 制品直接放进了 host 核心：
`internal/theorems/`, `internal/verifications/`, `internal/routes/theorems.go`,
`internal/routes/verifications.go`，再加上 `hostapi/core.go` 把 `theorems/get`,
`theorems/create`, `verifications/submit` 暴露成通用 host 方法。这是领域泄露——host
不该知道 “theorem” 或 “verification” 是什么；这些是 lean 插件的概念。`pages/get`
（wiki domain）也是同一种泄露。

保留这种泄露会迫使每个新插件都切 host-core 代码，让插件抽取在不破坏 API 的情况下变得不可能，
也会阻止 external（三方）插件像 builtins 一样 own 自己的 schema。

## 移动什么

- **移入 theorems builtin 插件**（`internal/theoremsplugin/` 或类似位置）：`git pull`
  + 透传读取机制、`registry.json` 和 dependency graph 的内存缓存，以及
  `/api/theorems/*` 路由。这个插件在本迭代中**不** own 一个 PocketBase 集合
  （见 ADR `0004`）；已证明 Theorems 从 git 透传读取。
- **移入 wiki builtin 插件**：`hostapi/core.go` 的 `pages/get`（重新放到 `wiki.*` namespace 下）。
  现有 `internal/wiki/` package 成为 wiki 插件的实现 package。
- **本迭代完全移出代码树**：`internal/theorems/`,
  `internal/routes/theorems.go`, `internal/verifications/`,
  `internal/routes/verifications.go`，以及 `hostapi/core.go` 上的 lean-domain methods
  （`theorems/get`, `theorems/create`, `verifications/submit`）——理由见 ADR `0004`。
- **保留在 host 核心**：plugin registry/manifest/lifecycle、Paper catalog
  （`internal/papers/`, `paper_works` PG table——由每个插件共享）、auth/PAT、事件总线、
  PocketBase，以及真正领域中立的 hostapi 方法（`papers/getMarkdown`,
  `papers/getMeta`, `papers/getCitedRefs`, `events/publish`）。

## 机制：一个 builtin-plugin 注册 hook

把 `main.go` 里临时拼出来的逐插件 `RegisterTheorems(...)`, `RegisterVerifications(...)`,
`RegisterWiki(...)`, `RegisterGraph(...)`, `RegisterRAG(...)` 调用，替换为每个 builtin 都实现的单一 hook：

```go
type BuiltinPlugin interface {
    Manifest() plugin.Manifest                 // matches plugins/<id>/plugin.json
    RegisterMigrations(app core.App) error     // own PocketBase collections
    RegisterRoutes(se *core.ServeEvent, deps Deps) error  // /api/<id>/*
    RegisterHostAPI(r *hostapi.Registry) error // <id>.* methods (optional)
}
```

`main.go` 遍历 builtin list 并调用每个 hook。新的 builtin 插件只需加入这个 list——无需给 host 核心做手术。

### 可选能力：`GitPullPlugin`（lean + wiki）

`git pull --ff-only` 被所有透传读取上游 git 仓库的 builtins（wiki, lean）共享。我们把 wiki
package 当前的 pull/status helpers **抽取**到一个 host-neutral 的 `internal/gitpull/` package
（`Pull(dir) (*PullResult, error)`, `ReadGitInfo(dir) GitInfo`, `PullError`, `PullResult`,
`GitInfo`——没有 wiki/lean 专属内容），并通过 builtin 可实现的可选子接口，把 pull **暴露成一种平台能力**：

```go
type GitPullPlugin interface {
    GitRepoDir() string         // working-tree path the host's `git pull --ff-only` runs in
    OnPullSucceeded() error     // plugin-specific post-pull work (wiki: cache.Refresh;
                                // lean: registryCache.Reload)
}
```

host-core 平台代码（也就是调用 `BuiltinPlugin` hooks 的同一个循环）对每个 builtin 做
`GitPullPlugin` type-assertion；满足接口的插件，平台会挂载一个**统一形态**：
`POST /api/<id>/sync/pull` + `GET /api/<id>/sync/status`，并保证两个路由在所有
pull-style 插件上行为一致。不需要 pull 的插件（graph, rag）不实现这个子接口；平台跳过它们。

### 为什么用子接口（而不是只做辅助 package）

两个选项都考虑过。纯 helper（每个插件自己的 `RegisterRoutes` 调用 `gitpull.Pull`）会把路由形态
和错误映射的一致性留给评审时人工把关。子接口把 “git-synced builtin” 提升为一等平台能力，
所以 host 能为每个自愿接入的插件保证一个 canonical `/api/<id>/sync/*` contract——想要同样行为的
external（socket/stdio）插件以后也可以自愿接入。成本只是一个很小的可选 Go interface；收益是为一种反复出现的形态统一 contract。

## 影响

- 如果/当 theorems 插件的 hostapi 方法（位于插件自己的 namespace 下，例如 `theorems.*`）增加时，
  external（socket/stdio）插件也可以写入这些方法——但不在本迭代。早期 commits 添加的
  面向 push 的 `verifications` 集合被完全移除（见 ADR `0004`）。
- 路由遵循逐插件 namespace：`POST /api/theorems/sync/pull`（由 `GitPullPlugin` 挂载的统一形态）、
  `GET /api/theorems/list`, `GET /api/wiki/pages` 等。
- `hostapi/core.go` 收缩到真正由 host 共享的关注点。
