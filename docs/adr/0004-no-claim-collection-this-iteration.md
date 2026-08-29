# 本轮不引入 QA 侧 Claim 集合 / Claims 页

我们**不**把 Claims（从论文中抽取、尚未证明的自然语言陈述）持久化进 PocketBase，**不**在
QA 的 WebUI 增加 `/claims` 页面，也**不**保留 `b79e622` + `160f806` 增加的
`internal/theorems/`（实际存的是 Claims，不是 Theorems）或 `internal/verifications/`
集合、路由或 hostapi 方法。它们全部移除。

Claim 起草和 issue 提交完全发生在**上游 lean 仓库**内（`qatlas-lean` 的
`contrib claim` localhost WebUI——见 ADR `0008`，该 ADR 已将这部分从 QA client 移走；最初
在 ADR `0005` 下是 QA 侧 Copilot-SDK 插件）。该 localhost WebUI 里的 "Confirm claim"
按钮**只会在 `agony/qatlas-lean` 上提交一个 gitea issue**。在这个 issue 产出一个已证明的
Theorem、落入上游 Lean-content 仓库，并被 **theorems 插件**拉取之前（ADR `0002`），QA server
什么也看不到。

## 为什么本轮 QA 什么也不做

交接说明把 `papers / claims / units` 列为 WebUI 的权威界面，但用户已经澄清：
这是一个**终态**愿景。在终态里，localhost contrib WebUI 会吸收所有 Lean 侧 agent（scout、
statement、enricher、issue-raiser、solver、repair、the three Magi），上游会拆出一个只存放内容的
`QuantumAtlas-Theorems` 仓库，届时 Claims 也可能从另一个底座暴露出来。我们拒绝把
QA 侧 Claim 存储设计两遍。

对这个过渡迭代来说，相关事实是：

- **不需要 QA 侧持久化**。localhost agent 已经维护自己的 session state；gitea 是已提交
  Claims 的权威收件箱；上游 Lean-content 仓库是已证明 Theorems 的权威存储。
- **不需要 QA 侧展示**。WebUI 展示 `/wiki`（markdown content）和 `/theorems`（经 theorems
  插件得到的已证明 Theorems）；二者都直接来自 `git pull`。如果未来确实需要“待证明 Claims
  列表”视图，也可以稍后从 gitea 或终态 contrib WebUI 派生——不是从 QA 集合派生。
- **领域词不属于 host core**。`b79e622` 里的 `internal/theorems/` +
  `internal/verifications/` 是放在 host core 层的领域词（见 ADR `0003`）；即使以后还要
  Claims，它们也不会回到这个位置。

## 影响

- 本轮移除 `internal/theorems/`（存的是 pre-proof `statement_nl`，按修正后的术语即
  **Claims**）、`internal/routes/theorems.go`、`internal/verifications/`、
  `internal/routes/verifications.go`、`hostapi/core.go` 上的 theorems/verifications 方法、
  `main.go` 中的注册，以及相关测试。
- `theorems` 插件 id 被释放出来，用于它本该表示的含义（已证明的 Theorems，经 git
  read-through——ADR `0002`）。
- `b79e622`/`160f806` 中针对已删除 theorems/verifications 集合的测试随之删除；插件平台测试和
  external-transport（socket/stdio）测试保留。
- localhost contrib agent 的 WebUI 在**自己的 state** 里保存 Claim 草稿；QA 永远看不到它们。
  等终态 `QuantumAtlas-Theorems` 仓库存在时，"Confirmed Claims" 会作为档案记录在那个内容仓库中，
  与已证明 Theorems 并列——届时 theorems 插件的 git-pull pipeline 会自然把它们拉进来，无需新的
  PocketBase 集合。
