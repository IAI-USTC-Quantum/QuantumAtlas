# 安装与 service 注册

## 选择部署方式

| 方式 | 适合 | 起手步骤 |
|---|---|---|
| **预编译 binary + systemd** | 长期生产、单机 / 多边缘、目标机无需构建工具链 | 下载同 tag 安装脚本、审阅、安装，再单独注册服务 |
| **`go install` + systemd** | 已有 Go 工具链、希望本机编译 | 安装明确版本；首次启动自动取得同版本 UI |
| **docker compose** | 容器部署（PostgreSQL / 对象存储保持外部） | 见 [docker.md](docker.md) |

服务端业务配置统一为 **YAML**。不同部署方式可使用同一配置模型，但迁移时仍须核对路径、属主、数据库版本及一致性备份；不能同时用两个实例写同一 `pb_data`。

!!! warning "新分发格式的适用范围"

    下文 `TAG=vX.Y.Z` 是**必须替换的占位符**：请选择已经采用新流程、且所需附件已经公开的服务端 tag。这套流程变更本身不是一次新发行，历史 `v0.34.0` 不会补发这些新格式产物；不要重发旧 tag，也不要用 `latest` 掩盖发布切换窗口。

## 安装预编译 binary（推荐）

新流程使用 GoReleaser **v2.18.1** 的默认归档命名，支持 `linux/amd64`、`linux/arm64`、`darwin/arm64` 三个平台：

```text
qatlasd_<version>_linux_amd64.tar.gz
qatlasd_<version>_linux_arm64.tar.gz
qatlasd_<version>_darwin_arm64.tar.gz
qatlasd_<version>_web.zip
qatlasd_<version>_checksums.txt
```

`<version>` 不带前导 `v`。每个 `tar.gz` 内是普通文件 `qatlasd`（另有默认收录的说明 / 许可证），不是旧式裸附件 `qatlasd-<os>-<arch>`。官方预编译程序已内嵌完整 UI 与文档，**首次启动不需要联网下载 UI**；业务功能依赖的数据库、外部 API 等网络需求不变。

### 从同一新 tag 下载脚本，审阅后执行

```bash
# 必须替换为已采用新流程且附件已公开的 tag
TAG=vX.Y.Z
curl -fL --proto '=https' --proto-redir '=https' \
  "https://raw.githubusercontent.com/IAI-USTC-Quantum/QuantumAtlas/$TAG/cmd/qatlasd/install-qatlasd.sh" \
  -o install-qatlasd.sh
less install-qatlasd.sh
# 审阅通过后再执行；脚本版本与安装目标必须相同
sh install-qatlasd.sh --version "$TAG"

# 如需更换安装目录，在上述执行命令中另加：--dir /opt/qatlas/bin
```

默认安装位置为 `~/.local/bin/qatlasd`。确认该目录在 `PATH` 中；指定其他目录时，调用者须自行保证写权限。脚本保留 `QATLAS_VERSION`、`QATLAS_INSTALL_DIR`、`QATLAS_REPO` 工具覆盖项，它们不是服务端业务配置；生产建议显式传 `--version` 和 `--dir`。

安装器需要 **curl（优先）或 GNU wget**、`tar`、`sha256sum` 或 `shasum`，以及标准命令行工具（如 `awk`、`cmp`、`mktemp`）。BusyBox wget 缺少所需的禁止自动重定向能力，会明确被拒绝；这种环境请安装 curl。脚本是 POSIX sh，但不意味着任意极简 BusyBox 环境都已具备这些工具。

`QATLAS_INSTALL_TIMEOUT` 默认 `60` 秒，用于每次请求 / 归档处理步骤；`QATLAS_INSTALL_VERSION_TIMEOUT` 默认 `10` 秒，用于候选 binary 的版本执行检查。两者都只接受 `1..300` 的秒数，不是总升级时限。按需仅给该次安装调用设置，例如：

```bash
QATLAS_INSTALL_TIMEOUT=120 QATLAS_INSTALL_VERSION_TIMEOUT=20 \
  sh install-qatlasd.sh --version "$TAG"
```

这些是安装工具参数，不要把它们写入业务 YAML 或用来配置服务环境。下载脚本失败时停止，不要执行磁盘上的残留旧脚本。

