# FAQ

> 答案按类聚合。找不到的话提 [issue](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues) 或 [start a discussion](https://github.com/IAI-USTC-Quantum/QuantumAtlas/discussions)。

## 关于项目

??? question "QuantumAtlas 跟 X 有什么区别？"

    - **跟 arXiv-sanity 比**：他们做的是 paper recommendation，我们做的是 paper → wiki → 图谱 → 可执行代码的完整链路
    - **跟传统 wiki 比**：我们的 wiki 是结构化 + 强类型 + Neo4j 同步，不是纯叙述
    - **跟 Qiskit Aqua / PennyLane libraries 比**：他们是 algorithm 实现库，我们是 algorithm 知识库 + 自动生成实现
    - **跟 Notion / Obsidian 比**：我们是开源 + 自部署 + 量子算法专用 + 含 LLM extractor

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

??? question "client 必须装 server 吗？"

    不必。**client 是独立的 PyPI 包**（`quantum-atlas`）；纯 client 用户只需要 `QATLAS_SERVER_URL` 指向远端 server（如 `https://quantum-atlas.ai`）。

??? question "Windows 能跑 server 吗？"

    理论可以——Go binary 可以 cross-compile 出 Windows 版（release pipeline 当前没出 Windows artifact，需要手 build）。但 systemd / Caddy 这套生态都是 Linux 一等，Windows 不建议生产。

    **WSL2 可以**——按 [Neo4j 部署](../deployment/neo4j.md) 那段 WSL2 注意事项配 portproxy 即可。

??? question "macOS 能跑 server 吗？"

    可以。binary `darwin-amd64` / `darwin-arm64` 都出。但 launchd 配置不如 systemd 成熟（kardianos/service 库默认配置在 macOS 跑通过没充分测试）。

??? question "我有 ARM VPS（aarch64），能装吗？"

    可以。release artifact 出 `linux-arm64`，`install-server.sh` 自动检测。

??? question "Docker 镜像有吗？"

    当前**没有官方 Docker 镜像**。如果你要做，参考：

    ```dockerfile
    FROM alpine:3
    COPY qatlasd-linux-amd64 /usr/local/bin/qatlasd
    RUN chmod +x /usr/local/bin/qatlasd
    EXPOSE 4200
    CMD ["qatlasd", "serve", "--http=0.0.0.0:4200"]
    ```

    pb_data / wiki / raw 用 volume mount 进容器，`.env` 用 `--env-file` 注入。pb_data 路径用 `QATLAS_PB_DATA_DIR` 控制。

## 客户端使用

??? question "为什么 `qatlas upload pdf` 要求带版本（v1）？"

    arXiv 同一篇 paper 有多个版本（v1 / v2 / ...），内容可能不同。server 端按 `<id>v<n>` 寻址对象——不带版本不知道是哪一版。**这是有意的强约束**。

??? question "MinerU 解析超时 / 失败怎么办？"

    `qatlas mineru` 默认超时 30 分钟。大论文（>50 页含很多图表）需要更长：

    ```bash
    export MINERU_TIMEOUT=3600
    qatlas mineru 2501.00010v1
    ```

    如果 MinerU API 本身返回失败（quota 满 / 限流 / 服务挂了），看 `/share/...` 的 PDF 文件是不是格式有问题——某些 scanned-only PDF 需要 `MINERU_IS_OCR=true`。

??? question "上传同样的 PDF 第二次返回 200 而不是 201，是不是失败了？"

    **不是**——200 表示**字节相同，server 端已经有了，零写入**（sha256 dedup）。是幂等成功。看 `unchanged: true` 字段。

??? question "我想强制 server 重抓 PDF / 重解析？"

    ```bash
    qatlas ingest 2501.00010 --parser mineru --force-fetch --force-parse
    ```

    或 `qatlas upload pdf <id> --pdf new.pdf --overwrite` —— **旧版本会保留在 S3 versioning 里**（可恢复）。

## 鉴权

??? question "我的 PAT 显示 `qat_AB********`，丢了能恢复吗？"

    **不能**——server 只存 bcrypt hash，明文只在创建时显示一次。撤销 + 重建新 PAT。

??? question "session token 14d 到期需要重新登录？"

    是。如果不想频繁重登，**用 PAT**——可以设最长 365 天，CI / 长跑场景标配。

??? question "我有 PAT 但调 `/api/pat` 仍 403？"

    设计如此。PAT **不能**操作 `/api/pat`（防止 leaked PAT 自我复制）。用 session token（浏览器登录后 `/token` 拷）。

??? question "PAT 在 RackNerd 上建的，在阿里云用不了？"

    对。两台边缘各自独立 PocketBase，**用户和 PAT 不跨节点**。需要为每条线路各建 PAT。详见 [多边缘](../concepts/multi-edge.md)。

## Wiki / Neo4j

??? question "Wiki 改了之后 Neo4j 多久会更新？"

    不会自动更新。

    - 触发 `POST /api/wiki/sync/pull` → server 端 git fast-forward → in-memory cache 刷新
    - Neo4j 图谱的派生是**服务端职责**，由 Go `qatlasd` 基于 canonical Wiki 重建；Python 客户端不直连 Neo4j

    或者你把 sync 加进 GitHub Action：每次 Wiki repo PR 合并触发 server 端 sync。

??? question "Wiki 页面被删了 Neo4j 节点怎么办？"

    sync 会自动删 orphan 节点（基于 page id）。但如果你 rename 了 page id，sync 把它当作 "删旧 + 加新"——会断历史关系。**避免 rename id**。

??? question "lint 报 W003 孤儿页面，必须修吗？"

    W003 是 INFO 级别，不强制。但孤儿页面通常意味着"没人引用它"——要么补 `related: [...]`，要么这页本来就独立（手册 / 教程性质），可以 ignore。

## 部署 / 运维

??? question "qatlasd 默认监听 127.0.0.1，怎么对外？"

    前面挂反代（Caddy 推荐）做 TLS 终结。**不要**直接 `--http=0.0.0.0:4200` 暴露——会绕过反代的 TLS / 鉴权 / Host header preserve。详见 [反向代理](../deployment/reverse-proxy.md)。

??? question "pb_data 多大？"

    用户少（<100）时几 MB；几万 PAT + share record 时百 MB 级。不会非常大——大头数据（PDF / Markdown）在 RustFS，不在 pb_data。

??? question "RustFS 跟 MinIO 兼容吗？"

    兼容 S3 API，所以兼容 MinIO 客户端（mc）+ minio-go SDK。我们用的就是 minio-go。如果想换成 MinIO / AWS S3 / Cloudflare R2，把 `QATLAS_S3_ENDPOINT` 换掉即可（注意 endpoint 必须含 scheme）。

??? question "升级时需要停 service 吗？"

    严格来说不必——`systemctl restart` 大约 1-5 秒 downtime。如果在意：

    - 跨大版本（migration 改 schema）：**先备份 pb_data**
    - 多边缘：**rolling restart**（一台一台，DNS 不切的话用户感知约 0）

??? question "Neo4j 我不用图谱功能，能不装吗？"

    可以。`NEO4J_URI` 不配 → server 启动正常，graph endpoint 返回 `{"error":...}`，`/api/health` 报 `not_configured` 不下拉等级。SPA 的 Graph tab 是空的。

## 协议 / 法律

??? question "MIT 协议允许我把 QuantumAtlas 嵌进我的商业产品吗？"

    可以。MIT 是最宽松的开源协议之一。需要保留 LICENSE 文件即可。

??? question "Wiki 内容（论文摘要）的版权？"

    我们的 Wiki repo MIT；但论文本身是各家出版社 / arXiv 的版权（一般是 arXiv non-exclusive license）。Wiki paper 页面**应该是用自己的话总结 + 引用关键数据**，而不是 verbatim copy abstract。

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

    3.11+。Type hints 是项目主线，老 Python 不行。

??? question "Go 版本？"

    1.23+。CI 跑 1.23。

??? question "前端用什么？"

    React + Vite + TanStack Router + Tailwind v4 + shadcn/ui。源在 `web/`。

??? question "可以只贡献文档吗？"

    完全可以。文档是 mkdocs-material，源在 `docs/`，提 PR 到 main，RTD 自动 build preview。详见 [贡献指南 / docs](../contributing.md#docs)。
