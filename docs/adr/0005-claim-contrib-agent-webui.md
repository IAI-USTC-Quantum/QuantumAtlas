# Claim 起草走 localhost claim agent + WebUI

_被 ADR `0008` 取代——Claim 起草 + issue 提交已从 QA client 迁出，进入上游 lean 仓库
（`qatlas-lean` 的 `contrib claim` WebUI，CLI agents 而非 SDK）。下面的决策作为首次交付内容的记录保留；
当前边界请读 `0008`。_

`qatlas contrib claim` 命令会启动一个 **localhost** WebUI，以及一个由 Copilot-SDK 驱动的自定义
agent；它在人类参与审核的前提下，围绕一篇论文起草一个或多个 Claims（基准论文 +
近乎逐字的自然语言陈述 + 明示/隐含假设 + agent 基于 OpenAlex / arxiv 解析出的 reference IDs）。
命令名表达的是**贡献类型**，不是每个 session 的数量——一个 session 可以产出多个 Claim 草稿
（匹配 scout 的“extract every formalization-ready claim” contract）。当用户在 WebUI 中对单个
已起草 Claim 点击 **Confirm claim**（中文：**确认 claim**）时，localhost 进程会使用用户自己的
gitea token，在 `agony/qatlas-lean` 上每次点击提交**一个 gitea issue**——这就是本轮工作流的终点。
QA server 不参与（见 ADR `0004`）。

这个模式是计划中一组 localhost contrib WebUIs 的**第一个**。终态会把所有 Lean 侧 agent
（`scout`、`statement`、`enricher`、`issue-raiser`、`solver`、`repair`、`melchior`、
`balthasar`、`casper`）合并进同一个 contrib WebUI 界面，同时用一个只存放内容的
`QuantumAtlas-Theorems` 仓库替代今天 `qatlas-lean` 仓库中存放代码的部分。本轮只交付 Claim 起草工作流；
其余命名是为了向前保持一致。

## 为什么用 localhost agent + WebUI，而不是 CLI one-shot

- 如果 CLI 一次性串起 `scout → enricher → issue-raiser → POST gitea`，就会让 LLM 直接接触
  gitea，产出的 Claim 没有人类审核。交接说明把 Claim 质量列为硬性要求（“基准论文 +
  reference IDs + 近乎逐字的自然语言陈述 + 明示/隐含假设”）；LLM 一次性执行
  并不能稳定达到这个门槛。
- localhost WebUI 给人类一个与 agent 交互的位置——阅读、编辑、要求再起草一版、接受 references、
  丢弃坏 references——同时不需要搭建完整 web app，也不要求 QA server 运行 LLM。

## 为什么是 **localhost**，不是 QA server

- QA server 不运行 LLM agents，而且按交接说明也不应该运行。把 Copilot-SDK 代码放进 Go
  `qatlasd` 进程，会制造我们不想要的 token 管理、计费和并发问题。
- 每个用户都在本地持有自己的 Copilot SDK credentials 和自己的 gitea token；QA server 永远不需要代理二者。
- localhost WebUI 是每个 session 短生命周期的——用户运行 `qatlas contrib claim`，浏览器弹出，
  工作流执行，完成后进程退出。

## "Confirm" 按钮写入 gitea issue

- 按钮文本：英文 **"Confirm claim"**，中文 **"确认 claim"**（领域词 `claim` 在各 locale
  中保留，以匹配底层 schema/CLI/URL 词表）。
- 行为：localhost agent 把已起草 Claim 渲染成规范 issue body 形态，使用用户 token
  调用 gitea 的 `POST /repos/agony/qatlas-lean/issues`，并显示生成的 issue URL。
- issue 提交后，旧有的仓内 `qatlas-lean` Lean-prover agents（solver、magi 等）会像今天一样接手——
  本轮不改变 prover 侧。

## QA 侧拉取

- 已证明的 Theorem 通过 `theorems` builtin 插件的 git-pull 到达 QA WebUI（ADR `0002`）：
  用户在 `/theorems` 上点击 "Pull Now"（或 webhook 触发），插件在自己的 `qatlas-lean` checkout
  上运行 `git pull --ff-only`，`/theorems` 随即反映新条目。
- Claims 没有 QA 侧写路径（ADR `0004`）；直到 Claim 在上游仓库的 `registry.json` 中变成已证明
  Theorem，QA server 才会看到它。

## 命名

- CLI：`qatlas contrib claim`（单数——命令名指的是**贡献一个 Claim 这件事**；一个 webui session
  可以产出多个 Claim 草稿并提交多个 gitea issues，但命令名反映的是贡献类型，不是每个 session
  的数量）。现有 `qatlas contrib` 组已经承担“贡献者工作流”（今天是 PDF / MinerU uploads）；
  这个命令作为第一个由 agent 驱动的贡献者工作流放进去。
- 按钮文本：英文 **"Confirm claim"** / 中文 **"确认 claim"**——每次点击都确认恰好一个 Claim，
  并提交恰好一个 gitea issue（因此按钮文本用单数，匹配每次点击的语义）。
- 终态新增项，本轮不构建：驱动完整证明流（solver + Magi）的 `qatlas contrib theorem` 子命令，以及驱动
  audit-existing 的 `qatlas contrib audit`。同样遵循单数动作命名规则。

## 影响

- Python `qatlas` 包增加 `qatlas.client.contrib.claim` 模块：一个启动 localhost HTTP server、
  打开浏览器、驱动 Copilot-SDK 自定义 agent（系统提示词 = scout + enricher + issue-raiser 组合）、
  通过 QA suspend-and-wait endpoints 使用 paper 字节流、并在每次 Confirm 时提交到 gitea 的进程。
- QA WebUI 本轮**不会**增加 `/claims` 页面（ADR `0004`）。它会增加 `/theorems`（read-through，
  ADR `0002`）。
- 从 `claim` 走向终态 `theorem`-flow contrib 是增量式的——不会强迫重做 Claim flow。