!!! danger "首次迁移不能使用旧服务内嵌的安装脚本"

    `/install-qatlasd.sh` 由**正在运行的 qatlasd** 内嵌提供。旧实例仍返回只认识旧附件格式的脚本，不能用它安装新格式。首次升级必须使用上面**同一新 tag 的 raw.githubusercontent.com 脚本入口**；首次升级完成后，实例提供的内嵌脚本才随新 binary 切换。本流程不提供旧格式兼容层。

### 脚本的安全替换边界

1. 检测支持的 OS / arch，解析目标 tag，下载对应 `tar.gz` 和 `qatlasd_<version>_checksums.txt`。
2. 校验归档 SHA256；缺失、重复或不匹配的校验记录会失败。
3. 严格检查 tar 成员，拒绝危险路径、链接、重复条目等；只取预期的唯一普通 `qatlasd`，不把整个归档直接解到安装目录。
4. 在**目标文件系统**内创建临时文件、设置执行权限，并执行 `--version`，精确核对目标版本。
5. 全部通过后才 atomic rename 替换目标。下载、校验、解包、版本检查或替换失败，不改旧 binary / `config.yaml`。

脚本**不会自动 sudo、生成 / 覆盖配置、注册服务或重启服务**。替换 binary 与注册服务是两件独立的事；后者会启动服务，须在配置和数据准备好后单独执行。

### Release 资产的校验方式

SHA256 校验是安装器的必经步骤，用于发现传输、落盘或镜像内容不一致。**同源 checksum 不是签名**：能同时替换归档与校验清单的攻击者可以让二者重新匹配。HTTPS 和 checksum 也不代表源码本身安全。

手动核验时，下载同 tag 清单并找到目标归档的**唯一一条**记录，再比对本地摘要：

```bash
TAG=vX.Y.Z                     # 替换为符合上述条件的公开 tag
VERSION=${TAG#v}
ARTIFACT="qatlasd_${VERSION}_linux_amd64.tar.gz"  # 按实际平台修改
BASE="https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases/download/$TAG"
curl -fL "$BASE/$ARTIFACT" -o "$ARTIFACT"
curl -fL "$BASE/qatlasd_${VERSION}_checksums.txt" -o "qatlasd_${VERSION}_checksums.txt"
sha256sum "$ARTIFACT"          # macOS 可用 shasum -a 256 "$ARTIFACT"
```

**当前发布流程不再生成 GitHub attestation**，不能假定新归档或镜像带有 SLSA build provenance。只有核对历史 tag 确实发布过相应证明时，才可用 GitHub CLI 验证历史产物：

```bash
gh attestation verify "./$ARTIFACT" --repo IAI-USTC-Quantum/QuantumAtlas
# 对已下载的 UI zip，验证对象同样是 zip 本身：
gh attestation verify "./qatlasd_${VERSION}_web.zip" --repo IAI-USTC-Quantum/QuantumAtlas
```

**证明对象是 `tar.gz` / `zip` 归档本身，不是解出的 `qatlasd`。** 检查验证结果里的仓库、workflow、tag / commit 是否符合预期。Provenance 证明特定构建身份与产物摘要的关联，**不保证源码无恶意、依赖无漏洞或有源码写权限的攻击者无法发版**。安装器和运行时 UI 下载器不会自动执行这一步 attestation 核验。

## 用 Go 原生安装

Git / Go 模块只分发源码，不提交生成的 `web/dist`。安装明确的公开版本只需要符合仓库 `go.mod` 要求的 Go 工具链，**不需要 Node、npm 或 Sphinx**：

```bash
# vX.Y.Z 必须替换为已采用新流程、带公开 UI 资源的 tag
go install github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@vX.Y.Z
# 默认在 ~/go/bin；若设置了 GOBIN / GOPATH，以实际位置为准
~/go/bin/qatlasd --version
```

版本优先取有效的 `main.version` 注入值，其次取 Go build info 的 `Main.Version`；CLI / API 展示统一去掉前导 `v`（例如输出 `qatlasd version X.Y.Z`）。`--version` 不需要业务配置、数据库或 UI 下载。

### 首次启动与按版本离线缓存

普通 `go install` 不嵌入生成资源。首次 `serve` 在没有内嵌资源时，从程序的**精确版本**下载：

```text
https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases/download/v<version>/qatlasd_<version>_web.zip
https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases/download/v<version>/qatlasd_<version>_checksums.txt
```

