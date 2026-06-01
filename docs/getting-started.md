# 入门指南

按你的角色选一条路径开始。每条路径都是「装好工具 → 跑一个最小可工作的例子 → 知道下一步往哪走」。

=== ":material-account-search: 我是研究者"

    我想用 QuantumAtlas 查论文 / 看 Wiki / 浏览图谱。**不需要装 server**。

    **1. 装 client：**

    ```bash
    # 推荐：uv 全局工具（升级方便）
    uv tool install quantum-atlas
    # 或 pipx
    pipx install quantum-atlas
    # 或 pip
    pip install quantum-atlas

    qatlas --help
    ```

    **2. 指向远端 server：**

    ```bash
    export QATLAS_SERVER_URL=https://quantum-atlas.ai
    ```

    或写在 `~/.config/qatlas/.env`（推荐永久配置）。**不需要 token** —— 所有读接口都是公开的（Wiki 是公开仓库）。

    **3. 跑起来：**

    ```bash
    # 列出最近摄入的论文
    qatlas wiki list --type source

    # 查一个具体页面（algorithm / primitive / paper）
    qatlas wiki show prim-qft

    # 模糊搜索
    qatlas wiki search "quantum fourier"
    ```

    **下一步：**

    - [写作 Wiki 页面](guides/write-wiki-pages.md) — 想贡献内容了？
    - [浏览图谱](guides/circuit-toolchain.md#explore-graph) — 用图查询关系
    - [reference/CLI](reference/cli-qatlas.md) — 看全 CLI 命令

=== ":material-upload: 我是贡献者"

    我想上传论文 / 写 Wiki / 跑 MinerU。**需要装 client + 申请一个 PAT**。

    **1. 装 client（同上）**

    **2. 拿一个 PAT**：浏览器打开 `https://<your-server>/pat` → 用 GitHub 登录 → 创建 PAT，**勾上 `papers:write` 和 `shares:write` 两个 scope** → 复制以 `qat_` 开头的明文。

    !!! warning "PAT 明文只会显示一次"
        复制后立即存好。丢了就只能 revoke + 重建。

    **3. 把 PAT 存到本地：**

    ```bash
    # 推荐：交互式存到 ~/.config/qatlas/hosts.yml
    qatlas auth login -H quantum-atlas.ai
    # 然后粘贴 PAT 明文

    # 验证
    qatlas auth status
    ```

    **4. 上传第一篇论文：**

    ```bash
    # 上传 PDF + 元数据 JSON
    qatlas upload pdf 2501.00010v1 --pdf paper.pdf --metadata meta.json

    # 用本地 MinerU 配额解析后推回云端
    export MINERU_API_TOKEN=<your-token>
    qatlas mineru 2501.00010v1 --push-pdf
    ```

    **下一步：**

    - [上传论文资产](guides/upload-assets.md) — sha256 dedup、冲突处理、`--overwrite`
    - [用 MinerU 解析](guides/parse-with-mineru.md) — 单篇 / 队列模式 / 并发协作
    - [写 Wiki 页面](guides/write-wiki-pages.md) — 把论文沉淀成 Concept / Paper / Algo
    - [管理 PAT](guides/manage-credentials.md) — 撤销、轮换、scope 升降

=== ":material-server-network: 我是运维者"

    我想部署 / 维护 qatlasd。

    **1. 装 binary：**

    ```bash
    # 自动下载最新 release 到 ~/.local/bin/
    curl -fsSL https://quantum-atlas.ai/install-server.sh | sh
    # 或锁定版本
    curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- --version v0.2.8
    ```

    **2. 准备 .env：** 参照 [env vars 参考](reference/env-vars.md)，最小配置：

    ```bash
    QATLAS_SERVER_URL=https://your-domain.tld
    NEO4J_URI=bolt://localhost:7687
    NEO4J_USER=neo4j
    NEO4J_PASSWORD=<set-this>
    GITHUB_CLIENT_ID=<from-github-oauth-app>
    GITHUB_CLIENT_SECRET=<from-github-oauth-app>
    ```

    **3. 注册成 systemd 服务：**

    ```bash
    # 交互式（会让你确认 mode + .env 路径 + 渲染 unit 预览）
    qatlasd service install

    # CI / 全自动
    sudo qatlasd service install --mode system \
        --dotenv-path /etc/quantum-atlas/.env --force
    ```

    **4. 验证：**

    ```bash
    curl http://127.0.0.1:4200/api/health
    # 应该看到 {"code":200,"message":"API is healthy.","data":{...}}
    ```

    **下一步：**

    - [部署运维总览](deployment/index.md) — 反代、TLS、OAuth、Neo4j、RustFS 全过完一遍
    - [GitHub OAuth 接入](deployment/github-oauth.md)
    - [反向代理模板](deployment/reverse-proxy.md) — Caddy 和 nginx 配置
    - [健康检查 + 监控](deployment/health-and-monitoring.md)

=== ":material-code-braces: 我是开发者"

    我想本地起 stack、改代码、跑测试、提 PR。

    **1. clone + 装依赖：**

    ```bash
    git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas.git
    cd QuantumAtlas

    # 一次性同步 Python + npm + 前端 build + Go build
    pixi run build
    ```

    **2. 跑一个不依赖远端的 demo：**

    ```bash
    uv sync
    uv run --script examples/demo_pipeline.py \
        --algorithm qft --backend qiskit --save-code
    ```

    这个 demo 不需要 LLM API key 也不需要 Neo4j。完整走完「设计 → 生成代码 → 验证 → 资源估计」主流程。

    **3. 起本地 Web 服务：**

    ```bash
    cp .env.example .env
    # 编辑 .env：填 NEO4J_* 指向你自己起的 Neo4j

    ./build/qatlasd serve --http=0.0.0.0:4200
    ```

    访问：

    | 入口 | URL |
    |---|---|
    | SPA | <http://localhost:4200> |
    | PocketBase admin UI | <http://localhost:4200/_/> |
    | PAT 管理 | <http://localhost:4200/pat> |
    | API 健康 | <http://localhost:4200/api/health> |

    **4. 跑测试：**

    ```bash
    # Python 测试
    uv run pytest

    # Go 测试（必须通过 pixi 跑：cgo + 工具链都在 pixi env 里）
    pixi run test-go

    # 前端 build + type check
    cd web && npm run build
    ```

    **下一步：**

    - [贡献指南](contributing.md) — 仓库结构、Conventional Commits、release 流程
    - [架构概览](concepts/index.md) — 理解代码组织前先看
    - [参考手册](reference/index.md)

---

## 通用配置：.env 文件

QuantumAtlas client 和 server 共享一份 `.env`，按角色取不同字段。**纯 client 最小配置**：

```bash
QATLAS_SERVER_URL=https://quantum-atlas.ai
# QATLAS_INSECURE=1                  # 仅当远端用自签证书
# QATLAS_TOKEN=<paste-pat-here>      # 需要写操作时
# MINERU_API_TOKEN=<your-token>      # 仅当本地跑 qatlas mineru
```

详细字段语义见 [reference/env-vars](reference/env-vars.md) 和模板 [`.env.example`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.env.example)。

## 找不到答案？

- 看 [操作指南](guides/index.md)（"怎么做某件事"）
- 查 [参考手册](reference/index.md)（"API 长什么样"）
- 翻 [FAQ](about/faq.md)
- 提 [GitHub issue](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues)
