# 入门指南

按你的角色选一条路径开始。每条路径都是「装好工具 → 跑一个最小可工作的例子 → 知道下一步往哪走」。

客户端由 [qatlas-cli 独立仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli)维护和发版，安装包为 [PyPI `qatlas-cli`](https://pypi.org/project/qatlas-cli/)。安装本仓不会提供 `qatlas` 命令；已装过旧包 `quantum-atlas` 的用户请先完成[迁移](#migrate-quantum-atlas)。

=== ":material-account-search: 我是研究者"

    我想用 QuantumAtlas 查论文 / 拉论文资产。**不需要装 server**。

    **1. 装 client：**

    ```bash
    # 推荐：uv 全局工具（升级方便）
    uv tool install qatlas-cli
    # 或 pipx
    pipx install qatlas-cli
    # 或 pip
    pip install qatlas-cli

    qatlas --help
    ```

    **2. 指向远端 server：**

    ```bash
    # 一次性配好（写入平台原生 user-config 路径下的 config.yaml）
    qatlas config set server_url https://quantum-atlas.ai
    qatlas config path                                    # 看真实文件路径
    qatlas config show                                    # 看当前所有解析值（敏感字段自动遮罩）
    ```

    **3. 跑起来：**

    论文数据不匿名可读——先在浏览器里用 GitHub 登录 server，然后：

    ```bash
    # OAuth device-code 登录（拿到 papers:read PAT 写进 hosts.yml）
    qatlas auth login -s <your-server>

    # 拉一篇论文的 MinerU markdown（server 缓存未命中会自动 silent fetch + 转换）
    # PDF 交付已停用；图片用 get images 显式获取
    qatlas paper get markdown 2501.00010v1 -o paper.md
    qatlas paper get images quant-ph/9508027 -o shor-images.zip
    ```

    多 provider 搜索（本地 catalog + arXiv + OpenAlex，可选 Qdrant 语义检索）
    走 SPA 的搜索页，或直接 `POST /api/search`（`papers:read` scope）。

    **下一步：**

    - [拉取论文资产](client/cli-qatlas.md#qatlas-paper) — `qatlas paper` 的 ID 形态与 LRO 行为
    - [CLI 参考](client/cli-qatlas.md) — 看全 CLI 命令

=== ":material-upload: 我是贡献者"

    我想上传论文 / 跑 MinerU。**需要装 client + 申请一个 PAT**。

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
    qatlas contrib pdf 2501.00010v1 --pdf paper.pdf

    # 用本地 MinerU 配额解析后推回云端（先把 token 写进 yaml）
    echo <your-jwt-from-mineru.net> | qatlas config set mineru_api_token
    qatlas contrib mineru 2501.00010v1 --push-pdf
    ```

    **下一步：**

    - [上传论文资产](client/upload-assets.md) — sha256 dedup、冲突处理、`--overwrite`
    - [用 MinerU 解析](client/parse-with-mineru.md) — 单篇 / 队列模式 / 并发协作
    - [管理 PAT](client/manage-credentials.md) — 撤销、轮换、scope 升降

=== ":material-server-network: 我是运维者"

    我想部署 / 维护 qatlasd。

    **1. 装 binary：**

    ```bash
    TAG=vX.Y.Z # 替换为采用新格式的已公开版本；不是让你重装旧v0.34.0
    curl -fL --proto '=https' --proto-redir '=https' \
      "https://raw.githubusercontent.com/IAI-USTC-Quantum/QuantumAtlas/$TAG/cmd/qatlasd/install-qatlasd.sh" \
      -o install-qatlasd.sh
    # 审阅脚本后，从同一个版本安装
    sh install-qatlasd.sh --version "$TAG"
    # 或仅需Go（无Node/Sphinx）：
    # go install "github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@$TAG"
    ```

    预编译程序已内嵌完整 UI，首次运行无需联网补资源；`go install` 首次 `serve` 自动获取精确版本 Release 的 UI、校验并缓存，后续离线复用。升级自动切换版本缓存，失败不会回退 latest。旧服务的安装脚本不识别新格式，迁移时必须从同一新 tag 取脚本。详见[安装说明](server/install.md)。

    **2. 准备 YAML：**

    ```bash
    qatlasd config init # ~/.qatlas/config.yaml，不覆盖已有配置
    # 按服务器需要编辑 postgres_dsn、GitHub OAuth、对象存储等字段
    qatlasd config show
    ```

    服务端拒绝旧的应用配置环境变量，完整字段见[配置参考](server/server-config.md)。不需要手动选择或配置 UI 资源。

    **3. 注册成 systemd 服务：**

    ```bash
    # 交互式（确认 mode + YAML 路径 + unit 预览）
    qatlasd service install

    # CI / 全自动
    sudo qatlasd service install --mode system \
        --config "$HOME/.qatlas/config.yaml" --force
    ```

    **4. 验证：**

    ```bash
    curl http://127.0.0.1:4200/api/health
    # 应该看到 {"code":200,"message":"API is healthy.","data":{...}}
    ```

    **下一步：**

    - [Go 服务端总览](server/index.md) — 反代、TLS、OAuth、PostgreSQL、RustFS 全过完一遍
    - [GitHub OAuth 接入](server/github-oauth.md)
    - [反向代理模板](server/reverse-proxy.md) — Caddy 和 nginx 配置
    - [健康检查 + 监控](server/health-and-monitoring.md)

=== ":material-code-braces: 我是开发者"

    我想本地起 stack、改代码、跑测试、提 PR。

    **1. clone + 装依赖：**

    ```bash
    git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas.git
    cd QuantumAtlas

    # 源码编译与测试不需要前端产物
    CGO_ENABLED=0 go build -o build/qatlasd ./cmd/qatlasd
    # 要运行本地dev UI：先按贡献指南完成Sphinx两站+npm完整构建，再
    CGO_ENABLED=0 go build -tags embedui -o build/qatlasd ./cmd/qatlasd
    ```

    主仓的单元、部署结构和冒烟 fixture 测试使用 Go，不再需要 pytest。`pyproject.toml` 只保留 Python 辅助脚本的开发工具；Sphinx/MkDocs 的文档依赖仍独立保留。这不会安装 `qatlas`；需要 CLI 联调时，按上面的独立客户端安装步骤操作。CLI 自身的代码修改与测试请到 [qatlas-cli 仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli)。

    **2. 起本地 Web 服务：**

    ```bash
    # 完整UI构建命令见「贡献指南 → 完整 UI 构建」，只提交源码
    ./build/qatlasd config init
    # 编辑 ~/.qatlas/config.yaml；例如postgres_dsn（只指向测试数据库）
    ./build/qatlasd serve --http=127.0.0.1:4200
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
    # 部署结构与本地 HTTP fixture：离线，不启动容器/业务服务
    go test ./tests/...

    # 完整 Go 测试（包含上面的 tests/，生产 e2e 默认不编译）
    pixi run test-go
    # 或：CGO_ENABLED=0 go test ./internal/... ./cmd/... ./web ./tests/...

    # 前端 build + type check
    (cd web && npm run build)
    ```

    **下一步：**

    - [贡献指南](contributing.md) — 仓库结构、Conventional Commits、release 流程
    - [架构概览](concepts/index.md) — 理解代码组织前先看
    - [参考](reference/index.md)

---

## 从旧包 quantum-atlas 迁移 { #migrate-quantum-atlas }

`quantum-atlas 0.21.0` 是仅含元数据的最终迁移版：没有 `qatlas` Python 模块、parser 库或 console entry，没有运行时依赖，也不会自动安装 `qatlas-cli`。升级旧包不等于安装新 CLI；旧包残留的 Python helpers 已退役，并非主仓继续提供的库 API。

**选择最初安装旧包的工具，只执行对应的一组命令**。pip 用户需先激活原来的虚拟环境（如有），不要跨安装器混用：

=== "uv tool"

    ```bash
    uv tool uninstall quantum-atlas
    uv tool install qatlas-cli
    ```

=== "pipx"

    ```bash
    pipx uninstall quantum-atlas
    pipx install qatlas-cli
    ```

=== "pip"

    ```bash
    pip uninstall quantum-atlas
    pip install qatlas-cli
    ```

!!! warning "两包已共存时，卸载之后重装新包"
    旧发行版可能与 `qatlas-cli` 共享模块或命令路径；卸载 `quantum-atlas` 时可能把它们一起移除。即使新包显示「已安装」，也必须在**旧包卸载之后**用对应安装器重装：

    ```bash
    # 三选一，与原安装器一致
    uv tool install --reinstall qatlas-cli
    # 或
    pipx reinstall qatlas-cli
    # 或
    pip install --force-reinstall qatlas-cli
    ```

**不要删除 `~/.config/qatlas`**（macOS / Windows 保留对应平台的配置目录），其中的配置和凭据无需因包名迁移而清除。最后运行 `qatlas --help`、`qatlas config path` 验证安装和配置路径。客户端后续升级与问题反馈请到 [qatlas-cli 仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli)。

## 通用配置：client YAML / server `.env`

**v0.17.0 起 client 和 server 配置完全分离**：

- **Client (`qatlas`)**: YAML，由 [`platformdirs`](https://platformdirs.readthedocs.io/) 选定位置（Linux `~/.config/qatlas/`、macOS `~/Library/Application Support/qatlas/`、Windows `%APPDATA%\qatlas\`）。**首次跑任何 `qatlas <cmd>` 自动创建模板**——不需要 `qatlas config init` 步骤。

    最小配置：

    ```yaml
    # config.yaml — auto-created on first qatlas invocation; edit directly or via `qatlas config set`
    server_url: https://quantum-atlas.ai
    # insecure: true              # 仅当远端用自签证书
    # token: qat_xxxxxxxx         # 需要写操作时
    # mineru_api_token: jwt_...   # 仅当本地跑 qatlas contrib mineru
    ```

    或用 `qatlas config set` 维护：`qatlas config set server_url https://quantum-atlas.ai`。

- **Server (`qatlasd`)**: 三入口 CLI flag > OS env > `.env` 文件 > default。最小 `.env` 见 [reference/env-vars](reference/env-vars.md) 和模板 [`.env.example`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.env.example)。

## 获取帮助

- 看 [Python 客户端](client/index.md)（客户端工作流）
- 查 [参考](reference/index.md)（字段 / 格式 / 环境变量）
- 翻 [FAQ](about/faq.md)
- 提 [GitHub issue](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues)
