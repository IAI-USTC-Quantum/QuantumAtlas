# FAQ

> 答案按类聚合。找不到的话提 [issue](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues) 或 [start a discussion](https://github.com/IAI-USTC-Quantum/QuantumAtlas/discussions)。

## 关于项目

??? question "QuantumAtlas 跟 X 有什么区别？"

    - **跟 arXiv-sanity 比**：他们做的是 paper recommendation，我们做的是 paper 收集 + 注册 + 多路检索的完整链路
    - **跟传统 wiki / 笔记比**：我们的论文资产是结构化 + 强类型 + PostgreSQL registry 登记，不是纯叙述
    - **跟 Google Scholar 比**：我们是开源自部署，本地 catalog 可 SQL 直查，还能接语义向量检索
    - **跟 Notion / Obsidian 比**：我们是开源 + 自部署 + 量子算法专用

??? question "适合什么人 / 什么场景？"

    - 量子算法研究组：长期沉淀算法知识、跟踪文献、共享实现
    - 教学：把课程涉及的算法 / 论文整理成有结构的 wiki
    - 量子软件团队：建立算法 + 原语库，新成员快速 onboarding
    - 个人研究：用 LLM 辅助提炼论文 + 长期记笔记

    **不适合**：纯实验性 / 一次性的小研究、不需要长期沉淀的项目。

??? question "项目处于什么阶段？"

    Alpha。主线打通，但 still moving fast。version `0.2.x` 里随时可能有 schema 变动（会在 CHANGELOG.md 标 BREAKING）。

??? question "维护者是谁？"

    [IAI-USTC-Quantum](https://github.com/IAI-USTC-Quantum) 团队。具体协作者见 [credits](credits.md)。

## 安装 / 部署

??? question "go install 和预编译程序的 UI 有区别吗？"

    内容与就绪后的服务行为相同。预编译程序在构建时内嵌完整 UI；`go install ...@vX.Y.Z` 仅编译源码，第一次 `serve` 自动从**相同版本** GitHub Release 下载 UI ZIP 与 SHA256 清单，校验后缓存。普通安装无需 Node/Sphinx，也无需选择资源。新分发格式从未来新版本启用，不重发旧 tag，见[安装说明](../server/install.md)。

??? question "首次下载失败、缓存损坏或 dev 版本怎么办？"

    程序明确报错，不使用 latest 或别的版本。确认该版本 Release 与 UI 附件已经公开，网络恢复后重试。已损坏的缓存不会静默替换：仅移走报错中指定的版本缓存目录，再启动自动下载。`dev`/Go 伪版本没有对应 UI，请安装带资源的正式 tag；开发者则完整构建 UI 后用 `-tags embedui` 编译。缓存位置为系统用户缓存目录的 `qatlas/ui/v<version>`，升级会使用新版本目录，旧缓存不影响新版本。

??? question "client 必须装 server 吗？"

    不必。**client 是独立的 [PyPI 包 `qatlas-cli`](https://pypi.org/project/qatlas-cli/)**，由 [qatlas-cli 仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli)独立维护和发版；纯 client 用户只需要在 `~/.config/qatlas/config.yaml` 设 `server_url:` 指向远端 server（如 `https://quantum-atlas.ai`）。安装 QuantumAtlas 主仓不会提供 `qatlas`。

??? question "quantum-atlas 退役后怎么迁移？会自动安装 qatlas-cli 吗？"

    **不会自动安装。**`quantum-atlas 0.21.0` 是仅含元数据的最终迁移版，不含 `qatlas` Python 模块、parser 库或 console entry，没有运行时依赖，也没有指向 `qatlas-cli` 的转发依赖。

    按[迁移指南](../getting-started.md#migrate-quantum-atlas)选择原安装器（uv tool / pipx / pip），先卸载 `quantum-atlas`，再安装 `qatlas-cli`。如果两者已经共存，卸载旧包可能移除共享的模块或命令路径，务必在**卸载之后重装 `qatlas-cli`**。

    **不要删除 `~/.config/qatlas`**（或对应平台的配置目录）；原来的配置和凭据继续保留。

??? question "Windows 能跑 server 吗？"

    理论可以——Go binary 可以 cross-compile 出 Windows 版（release pipeline 当前没出 Windows artifact，需要手 build）。但 systemd / Caddy 这套生态都是 Linux 一等，Windows 不建议生产。

    **WSL2 可以**——qatlasd 在 WSL2 下直接跑即可；对外暴露时注意 Windows 防火墙与 portproxy 的常规配置。

??? question "macOS 能跑 server 吗？"

    Apple Silicon 可以——release 出 `darwin-arm64` binary。Intel Mac 没出预编 binary（GitHub Actions `macos-13` runner 排队 10–40 分钟），用 `go install github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@vX.Y.Z`（换成已公开且带 UI 包的 tag） 自编。但 launchd 配置不如 systemd 成熟（kardianos/service 库默认配置在 macOS 跑通过没充分测试）。

??? question "我有 ARM VPS（aarch64），能装吗？"

    可以。release artifact 出 `linux-arm64`，`install-qatlasd.sh` 自动检测。

??? question "Docker 镜像有吗？"

    发布流程提供 `ghcr.io/iai-ustc-quantum/qatlasd:<tag>`（当前承诺 `linux/amd64`），内嵌完整 UI。选择已通过验证的公开版本，不把 RC 当 latest。按[容器部署文档](../server/docker.md)挂载 YAML 配置与持久数据；不要用旧应用环境变量代替配置文件。

## 客户端使用

??? question "为什么 `qatlas contrib pdf` 要求带版本（v1）？"

    arXiv 同一篇 paper 有多个版本（v1 / v2 / ...），内容可能不同。server 端按 `<id>v<n>` 寻址对象——不带版本不知道是哪一版。**这是有意的强约束**。

??? question "MinerU 解析超时 / 失败怎么办？"

    `qatlas contrib mineru` 默认超时 30 分钟。大论文（>50 页含很多图表）需要更长：

    ```bash
    export MINERU_TIMEOUT=3600
    qatlas contrib mineru 2501.00010v1
    ```

    如果 MinerU API 本身返回失败（quota 满 / 限流 / 服务挂了），看 `qatlas-pdf` 桶里那个 PDF 是不是格式有问题——某些 scanned-only PDF 需要 `MINERU_IS_OCR=true`。

??? question "上传同样的 PDF 第二次返回 200 而不是 201，是不是失败了？"

    **不是**——200 表示**字节相同，server 端已经有了，零写入**（sha256 dedup）。是幂等成功。看 `unchanged: true` 字段。

??? question "我想强制 server 重抓 PDF / 重解析？"

    ```bash
    qatlas ingest 2501.00010 --parser mineru --force-fetch --force-parse
    ```

    或 `qatlas contrib pdf <id> --pdf new.pdf --overwrite` —— **旧版本会保留在 S3 versioning 里**（可恢复）。

## 鉴权

??? question "我的 PAT 显示 `qat_AB********`，丢了能恢复吗？"

    **不能**——server 只存 bcrypt hash，明文只在创建时显示一次。撤销 + 重建新 PAT。

??? question "session token 14d 到期需要重新登录？"

    是。如果不想频繁重登，**用 PAT**——可以设最长 365 天，CI / 长跑场景标配。

??? question "我有 PAT 但调 `/api/pat` 仍 403？"

    设计如此。PAT **不能**操作 `/api/pat`（防止 leaked PAT 自我复制）。用 session token 调（浏览器登录后 SPA 内部已自动持有；如需用 SDK / CLI 调用 `/api/pat`，请在浏览器 SPA 内操作）。

??? question "PAT 在某台边缘建的，在另一台用不了？"

    对。多边缘各自独立 PocketBase，**用户和 PAT 不跨节点**。需要为每条线路各建 PAT。

## Registry / 搜索

??? question "上传的论文多久能被搜到？"

    立刻。upload / ingest 的写路径是 write-through：对象落桶后同事务登记
    `papers` + `paper_assets`，`POST /api/search` 的 catalog provider 下一轮查询即可命中。
    PostgreSQL 暂时不可用时上传仍成功（`X-Catalog-Sync: deferred`），事后跑
    `qatlasd papers sync --full --from-rustfs` 从对象存储重建即可。

??? question "搜索结果里 catalog / arxiv / openalex 有什么区别？"

    `POST /api/search` 把查询 fan-out 到 `QATLAS_SEARCH_PROVIDERS` 配置的 provider：
    **catalog** 查本地 registry（含资产状态）；**arxiv / openalex** 是上游在线查询。
    同一个 query 一次拿全，不需要逐源跑。语义向量检索由独立的 qatlas-search /
    qatlas-rag 微服务提供，不经由 `/api/search`。

## 部署 / 运维

??? question "qatlasd 默认监听 127.0.0.1，怎么对外？"

    前面挂反代（Caddy 推荐）做 TLS 终结。**不要**直接 `--http=0.0.0.0:4200` 暴露——会绕过反代的 TLS / 鉴权 / Host header preserve。详见 [反向代理](../server/reverse-proxy.md)。

??? question "pb_data 多大？"

    用户少（<100）时几 MB；几万 PAT 时百 MB 级。不会非常大——大头数据（PDF / Markdown）在 RustFS，不在 pb_data。

??? question "RustFS 跟 MinIO 兼容吗？"

    兼容 S3 API，所以兼容 MinIO 客户端（mc）+ minio-go SDK。我们用的就是 minio-go。如果想换成 MinIO / AWS S3 / Cloudflare R2，把 `QATLAS_S3_ENDPOINT` 换掉即可（注意 endpoint 必须含 scheme）。

??? question "升级时需要停 service 吗？"

    严格来说不必——`systemctl restart` 大约 1-5 秒 downtime。如果在意：

    - 跨大版本（migration 改 schema）：**先备份 pb_data**
    - 多边缘：**rolling restart**（一台一台，DNS 不切的话用户感知约 0）

??? question "PostgreSQL 暂时没准备好，能先跑起来吗？"

    可以。`QATLAS_POSTGRES_DSN` 不配 → server 启动正常，registry 端点（`/api/papers/stats` /
    needs-mineru）返回 `available:false`，上传仍写对象存储并带 `X-Catalog-Sync: deferred`。
    配好 DSN 后跑 `qatlasd papers sync --full --from-rustfs` 重建 registry 即可。

## 协议 / 法律

??? question "Apache-2.0 协议允许我把 QuantumAtlas 嵌进我的商业产品吗？"

    可以。Apache-2.0 是工业界最主流的 permissive 开源协议之一（Kubernetes、Docker、Terraform、Prometheus 等都用它）。需要保留 LICENSE / NOTICE 文件并显著标注改动，且额外获得 contributor 的专利使用权。详见 [LICENSE](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/LICENSE) 全文。

??? question "上传的 PDF 谁的版权？"

    谁上传谁负责。**不要上传你没有合法权利分享的 PDF**——尤其是出版社 paywall 后的版本。arXiv preprint 通常没问题。

## 开发 / 贡献

??? question "想加新功能，怎么开始？"

    1. 先开 issue 讨论方向
    2. 同意后 fork + 写代码
    3. 跑测试 + lint
    4. 提 PR，使用 Conventional Commits

    详见 [贡献指南](../contributing.md)。

??? question "Python 版本要求？"

    Go 开发和运行不需要 Python。Sphinx/MkDocs 与少量 CI 辅助脚本需要独立的 Python 工具环境（CI 使用 3.12），不恢复已退役的 Python 包。独立客户端要求见 qatlas-cli 仓库。

??? question "Go 版本？"

    根 `go.mod` 是准绳，当前要求 Go 1.26.2；CI 从该文件选择工具链。不需要 Pixi 或过时的 DuckDB/C++ 依赖。

??? question "前端用什么？"

    React + Vite + TanStack Router + Tailwind v4 + shadcn/ui。源在 `web/`。

??? question "可以只贡献文档吗？"

    可以。`docs/` 是 MkDocs/RTD 站，`docsite/` 是随 UI 分发的 Sphinx 使用/开发站；两者都由 CI 验证，构建输出不提交。详见[贡献指南 / docs](../contributing.md#docs)。
