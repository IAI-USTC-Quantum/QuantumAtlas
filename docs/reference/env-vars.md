# 环境变量与配置边界

> **当前内容契约更新**：固定来源 PDF 已恢复为 paper_access 开关下的鉴权字节，
> 关闭404，qa_、别名、Range、历史原件不能绕过。新内容在独立 qatlas-content 桶的
> content/ 命名空间不可变发布：source 绑定确切 PDF SHA，arXiv vN 是语义版本、
> S3VersionId 是物理版本、parse revision 是独立不可变身份；三者不可混用。
> 同 paper/SHA 可复用 source；冻结导入别名不同字节409（overwrite也不替换）。
> 解析上传必须完整支持的 Middle+Markdown，保留所有成员原名/字节，MD-only拒收；
> manifest最后核验发布且PG必需，重传新revision，不覆盖历史。旧MD/JSON/images忽略，
> 只在内容/PDF访问按需冻结旧PDF，不bulk回填、不删除旧桶；没有nightly/boot/入库自动推理。
> 下文涉及旧三桶覆盖、仅full.md+images、原PDF410/外部直链的段落保留为历史背景，
> **不作为当前操作规则**。当前API/上传/续读统一见[论文内容契约](../server/paper-content.md)
> 与[Upload API](../server/upload-api.md)；完整read视图不是原Middle JSON。

主服务 **`qatlasd` 的业务配置是 YAML-only**，不是旧 Python 包的 `.env` 模式。默认文件为 `~/.qatlas/config.yaml`，通过 `qatlasd config init` 创建、`--config` 选择；完整字段见[服务端配置](../server/server-config.md)。旧 `QATLAS_*` 业务变量、`MINERU_*`、`POSTGRES_*` 等残留会触发明确错误，不会默默覆盖 YAML。

本页只保留目前仍有用途的环境变量类别，以及从旧配置迁移的入口。环境变量属于哪个进程，必须先分清。

## 进程/工具矩阵

| 使用方 | 配置入口 | 说明 |
|---|---|---|
| 主服务 `qatlasd` | `~/.qatlas/config.yaml` 或 `--config` | OAuth、PG、S3、搜索和下载器均在 YAML；仅保留 PocketBase 启动参数等明确例外 |
| 独立 `qatlas` CLI | 由 `qatlas-cli` 管理的用户 YAML 与凭据文件 | 不安装于主仓，详见其独立仓库/CLI 文档 |
| `install-qatlasd.sh` | `--version`、`--dir` 与安装器工具变量 | 安装目标/超时，不是运行服务的设置 |
| Docker Compose | `deploy/.env` 镜像版本插值 | 只选择镜像，服务内仍挂载 YAML |
| `downloaderworker` / 旧 proxy | `DL_WORKER_*` / `DL_PROXY_*` | 独立程序的运行配置，不传进主服务 |
| Vite dev server | `web/.env.development.local` | 本地代理/HMR/调试；不是生产配置 |
| Go 集成测试 | build tag + 显式测试变量 | 不默认运行，不读取部署 `.env` |
| 文档刷新工具 | `DOCS_*` | 作用于明确的文档输出/覆盖目录，不选择安装所需 UI |

## 安装器与 Compose

| 变量 | 使用方 | 用途 |
|---|---|---|
| `QATLAS_VERSION` | 安装器 / Compose | 安装版本或镜像 tag；不设置运行中程序的版本 |
| `QATLAS_INSTALL_DIR` | 安装器 | 可写的安装目录，默认 `~/.local/bin` |
| `QATLAS_REPO` | 安装器 | GitHub owner/repo，默认官方仓库 |
| `QATLAS_INSTALL_TIMEOUT` | 安装器 | 每次下载/归档步骤时限，默认 60 秒，1..300 |
| `QATLAS_INSTALL_VERSION_TIMEOUT` | 安装器 | 候选程序 `--version` 时限，默认 10 秒，1..300 |
| `QATLAS_SEARCH_VERSION` / `QATLAS_MATCH_VERSION` / `QATLAS_RAG_VERSION` | Compose | 独立 app 镜像版本，仅由 Compose 插值 |

只在相应工具的一次调用中设置需要的变量，避免长期 export 后误传给主服务。不能用 `QATLAS_VERSION` 伪造 `qatlasd --version`，也不能用安装器 repo 覆盖主服务的 UI 下载来源；源码安装始终使用自身精确版本的官方 Release。

