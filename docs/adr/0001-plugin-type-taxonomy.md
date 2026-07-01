# 插件类型双轴命名：`kind`（`builtin`/`external`）× `transport`（`socket`/`stdio`）

插件的 manifest 原本把插件类型编码成一个 `transport` 枚举
（`in-process-go`, `jsonrpc-ws`, `jsonrpc-stdio`），把四件互不相干的事混在了一起：来源
（一方 vs 三方）、位置（进程内 vs 独立进程）、实现语言，以及线协议。我们参考 VSCode
扩展和 Zotero 插件，把它拆成两个正交轴：

- **`kind`** — `builtin`（一方，编译进 qatlasd，以 Go 进程内运行）或
  `external`（三方，作为独立进程通过 JSON-RPC 与 host 通信）。
- **`transport`** — 只对 `external` 有意义：`socket`（插件拨入 host 的 WebSocket 端点，
  并用连接密钥认证）或 `stdio`（host 从 `spawn.command` 启动插件可执行文件，并通过
  其 stdin/stdout 跑 JSON-RPC，即 LSP/DAP 模型）。`builtin` 不需要 transport（调用就是进程内函数调用）。

从旧枚举迁移：`in-process-go → kind=builtin`；`jsonrpc-ws → kind=external,
transport=socket`；`jsonrpc-stdio → kind=external, transport=stdio`。JSON-RPC 是唯一的 RPC
方言，因此不再出现在任何名称里。

## 备选方案

- **保留单个 `transport` 枚举。** 否决：一个字段会混淆来源/位置/语言/线协议，
  名称也泄露线协议（`jsonrpc-ws`），而不是表达概念（一个拨入的 external 插件）。

## 影响

- `manifest.go`（`Manifest` struct + `Validate`）和每个 `plugins/<id>/plugin.json` 都改成
  双轴形态。`builtin`/`socket` 要求 `spawn == null`；`stdio` 要求 `spawn.command`。
- 四个一方插件（graph, rag, wiki, lean）都是 `kind=builtin`。
- `external` 的 socket/stdio transport（现有的 `plugin/rpc.go` + `hostapi`）保留给真正的三方插件——不删除。
