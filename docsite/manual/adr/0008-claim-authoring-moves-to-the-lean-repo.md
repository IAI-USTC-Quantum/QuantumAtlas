# Claim 起草 + issue 提交迁回上游 lean 仓，退役 QA client claim 插件

_取代 ADR `0005`。_

Claim 起草和 `type/theorem` issue 提交 **从 QuantumAtlas client 迁出，进入上游 lean 仓**
（`agony/qatlas-lean`）。ADR `0005` 中作为 QA 侧 localhost 插件发布的内容（`qatlas contrib
claim` —— 位于 `qatlas/client/claim/` 下的 Copilot/anthropic/openai-SDK 起草器 + WebUI）
**退役：保留为随包分发的一方 builtin 插件，但默认关闭，只能通过配置驱动的 `plugins` 列表启用**
（见下文"插件平台如何保持通用"）；qatlas-lean 现在以自己的 `qatlas-lean contrib claim
<paper>` localhost WebUI 提供等价流程，由 **qatlas-lean 的 CLI flat-agent runner**（`copilot -p
--agent claim-drafter`）驱动，**不是 SDK**。

QuantumAtlas 只保留它已经承担的角色：**文献托管方**（通过 `GET /api/papers/{id}/markdown`
提供 paper Markdown，并通过 `GET /api/papers/lookup` 解析引用，见 ADR `0007`），以及已证明
Theorems 的 **theorems 拉取插件**消费者（ADR `0002`）。qatlas-lean 现在是这些只读端点的
*client*。

## 为什么迁到 lean

- **一个仓库拥有完整的 prover 侧界面。** Claim 起草 → 证明（solver）→ 审计（三贤者）→
  注册表/账本现在全部在 `qatlas-lean`。贡献者只需阅读一个仓库，"所有 Lean 相关内容"有唯一归宿
  ——这与 ADR `0005` 设想的终态相反（当时让 QA 的 contrib WebUI *吸收*所有 lean agent，并把
  qatlas-lean 拆成仅存放内容的 `QuantumAtlas-Theorems` 仓）。该设想已**放弃**；本 ADR 是新的边界终态。
- **contrib 路径不使用 SDK。** ADR `0005` 在 QA client 中放了一个
  Copilot/anthropic/openai-SDK 起草器。qatlas-lean 已经把每个 agent 都作为扁平的 `copilot -p
  --agent <name>` CLI 进程运行；Claim 起草器只是其中一个（`claim-drafter`）。不引入第二套 LLM
  调用机制，也不在 QA 侧保存 LLM 凭据。
- **mock 仍是完整替身。** contrib 流程复用 qatlas-lean 自己的 Gitea client，因此 `backend=mock`
  会提交到 daemon 已经轮询的文件后端 mock，`backend=gitea` 则在贡献者自己的 token 下提交真实 issue。
  整条起草 → 求解 → 审计流水线可以在零账号条件下端到端测试（`tests/integration/claim_contrib.py`）。
- **QA 边界更简单，而不是更丰富。** QA 通过 HTTP（以及后续 MCP —— ADR `0007`）暴露只读
  paper/corpus 数据；它从不起草、持久化或维护 Claims/Theorems。client claim 插件默认关闭（不出现在配置
  `plugins` 列表中）意味着 QA client 代码默认不进入 lean 领域；文件仍保留，但直到用户通过配置选择启用前都处于休眠（未激活）状态。

## 插件平台如何保持通用（配置驱动启用）

QA client 插件系统有意保持**通用**且**配置驱动**，与 `qatlasd` 的 builtin 插件模型一致（ADR
`0001`/`0003`）：哪些一方插件被*启用*由用户配置文件决定——即 `config.yaml` 的 `plugins:` 列表
——**而不是硬编码在代码里**。`pip install quantum-atlas` 后，用户通过编辑该列表打开/关闭某个
builtin；无需改代码。一方插件（`lean`、`claim`）随包分发，并作为 catalog（目录）登记在
`qatlas/client/plugins/registry.py`；只有当 builtin 的名字出现在已启用的 `plugins` 列表中（默认只有
`lean`）**且**其 `available()` 环境检查通过时，它才贡献 CLI 命令。第三方插件通过 `qatlas.plugins`
入口点注册。因此，"退役" claim 插件只是**把它留在默认启用集合之外**——文件仍作为随包分发、可选择启用的插件保留，
`pyproject.toml` 的 `contrib` extra（fastapi/uvicorn）也保留，使其在启用时仍可运行。（`qatlasd`
是 Go，配置路径不同，但遵循同样的配置驱动原则——见 lean 侧交接。）