下载器核对 **SHA256 + zip comment 中的版本**，并验证资源结构后，缓存到：

```text
os.UserCacheDir()/qatlas/ui/v<version>/
├── bundle.zip
└── sha256
```

后续启动按版本重新验证本地缓存，可离线提供 UI；升级到新版本使用新的版本目录，不复用旧版本资源。Linux 通常为 `${XDG_CACHE_HOME:-$HOME/.cache}/qatlas/ui/`，macOS 通常为 `~/Library/Caches/qatlas/ui/`。以**实际服务运行用户**的缓存目录为准，不能用 root 的缓存代替普通服务用户的缓存。

zip 与官方二进制内嵌资源来自**同一次 Sphinx 两站构建 + npm 构建产物**，通过统一 `fs.FS` 提供 SPA、静态文件与文档路由。资源下载和选择不新增业务 YAML 字段；现有部署仍按 `config init` 配置 PostgreSQL / OAuth / S3 等即可。

`dev`、`(devel)` 或 Go pseudo-version 不能据此下载一个“差不多的 latest”：没有有效发布版本、附件尚为 draft / 不存在、校验失败都会明确报错，**不会回退 latest**。请选择带公开资源的新 tag；开发者则按[贡献指南](../contributing.md)完成 Sphinx 两站及 npm 全 UI 构建，再用 `go build -tags embedui ./cmd/qatlasd` 编译内嵌版本。仅加 build tag 不会代替资源构建。

### 常见错误

- **404 / 缺少资源**：核对 tag 是否采用新流程、Release 和对应平台归档 / UI zip / checksum 是否已公开。Git tag 可被 `go install` 发现，不代表 Release 附件已经可下载。
- **缓存损坏 / 不完整**：启动会拒绝使用，不能跳过校验。确认服务用户和版本后，移走**该版本**缓存目录，再在可联网时重新启动；不要手改 `sha256` 让损坏资源通过。
- **目录不可写**：安装目录和运行用户的缓存目录是两种权限。检查失败发生在哪一步，按需修正目录属主 / 服务沙箱；不要让安装器自动提权。
- **不支持的平台**：官方仅发上述三平台；其他平台可尝试明确版本的 Go 源码安装，不等于承诺经过生产验证。

## 准备 YAML 配置

```bash
# 默认 ~/.qatlas/config.yaml；已有文件时拒绝覆盖
qatlasd config init
# 或指定路径（调用者须有写权限）
qatlasd config init --config /path/to/config.yaml

# 编辑后检查路径与有效配置（secret 默认脱敏）
qatlasd config path --config /path/to/config.yaml
qatlasd config show --config /path/to/config.yaml
```

新建模板为 mode `0600`，按需取消注释并填写。完整字段见随 binary 提供的模板和[服务端配置](server-config.md)；不要在升级时对已有配置运行 `config init --force`。

服务端**不读取 `.env` 或 process env 作为业务配置**；残留的 `QATLAS_*` 业务变量、`MINERU_*`、`GITHUB_CLIENT_*` 等旧配置变量会触发报错，须迁入 YAML 并从服务环境删除。安装工具覆盖项例外，不应用来配置业务。

配置文件必须能被最终服务用户读取。若用 `sudo qatlasd config init --config /etc/quantum-atlas/config.yaml` 创建，文件由 root 拥有且为 `0600`；注册成普通用户运行的 system service 前，须明确调整属主 / 访问权限，不能只因文件存在就认为服务能读。

## 注册为系统服务

`qatlasd service install` 使用**当前执行的 binary 路径**生成服务，显式传入 YAML 路径最易审计：

```bash
# 先在前台检查；确认后 Ctrl-C，再注册服务
qatlasd --config "$HOME/.qatlas/config.yaml" serve --http=127.0.0.1:4200

# user mode，不用 sudo；非 TTY 必须同时给 --mode 和 --force
qatlasd service install --mode user \
  --config "$HOME/.qatlas/config.yaml" --bind 127.0.0.1:4200 --force

# system mode：从预定服务用户的 shell 运行 sudo
sudo /home/<USER>/.local/bin/qatlasd service install --mode system \
  --config /etc/quantum-atlas/config.yaml --bind 127.0.0.1:4200 --force
```