## Go 开发和集成测试

Go 版本由根 `go.mod` 决定；`GOCACHE`、`GOMODCACHE`、`GOPATH` 可以按开发环境设置。不要关闭 `GOSUMDB` 或用修改 tag 绕过源码校验。正常发布使用 `CGO_ENABLED=0`，`-race` 则另需本机 C 编译器和 `CGO_ENABLED=1`，与已移除的 DuckDB 无关。

| 真资源 | 编译选择 | 仍需显式提供 |
|---|---|---|
| PostgreSQL registry/corpus/usage/admin | `-tags integration` | `QATLAS_TEST_PG_DSN`，只指向可丢弃测试库 |
| Fleet/worker/归档回调 PostgreSQL | `-tags integration` | `TEST_DOWNLOADFLEET_DATABASE_URL`，只指向可丢弃测试库 |
| S3 测试桶 | `-tags integration` | `QATLAS_S3_TEST_ENDPOINT/BUCKET/ACCESS_KEY_ID/SECRET_ACCESS_KEY` |
| MinerU 真上传/转换 | `-tags integration` | `MINERU_LIVE_TEST=1` 及测试 token；会消耗额度 |
| OpenAlex/出版商真请求 | `-tags integration` | `QATLAS_TEST_LIVE=1` 等测试配置 |
| 生产协议冒烟 | `-tags e2e` | `QATLAS_SERVER_TARGETS`；可选 `QATLAS_EXPECTED_VERSION` |

普通测试中 `internal/testutil.IntegrationEnabled` 固定为 false；环境变量残留不能单独打开 internal/cmd 的真服务测试。加 tag 后仍需原有目标/开关。S3 真测试和生产冒烟还分别由自己的文件 build tag 隔离。

开发时仍应清除真实目标和凭据、使用临时 HOME，避免误读配置或传播 token。只检查集成路径是否可编译，可以使用 `go test -tags=integration ./internal/... ./cmd/... -run '^$'`；完整说明见[贡献指南](../contributing.md)。不要把以上真实测试接入普通 PR 的必跑检查。

## Vite 与下载节点

Vite 使用 `VITE_DEV_API_TARGET`、`VITE_DEV_FAKE_AUTH`、`VITE_DEV_ALLOWED_HOSTS` 及可选测试 PAT；语义和风险见 [`web/README.md`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/web/README.md)。`VITE_` 是客户端可暴露前缀，不能把真实凭据放入构建环境；fake-auth 不授予 API 权限。仅连接本地或明确获准的测试实例。

下载节点的完整环境契约见[worker 配置](../server/downloader-workers.md)。这些独立进程仍使用环境配置，不等于主服务恢复了 dotenv 模式。

## Server：论文访问开关（self-hosted 可选） { #server-论文访问开关-self-hosted-可选 }

当前使用 YAML 的 `paper_access.enabled`，默认 false。启用前确认版权、访问权限与外部服务预算；PDF/content/raw原件/图片随 paper_access 开关统一鉴权交付；已存PDF不需解析token，关闭404。见[版权与资源访问](../about/license-and-attribution.md)。

### Server-side MinerU { #server-side-mineru }

配置位于 `paper_access.mineru`：`api_tokens` 是 YAML 列表，模型、语言、OCR、并发与超时也在该段。不是进程 `MINERU_*` 环境变量，更不是客户端的用户配置。具体字段见[服务端配置](../server/server-config.md#server-side-mineru)。

## Client：qatlas 配置 { #client-qatlas-配置yaml-onlyv0170 }

客户端由 [qatlas-cli](https://github.com/IAI-USTC-Quantum/qatlas-cli) 独立维护，通过 `qatlas config path/show` 查看实际配置。其 `server_url` 表示“我要连接谁”，与服务端 YAML 的 `public_url`（“我的公开入口”）不同；其认证与本地 MinerU 配置也不共享主服务配置文件。参见[CLI 配置参考](../client/cli-qatlas.md#qatlas-config)。

旧 `quantum-atlas 0.21.0` 仅是退役通知，不会安装新客户端，也不提供这些旧环境变量的兼容层。历史例子留在不可变 tag，不再作为 main 的可执行开发说明。