## 变化内容

- **QA 中默认关闭（配置驱动，文件保留）**：client 插件名单现在由配置驱动——`qatlas/config.py`
  增加 `plugins` 列表，`qatlas/client/plugins/registry.py` 只启用其中点名的 builtin（默认 `lean`）。
  `claim` **不**在默认集合中，因此 `qatlas contrib` 不再暴露它；用户可用 `plugins: [lean, claim]`
  重新启用。插件文件（`qatlas/client/claim/`）及其测试（`tests/client/claim/`）**保留**为随包分发、可选择启用的插件；
  `pyproject.toml` 的 `contrib` extra（fastapi/uvicorn）也**保留**，以便启用时运行。旧的专用
  `claim_plugin_enabled` 标志被删除，改用通用 `plugins` 列表。只更新用于*暴露入口*的文档字符串
  以及 `qatlas/client/contrib.py` 中对 `claim` 的引用，使其指向 qatlas-lean。
- **QA 中保留**：`lean` 透传插件（`qatlas/client/leanplugin/` —— `qatlas lean <subcommand>`
  仍驱动配置好的 qatlas-lean checkout，因此 `qatlas lean contrib claim …` 会到达新流程）；文献读取界面
  （`/api/papers/*`，ADR `0007`）；theorems 拉取插件（ADR `0002`）。
- **qatlas-lean 中新增**（它的仓库，不是本仓库）：`src/qatlas-lean/contrib/claim/`（移植后的流程、stdlib
  `http.server`、CLI-agent 起草器）+ 一个 `claim-drafter` flat agent + 一个 `qa_base_url` 配置旋钮。

## 不变内容（重申决策，而非推翻）

- **ADR `0004` —— QA 侧没有 Claim 集合 / 没有 `/claims` 页面。** 仍然成立；事实上被进一步强化——
  QA 不持久化任何 Claims 相关内容。只更新 `0004` 中指向起草*机制*的交叉引用（它现在位于 qatlas-lean，
  而不是 QA 的 Copilot-SDK 插件）。
- **ADR `0007` —— references 是 `kind:id`，由服务端通过 `/api/papers/lookup` 解析。** 协议
  完全相同；只有*调用者*变化（从 QA 的 contrib agent 改为 qatlas-lean 的 claim 流程）。`0007`
  已经记录了这个 prover-as-client 边界。
- **ADR `0002` —— theorems 插件从 git 拉取已证明 Theorems。** 不变。

## 备选方案

- **保持 QA 侧 claim 插件启用（ADR `0005` 原样）。** 否决：这会把*维护中的* Lean 相关流程拆到两个仓库，
  让 SDK 代码路径继续存活在 QA client 中，并与 `0007` 现在记录的"qatlas-lean 拥有 claim/proof/theorem
  工作流"边界相矛盾。
- **直接删除插件文件。** 否决：启用由配置驱动（builtin 只有在配置 `plugins` 列表中被点名时才生效），
  因此一个只是*不在默认集合中*的插件已经是休眠的——删除相较于默认关闭没有额外收益。保留这些文件作为随包分发、可选择启用的插件，
  可以在零活跃界面成本下保留一个可选本地兜底 + 参考实现。漂移风险有边界：插件默认关闭，
  并且明确标记为非权威（qatlas-lean 拥有维护中的流程）。
- **每个插件一个布尔标志（旧的 `claim_plugin_enabled`）。** 否决，改用一个通用 `plugins` 列表：
  每个插件一个专用开关字段无法泛化，而单一列表可扩展到任意 builtin，并在一个地方表达"已启用集合"。

## 影响

- QA client 界面缩小：`qatlas contrib` 默认失去 `claim` 子命令（默认启用集合只有 `lean`）。`claim`
  仍随包分发，只需一次配置编辑（`plugins: [lean, claim]`）即可恢复。
- **维护中的/权威** issue 正文形状（`## Claim` / `## Why it matters` / `## Paper reference`
  / `## References` + 末尾 `claim_id:` 标记）现在**只存在一份**，位于 qatlas-lean 的
  `contrib/claim/issue.py`。QA 只保留一份休眠、未注册的副本（自己的 `qatlas/client/claim/issue.py`），
  它是非权威且不对外暴露的。
- 发布前不承诺兼容性；旧 QA 插件提交过的 issue 历史已经是权威形状，因此 qatlas-lean 的去重逻辑可原样识别。