交互模式会提示 user / system、确认默认 `~/.qatlas/config.yaml`（若存在）、渲染 unit 并确认写入。显式 `--config` 必须指向已存在的文件；未指定且默认文件不存在时，unit 不固定 `--config`，运行时按服务用户的默认路径加载。**注册会写入并启动服务**，不是仅生成模板。

| Flag | 默认 | 含义 |
|---|---|---|
| `--mode user\|system` | TTY 询问；非 TTY 必填 | 服务安装位置与管理模式 |
| `--config <path>` | 已存在的 `~/.qatlas/config.yaml`，否则不固定 | 写入 `qatlasd --config <绝对路径> serve` |
| `--bind <addr>` | `127.0.0.1:4200` | 写入 `serve --http=`，优先于 YAML 的 `http_addr` |
| `--name <name>` | `qatlasd` | 服务名 |
| `--dry-run` | false | 只渲染，不写盘、不 reload；非 TTY 仍须 `--mode` 和 `--force` |
| `--force` | false | 跳过确认并允许替换已有 unit；非 TTY 必填 |

!!! danger "不要混用 sudo 与 user mode"

    system mode 写 `/etc/systemd/system/`，需要 root 写权限，但运行用户优先由 `$SUDO_USER` 决定，并非默认必须 root 运行。应从预定运行用户的 shell 调用 `sudo qatlasd service install --mode system ...`；不要用 `sudo -u <user> ... --mode system`，也不要用 `sudo ... --mode user`。若 sudo 的 PATH 找不到 binary，使用绝对路径。

### systemd unit 示例

以下是 system mode 的语义示例；实际结果以 `service install --dry-run --mode system --config ... --force` 为准：

```ini title="/etc/systemd/system/qatlasd.service"
[Unit]
Description=QuantumAtlas server (Go + PocketBase)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=<USER>
WorkingDirectory=/etc/quantum-atlas
ExecStart=/home/<USER>/.local/bin/qatlasd --config /etc/quantum-atlas/config.yaml serve --http=127.0.0.1:4200
Restart=on-failure
RestartSec=5
KillSignal=SIGINT
TimeoutStopSec=15
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=no
ReadWritePaths=/etc/quantum-atlas /home/<USER>/.local/share/qatlasd
LockPersonality=true
RestrictRealtime=true

[Install]
WantedBy=multi-user.target
```

自动生成的 `ReadWritePaths` 包含配置目录、默认 XDG 数据目录和已存在的 `~/QuantumAtlas-Wiki`，**不会解析 YAML 中的自定义存储路径**。非默认数据路径或额外只读沙箱需要管理员核对 drop-in；无内嵌 UI 时也须确保运行用户的缓存目录可写。`ProtectSystem=full` 本身不把整个 home 设为只读。

!!! warning "ReadWritePaths 与启动权限"

    列出的目录不存在可能导致 `status=226/NAMESPACE`。在注册 / 重启前创建目录、核对属主与权限；修改 unit / drop-in 后要 `daemon-reload`，只改 YAML 则重启即可。不要以旧进程仍正常运行为依据跳过下一次启动验证。

### 验证与管理

```bash
# user mode
qatlasd service status --mode user
journalctl --user -u qatlasd -n 50
# system mode（不要让普通用户命令默认检查 user unit）
sudo qatlasd service status --mode system
journalctl -u qatlasd -n 50
curl http://127.0.0.1:4200/api/health | jq
```

`healthy` / `degraded` 应结合 `checks` 和日志判断；还应检查首页、JS 与文档能否加载。user unit 若要未登录也常驻，需按本机权限配置 `loginctl enable-linger`。macOS 的 launchd 服务管理仍须单独验证，不能将 Linux 的 systemd 验证等同于 macOS 生产验证。

## 升级与卸载

升级必须**先读目标版本说明并备份，再安全替换，最后显式重启 / 验证**。不要将“保留旧 binary”当作“可回滚数据库”的保证；完整步骤见[备份与升级](backup-and-upgrade.md)。

卸载时显式选择正确模式：

```bash
qatlasd service uninstall --mode user
# system mode 则用：sudo qatlasd service uninstall --mode system
trash-put ~/.local/bin/qatlasd
# config.yaml、pb_data、raw 和 UI 缓存不会自动删除，另行决定保留策略
```

不使用 service 时，直接前台运行 `qatlasd --config /path/to/config.yaml serve --http=127.0.0.1:4200`，由容器或其他进程管理器托管即可。
