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
    # 一次性配好（写入平台原生 user-config 路径下的 config.yaml）
    qatlas config set server_url https://quantum-atlas.ai
    qatlas config path                                    # 看真实文件路径
    qatlas config show                                    # 看当前所有解析值（敏感字段自动遮罩）
    ```

    **不需要 token** —— 所有读接口都是公开的（Wiki 是公开仓库）。token 仅在写操作时需要，参见下面 §「贡献内容 / 上传论文」段。

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

    **2. 登录：**

    ```bash
    qatlas auth login -s <your-server>
    # 1) CLI 跟 server 要一个 8 位 user_code + 深链
    # 2) 自动开本机浏览器到 https://<your-server>/device?user_code=WDJB-MJHT
    # 3) 用 GitHub 登录后看到 Approve 表单，默认全勾所有 scope，可改名字 /
    #    过期天数 / 取消不想要的 scope → 点 Approve
    # 4) CLI 轮询拿到 token 写进 ~/.config/qatlas/hosts.yml

    # SSH 远端 / 没 DISPLAY 的机器加 --no-browser，只打印 URL，
    # 自己复制到任意有浏览器的设备（手机、自己工位…）打开即可
    # qatlas auth login --no-browser -s <your-server>

    # 验证
    qatlas auth status
    ```

    !!! tip "已经手上有 PAT 明文了？"
        浏览器自助打开 `https://<your-server>/pat` 创建一个，复制以 `qat_` 开头的明文，然后用 `--with-token` 从 stdin 写进 hosts.yml：

        ```bash
        echo qat_xxxxxxxxxxx | qatlas auth login -s <host> --with-token
        ```

        （从 stdin 读而不是 argv 是为了 secret 不进 shell history / `ps` / CI runner log——跟 `gh auth login --with-token` 同款设计。）

    **3. 上传第一篇论文：**

    ```bash
    # 上传 PDF + 元数据 JSON
    qatlas upload pdf 2501.00010v1 --pdf paper.pdf

    # 用本地 MinerU 配额解析后推回云端（先把 token 写进 yaml）
    echo <your-jwt-from-mineru.net> | qatlas config set mineru_api_token
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
    curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh
    # 或锁定版本
    curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh -s -- --version v0.2.8
    ```

    **2. 准备 .env：** 参照 [env vars 参考](reference/env-vars.md)，最小配置：

    ```bash
    QATLAS_PUBLIC_URL=https://your-domain.tld
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

## 通用配置：client YAML / server `.env`

**v0.17.0 起 client 和 server 配置完全分离**：

- **Client (`qatlas`)**: YAML，由 [`platformdirs`](https://platformdirs.readthedocs.io/) 选定位置（Linux `~/.config/qatlas/`、macOS `~/Library/Application Support/qatlas/`、Windows `%APPDATA%\qatlas\`）。**首次跑任何 `qatlas <cmd>` 自动创建模板**——不需要 `qatlas config init` 步骤。

    最小配置：

    ```yaml
    # config.yaml — auto-created on first qatlas invocation; edit directly or via `qatlas config set`
    server_url: https://quantum-atlas.ai
    # insecure: true              # 仅当远端用自签证书
    # token: qat_xxxxxxxx         # 需要写操作时
    # mineru_api_token: jwt_...   # 仅当本地跑 qatlas mineru
    ```

    或用 `qatlas config set` 维护：`qatlas config set server_url https://quantum-atlas.ai`。

- **Server (`qatlasd`)**: 三入口 CLI flag > OS env > `.env` 文件 > default。最小 `.env` 见 [reference/env-vars](reference/env-vars.md) 和模板 [`.env.example`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.env.example)。

## 找不到答案？

- 看 [操作指南](guides/index.md)（"怎么做某件事"）
- 查 [参考手册](reference/index.md)（"API 长什么样"）
- 翻 [FAQ](about/faq.md)
- 提 [GitHub issue](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues)
