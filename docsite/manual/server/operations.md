# Deployment

## 适用范围

这份文档描述的是 QuantumAtlas 服务的部署方式。

目标是把下面几件事拆清楚：

- 如何本地启动一个可工作的服务。
- 如何把它安装成长期运行的 systemd 服务。
- 如何在公网入口前放置反向代理和鉴权层。
- 如何在不暴露真实机器名、真实地址或私有路由结构的前提下，给出可复用的 Caddy 示例。

> 本文不收录任何一次性 ops 脚本。生产部署时把"运维动作"通过本文档的
> "思路 + 模板"自己拼出来；过往一次性脚本只保留在维护者 `/tmp/` 直到完成，
> 然后丢弃。这样仓库不积累陈旧脚本，每次环境改动都强迫维护者重新理解。

## Go server 部署（当前路径）

QuantumAtlas server 是单个 Go 二进制 `qatlasd`，自带 PocketBase + SQLite。
官方预编译产物内嵌 SPA 与文档；普通 Go 源码安装则在首次 `serve` 时取得同版本
资源。两者使用统一 `fs.FS` 提供 SPA / 静态文件 / 文档服务。下游部署 =
拿到 binary + 准备 YAML + 装 systemd unit + 反代。下文的 `<USER>` / `<HTTP_PORT>`
须按实际环境替换。

### 1. 获取 binary

```{admonition} 先确认新流程的公开 tag
:class: warning

下文 `TAG=vX.Y.Z` / `@vX.Y.Z` 是显式占位符，必须替换为已经采用新流程且所需附件已公开的 tag。这套流程变更本身不是一次新发行，历史 `v0.34.0` 不会补发新格式产物；不要直接替换成它或假定 latest 已迁移。
```

#### A. 同 tag 安装脚本（推荐）

新流程使用 GoReleaser v2.18.1 默认的
`qatlasd_<version>_<os>_<arch>.tar.gz`，归档内为普通文件 `qatlasd`。
预编译平台为 `linux/amd64`、`linux/arm64`、`darwin/arm64`：

```bash
TAG=vX.Y.Z  # 替换为符合上述条件的公开 tag
curl -fL --proto '=https' --proto-redir '=https' \
  "https://raw.githubusercontent.com/IAI-USTC-Quantum/QuantumAtlas/$TAG/cmd/qatlasd/install-qatlasd.sh" \
  -o install-qatlasd.sh
less install-qatlasd.sh
# 审阅通过后，安装同一个 tag；可另加 --dir /opt/qatlas/bin
sh install-qatlasd.sh --version "$TAG"
```

默认安装到 `~/.local/bin/qatlasd`。脚本校验同 tag 的
`qatlasd_<version>_checksums.txt`、严格检查 tar 成员，在目标文件系统暂存并
精确核对 `--version` 后 atomic rename；失败不改旧 binary / YAML。
**不自动 sudo、注册或重启服务，也不覆盖配置。** 工具覆盖项
`QATLAS_VERSION` / `QATLAS_INSTALL_DIR` / `QATLAS_REPO` 不属于业务配置。

首次从旧发行格式迁移时，**不能用旧实例 `/install-qatlasd.sh`** 安装新格式：
它仍是旧 binary 内嵌的脚本。必须从目标新 tag 取得并审阅脚本；首次升级后，
服务内嵌脚本才切换，不提供旧格式兼容。

官方 binary 内嵌的资源和 Release `qatlasd_<version>_web.zip` 是同一份
Sphinx 两站 + npm 产物，首次运行无需下载 UI。目标机不需要 Go / Node / Sphinx，
但安装器需要 curl 或 GNU wget、tar、SHA256 工具和标准命令行工具；BusyBox wget
会被拒绝，可改用 curl。依赖与可调超时详见[安装文档](install.md)。
SHA256 用于完整性检查，同源清单不是来源签名，也不证明源码安全或程序无 bug。
后续采用新 workflow 的版本在 GoReleaser 发布后生成 GitHub 签名构建证明，
可用 `gh attestation verify` 核验；已发布的 `v0.35.0-rc.1` 不追溯补签。
Release 已公开不代表证明步骤已成功，详见[安装与校验](install.md)。

#### B. `go install`

Git / Go 模块仅分发源码。准备符合 `go.mod` 要求的 Go 工具链即可，
**普通安装不需要 Node / npm / Sphinx**：

```bash
go install github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@vX.Y.Z
~/go/bin/qatlasd --version  # 若设置了 GOBIN / GOPATH，以实际位置为准
```

版本优先来自有效 `main.version` 注入值，其次为 Go build info 的
`Main.Version`，显示时去掉前导 `v`。`--version` 不加载配置或联网。

无内嵌 UI 时，首次 `serve` 从精确版本的 GitHub Release 下载：

```text
https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases/download/v<version>/qatlasd_<version>_web.zip
https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases/download/v<version>/qatlasd_<version>_checksums.txt
```

核对 SHA256、zip comment 版本和资源结构后，缓存到
`os.UserCacheDir()/qatlas/ui/v<version>/{bundle.zip,sha256}`。
后续启动重新按版本校验本地缓存，可离线提供 UI；升级建立新版本缓存。
请按**服务运行用户**准备缓存访问权限 / 首次网络访问，而非安装时的 root 用户。
已有损坏或不完整缓存会报错；确认版本后移走该版本目录再联网重试，不能禁用校验。
业务 YAML 无需增加任何资源字段。

`dev` / `(devel)` / pseudo-version 或尚无公开附件的版本不能自动找 latest
凑资源；失败会明确退出。使用带公开资源的新 tag，或走下面的开发者内嵌构建。

#### C. 开发者全 UI 构建 / 自带 binary

Git 不保留生成的 `web/dist`。开发者按[贡献指南](../contributing.md)完成
**Sphinx 两站 → npm UI 构建**后，再运行：

```bash
go build -tags embedui -o build/qatlasd ./cmd/qatlasd
```

仅加 `embedui` 不会自动运行 Node / Sphinx。未发布 commit 的开发版本使用
完整内嵌资源，不回退线上 latest。可在受控 build host 构建后经 `scp` / `rsync`
等传到目标机；上线前自行核验来源、平台和版本，并按[备份与升级](backup-and-upgrade.md)
先备份、在目标文件系统暂存验证、原子替换。不要直接覆盖正在运行的 binary。

### 2. 目录布局（推荐）

按 XDG Base Directory（[freedesktop spec][xdg-spec]）+ FHS 拆分：git
checkout 仅供开发，生产不需要源码目录，也不应把含密钥的配置提交到 Git。
用户级数据去 `$XDG_DATA_HOME`（默认 `$HOME/.local/share/`），自定义系统级数据
可放 `/var/lib/`；业务配置与可重建的 UI 缓存分开保存。

[xdg-spec]: https://specifications.freedesktop.org/basedir-spec/latest/

用户级布局（以下缓存路径为 Linux 默认值）：

```
/home/<USER>/
├── .qatlas/config.yaml             # 业务 YAML，config init 生成，0600
├── .cache/qatlas/ui/v<version>/     # 无 embed 时的版本缓存，可重建
│   ├── bundle.zip
│   └── sha256
└── .local/
    ├── bin/qatlasd                 # 用户可写 binary
    └── share/qatlasd/              # 默认 XDG 数据目录
        ├── raw/                   # paths.raw_dir（PDF / MinerU 输出）
        ├── data/                  # paths.data_dir（运行时元数据）
        └── pb_data/               # paths.pb_data_dir（PocketBase SQLite）
```

系统级（显式选择 shared `/var/lib` 布局时）：

```
/etc/quantum-atlas/config.yaml      # ExecStart 通过 --config 指定
/usr/local/bin/qatlasd              # 系统 binary
/var/lib/quantum-atlas/             # YAML paths.* 显式设置的状态根
├── raw/
├── data/
└── pb_data/
```

不覆盖 `paths.*` 时，数据路径仍按服务用户的 `$XDG_DATA_HOME` / `$HOME`
计算，**不会因为 service 是 system mode 就自动迁到 `/var/lib/`**。
需要 FHS / 共享挂载点 / 独立分区时在 YAML 中显式设置。UI 缓存独立由
`os.UserCacheDir()` 定位，不是 `paths.data_dir` 的子目录。

binary 路径选 `~/.local/bin/` vs `/usr/local/bin/` 的取舍：

- `~/.local/bin/` 归运行用户所有，安全替换 binary 不需要 sudo。配
  user-mode systemd 单元时连 restart 也免 sudo；`go install` 的 `$GOBIN`
  与 unit 所引用的路径仍须核对，不能以 PATH 中另一个 binary 的版本代替验证。
- `/usr/local/bin/` 通常由 root 拥有，更新须由管理员授权写入。
  安装器不会自行 sudo，典型 system-mode 部署会采用这种布局。
- systemd 单元可以引用任意路径——`ExecStart=/home/<USER>/.local/bin/qatlasd`
  跟 `/usr/local/bin/qatlasd` 在 systemd 视角下完全等价。

### 3. systemd unit 模板

QuantumAtlas server 内置 `qatlasd service install` 子命令，**主推这条
路径**——自动生成 systemd unit（含 hardening）+ daemon-reload + enable +
start 一步到位，免手抄、免漂移。手写模板降级到本节末 §3.C 作为"想自定义
hardening 时的参考"。

**两种部署模式核心取舍**：

| 维度 | A. user-mode (`~/.config/systemd/user/`) | B. system-mode (`/etc/systemd/system/`) |
|---|---|---|
| 文件归属 | 运行用户拥有，不需要 sudo 编辑 | root 拥有，需要 sudo 编辑 |
| restart 权限 | 免 sudo（`systemctl --user restart`） | 需 sudo（`sudo systemctl restart`） |
| 启动时机 | 需要 `loginctl enable-linger <user>` 让未登录也保活 | boot 自起，无需 linger |
| systemd hardening 能力 | 基本（`PrivateTmp` 等部分指令不可用） | 完整（`ProtectSystem` / `ReadWritePaths` 全可用） |
| 适用场景 | 个人维护 / 频繁迭代 / 一人一服务 | 严格 hardening / 多用户共享 / 标准 FHS 部署 |

#### 3.A `qatlasd service install`（推荐）

子命令包装 [`github.com/kardianos/service`](https://github.com/kardianos/service)
做 unit 生成 + systemctl 操作；装完之后跟原生 systemctl 管理同一 unit
（例如 `qatlasd service start --mode user` 对应 `systemctl --user start qatlasd`）。

```bash
# 首次准备 ~/.qatlas/config.yaml；已有配置不要 --force 覆盖
qatlasd config init
# 编辑 OAuth / PostgreSQL / S3 等字段，然后检查有效配置（默认脱敏）
qatlasd config show --config "$HOME/.qatlas/config.yaml"

# 完全交互式 — 提示 mode、确认默认 YAML 路径、渲染 unit 后确认
qatlasd service install

# user mode：普通用户执行；写 unit 并启动服务
qatlasd service install \
    --mode user \
    --config "$HOME/.qatlas/config.yaml" \
    --bind 127.0.0.1:4200 \
    --force

# system mode：从预定服务用户的 shell 使用 sudo，配置须可被该用户读取
sudo /home/<USER>/.local/bin/qatlasd service install --mode system \
    --config /etc/quantum-atlas/config.yaml --bind 127.0.0.1:4200 --force

# 只看会写什么（不写文件）；非 TTY 的 dry-run 同样要 --mode / --force
qatlasd service install --dry-run --mode user \
    --config "$HOME/.qatlas/config.yaml" --force
```

flag 含义：

| flag | 默认 | 含义 |
|---|---|---|
| `--mode` | TTY 时按 uid 提示（root→system / 非 root→user）；非 TTY 必填 | `user` 或 `system` |
| `--config` | 默认 `~/.qatlas/config.yaml` 已存在时使用，否则不固定 | 显式路径必须存在；以绝对路径写入 `qatlasd --config <path> serve` |
| `--bind` | `127.0.0.1:4200` | 写入 `serve --http=`，覆盖 YAML `http_addr`；反代场景保留 loopback |
| `--name` | `qatlasd` | systemd unit 名（生成 `<name>.service`） |
| `--dry-run` | false | 只打印渲染后的 unit，不写文件、不 reload |
| `--force` | false | 跳过确认并允许替换已有 unit；非 TTY 上下文必填 |

system mode 需要 root 写 unit，但 `User=` 优先取 `$SUDO_USER`，不意味着 daemon
必须 root 运行。不要 `sudo ... --mode user` 或 `sudo -u <user> ... --mode system`。
用 root 新建的 `0600` YAML 须先为最终服务用户安排访问权限。

生成的 unit 固定当前 binary 路径，使用 `--config`，不再写业务 `Environment=` /
`EnvironmentFile=`。固定配置时 `WorkingDirectory` 为配置所在目录；不固定时为
服务用户的 home。保留 `RestartSec=5` / `KillSignal=SIGINT` / `TimeoutStopSec=15`
及模板中的 hardening；user mode 的沙箱指令支持程度取决于本机 systemd 环境。

**自动检测 ReadWritePaths** 仅包括 YAML 所在目录、默认 XDG 数据目录及已存在的
`~/QuantumAtlas-Wiki`，不解析 YAML `paths.raw_dir` / `paths.data_dir` /
`paths.pb_data_dir` 中的自定义路径。非默认布局需用对应模式的 `systemctl edit`
核对 drop-in，并预先创建目录、核对属主；无 embed 时还要保证服务用户的 UI 缓存
可写。`ProtectSystem=full` 本身不把整个 home 设为只读，但额外沙箱可能会。

#### 3.B 其他管理命令

```bash
qatlasd service status --mode user
qatlasd service start --mode user
qatlasd service stop --mode user
qatlasd service restart --mode user
qatlasd service uninstall --mode user   # stop + 删除服务注册
# system mode 对应：sudo qatlasd service <verb> --mode system
```

这些命令与对应的 `systemctl --user <verb> qatlasd` / `sudo systemctl <verb> qatlasd`
管理同一个 unit。默认模式按当前 uid 判断，普通用户查 system unit 时必须显式
`--mode system`，不要误查同名 user unit。drop-in 修改对两种管理入口均生效。

#### 3.C 手写 unit 模板（自定义 hardening 时的参考）

下面是可定制的语义模板，不是生成器输出的逐字副本；实际生成内容应通过
`service install --dry-run` 审阅。直接手写 unit 适合需要额外
`CapabilityBoundingSet=` / `SystemCallFilter=` / `MemoryMax=`、自定义 logging
或 monitoring agent 联动等场景。

**A. user-mode** (`~/.config/systemd/user/qatlasd.service`)：

```ini
[Unit]
Description=QuantumAtlas server (Go + PocketBase)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple

# YAML 是业务配置唯一来源；不要用 EnvironmentFile 注入旧业务变量。
# %h 在 user-mode unit 里展开成 $HOME，YAML 相对路径以其所在目录为 anchor。
WorkingDirectory=%h/.qatlas

# pb_data 用 YAML paths.pb_data_dir 控制（默认 XDG 数据目录），
# 不要另写 --dir 覆盖；--http 会优先于 YAML http_addr。
# WSL2 + Windows v4-only portproxy 场景按需在 YAML 写 force_tcp4: true；
# 普通 Linux VPS 不必打开。
ExecStart=%h/.local/bin/qatlasd --config %h/.qatlas/config.yaml serve --http=127.0.0.1:<HTTP_PORT>
Restart=on-failure
RestartSec=5
KillSignal=SIGINT
TimeoutStopSec=15

[Install]
WantedBy=default.target
```

启用 + 起动（**无 sudo**）：

```bash
systemctl --user daemon-reload
systemctl --user enable --now qatlasd.service
systemctl --user status qatlasd.service
loginctl enable-linger "$USER"   # 一次性：未登录也保活
```

**B. system-mode** (`/etc/systemd/system/qatlasd.service`)：

```ini
[Unit]
Description=QuantumAtlas server (Go + PocketBase)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=<USER>
Group=<USER>

WorkingDirectory=/etc/quantum-atlas
# YAML 须能被 User=<USER> 读取；数据路径由 paths.* 控制，不另写 --dir。
ExecStart=/home/<USER>/.local/bin/qatlasd --config /etc/quantum-atlas/config.yaml serve --http=127.0.0.1:<HTTP_PORT>
Restart=on-failure
RestartSec=5
KillSignal=SIGINT
TimeoutStopSec=15

# Hardening：read-only 系统目录 + 只把 stateful 路径打开写权限。
# 按 YAML paths.* 和实际沙箱调整 ReadWritePaths，并预先创建目录。
# 下面示例对应数据全部走 XDG 默认；额外只读策略还须考虑 UI 缓存：
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

启用 + 起动（需要 sudo）：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now qatlasd.service
sudo systemctl status qatlasd.service
```

### 4. 日常 deploy 流程

**首次部署**先准备 YAML 和数据目录，前台验证后再注册 service（注册会启动）。
**后续升级**不能只覆盖 binary 然后碰运气重启：

1. 确认目标是已采用新流程的公开 tag，先读 release notes / schema 迁移说明。
2. 保存旧 binary、YAML / unit 和一致性数据备份，制定停写窗口与恢复方案。
3. 按 §1.A 下载并审阅**同一目标 tag** 的安装脚本，再传同一个 `--version`；
   对 `go install` / 自带 binary 使用独立暂存路径验证，不直接覆盖活动文件。
4. 按对应模式显式重启，核验实际运行版本、日志、健康检查、首页 / JS / 文档。
   无内嵌资源时，新版本首次启动需要其公开 UI 附件及服务用户的可写缓存。

安装脚本不替你重启服务、调整 YAML 或迁移服务注册。原子替换只保护 binary
文件切换；**保留旧 binary 不等于数据库可回滚**。即使同为 0.x / patch 升级也要
逐版检查迁移说明，完整流程见[备份与升级](backup-and-upgrade.md)。

### 5. YAML 业务字段

以 `qatlasd config init` 输出的模板和[服务端配置](server-config.md)为准。
默认 `~/.qatlas/config.yaml`，自定义位置用 `--config`，secret 保持 mode `0600`。
公网部署示例（仅按需启用相应能力）：

```yaml
public_url: https://your-domain.tld
http_addr: 127.0.0.1:4200
postgres:
  dsn: postgres://qatlas:secret@127.0.0.1:5432/qatlas?sslmode=disable

auth:
  github_client_id: <oauth_app_client_id>
  github_client_secret: <oauth_app_secret>
  allowed_logins: [alice, bob]
  admin_logins: [alice]

# 只有需要非默认存储位置才写；否则走 XDG 默认。
# paths:
#   raw_dir: /srv/quantum-atlas/raw
#   data_dir: /srv/quantum-atlas/data
#   pb_data_dir: /var/lib/quantum-atlas/pb_data
```

GitHub OAuth App callback URL 配 `https://your-domain.tld/auth/callback`。
GitHub 登录在 `allowed_logins` 与 `admin_logins` 均为空时拒绝所有登录，不能沿用
旧文档“管理员字段尚未生效”的假设。资源缓存无需 YAML 配置。
服务端不读 `.env`；遗留业务环境变量须迁入 YAML 并从 unit / 启动环境移除。

### 6. 从旧部署迁移到当前布局

如果之前 binary 装在 `/usr/local/bin/`、unit 写死 system-wide 路径、或
raw / data / pb_data 直接放在 git checkout 里——一次性迁移思路
见 [docs/migration-storage-layout.md](migration-storage-layout.md)，
该文档覆盖：

- 把 raw / data / pb_data 从仓库内搬到 `$XDG_DATA_HOME/qatlasd/`
- binary 从 `/usr/local/bin/` 挪到 `~/.local/bin/`
- systemd unit 调整 + 启动验证

每步都该有备份（`cp -a <path> <path>.bak-$(date +%s)`）。整个流程
**不写成提交进仓库的脚本**——下次迁移环境可能完全不一样，强迫维护者
重新读这一节比照本机情况自己拼脚本，更不容易把陈旧假设拷过去。

### 7. 对象存储（RustFS）

PDF / MinerU 输出等大 blob 可走 S3 兼容对象存储而不是本地 `paths.raw_dir`。
Go server 通过 `internal/objstore` 抽象层接 minio-go SDK。YAML 的 `s3.endpoint`、
`bucket_pdf`、`bucket_md`、`bucket_images`、`access_key_id`、`secret_access_key`
六个连接字段须一起填写；全空则 fallback 本地 `paths.raw_dir`（dev / CI 无外部依赖），
半填会启动报错，避免 reader / writer 使用不同后端。

物理部署、bucket / IAM user / policy 的创建、rotate 流程，以及配套的
幂等 bootstrap 脚本 [`scripts/rustfs_bootstrap.sh`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/scripts/rustfs_bootstrap.sh)
统一收录在 [`deployment/rustfs.md`](rustfs.md)（RustFS ↔ qatlasd 集成 ops 指南）。

简言之：

```bash
export RUSTFS_ENDPOINT=https://raw.your-domain.tld
export RUSTFS_ROOT_ACCESS_KEY=<root_ak>      # 维护者密码管理器，不在 git
export RUSTFS_ROOT_SECRET_KEY=<root_sk>
bash scripts/rustfs_bootstrap.sh
# 末尾打印出绑死单桶的 access_key / secret_key
# 上述 RUSTFS_* 仅供 bootstrap 工具使用，不是 qatlasd 配置。
```

然后把服务账号写入 server YAML（不是 RustFS root 密钥）：

```yaml
s3:
  endpoint: https://raw.your-domain.tld
  bucket_pdf: qatlas-pdf
  bucket_md: qatlas-md
  bucket_images: qatlas-images
  access_key_id: <bootstrap 输出的服务账号 key>
  secret_access_key: <bootstrap 输出的服务账号 secret>
```

重启 server，检查启动日志的每桶 `raw store: S3 backend ...` 确认切换成功。

切到 S3 后端后，对应的 presigned URL（5 min TTL，绕过 server 节省 VPS 带宽）
由 server 内部签发；本地 RawDir 后端继续走 ServeFile。客户端拿到的资源 URL
不区分后端，redirect 由 server 透明处理。

边缘 Caddy 多加一个站点把 `raw.your-domain.tld` 反代到 RustFS `:9000`
即可，模板见 [`deployment/rustfs.md`](rustfs.md) 对应章节。

## 推荐的单机生产目录

配置独立于 Git checkout：默认 `~/.qatlas/config.yaml`，或用 `--config` 固定
`/etc/quantum-atlas/config.yaml`。数据使用 XDG 默认路径，或在 YAML `paths.*`
显式选择 `/var/lib/` / 共享盘。无内嵌 UI 的版本缓存可重建，不可代替数据备份。

建议：

- binary 和资源钉同一已公开 release tag，升级前核对说明与备份。
- 运行用户应对 `paths.raw_dir` / `paths.data_dir` / `paths.pb_data_dir` 有写权限，
  对 YAML 有读权限；无 embed 时还需可写的用户缓存目录。
- PostgreSQL 仅对后端服务暴露，不直接开放到公网。
- 公开访问统一走 YAML `public_url`。

## 核心 YAML 字段

完整字段以 `qatlasd config init` 生成的模板和[服务端配置](server-config.md)为准，
运行时默认在 `internal/config/config.go`。服务端拒绝残留的旧业务环境变量
（例如 `QATLAS_*` 业务项、`MINERU_*`、`GITHUB_CLIENT_*`），不是静默忽略后继续启动。
不要再使用 `.env`、`--dotenv-path` 或 `Environment=QATLAS_DOTENV=...`。

| 字段 | 何时需要 | 备注 |
|---|---|---|
| `public_url` | 公网 / 反代部署 | 对外 canonical URL，用于回调、外链；不同于客户端 `server_url` |
| `http_addr` | 默认 `127.0.0.1:4200` | `service install --bind` 生成的 `--http` 优先于本字段 |
| `postgres.dsn` | paper registry + OpenAlex corpus | 留空会降级相关功能 |
| `auth.github_client_id` / `auth.github_client_secret` | GitHub OAuth | 同时检查 `allowed_logins` / `admin_logins` |
| `user_header` | 上游反代注入审计身份头 | 不参与鉴权，仅用于日志 |
| `force_tcp4` | WSL2 + Windows netsh portproxy 场景 | 布尔值；普通 Linux VPS 不必打开 |

其余 `postgres.max_conns`、`search.providers`、`paths.*`、`s3.*`、
`paper_access.mineru.*` 等按模板填写，不新增 UI 资源 URL / 版本选择字段。

## 反向代理与鉴权边界

Go server 内嵌的 PocketBase 自带 OAuth + session 管理。**不需要**外置
caddy-security / oauth2-proxy 这类身份代理；反代只承担 SNI 选路 + TLS
终结 + 反向转发到后端。鉴权完全在 server 内部：

- 浏览器：访问 `/auth-with-oauth2`（PocketBase 内置）走 GitHub OAuth，
  登录后 `pb.authStore` 自动持有 14d 寿命的 session token（不暴露 UI
  入口让你 copy 它），或者在 `/pat` 页创建 fine-grained PAT（前缀
  `qat_`，过期可选）给非浏览器调用用。
- CLI / 自动化：`Authorization: Bearer <token>`，server 端 `authGuard`
  根据前缀分发——`qat_*` 走 `internal/pat` 包做 prefix lookup + bcrypt
  校验并查 scope；其余走 PocketBase session token 验证。
- 写口分两层：`scopeGuard(enforcer, obj, act, handler)` 给"PAT 可调"
  的写口（papers），强制 scope opt-in；`sessionGuard` 给"PAT
  不可调"的写口（PAT 自管理本身、admin 操作），只接受 session token。

如需在边缘补一层 IP/路径 ACL、按域名分流多服务、或做 raw 对象存储反代
（`raw.your-domain.tld` → RustFS），Caddy 模板下面给出。

### 路径分类与对应处理

| 路径 | 鉴权层 | 反代怎么写 |
|---|---|---|
| `/api/health` | open | 直接 reverse_proxy；监控可读（返回 `{code, message, data:{status, version, uptime_seconds, checks{rawstore, postgres}}}`） |
| `/install-qatlasd.sh` | open | 直接 reverse_proxy；当前 binary 内嵌的脚本，首次新格式迁移须改用目标 tag 的仓库脚本 |
| `/{path...}`、`/_/`、`/auth-with-oauth2` 等 SPA + PocketBase 内置 | open / 自管 | 直接 reverse_proxy；OAuth 由 server 自己处理 |
| `/api/search`、`/api/papers/stats` 等读口 | server 内 `authGuard + papers:read` | 直接 reverse_proxy |
| `/api/papers/...`、`/api/pat/...` | server 内的 `authGuard` / `scopeGuard` / `sessionGuard` | 直接 reverse_proxy；**不要**剥 `Authorization` header（server 要拿来鉴权） |
| `raw.your-domain.tld/*`（启用 S3/RustFS 时） | RustFS 自管（presigned URL） | 反代到 RustFS `:9000` |

## Caddy 示例

Caddy 现在只承担 SNI 选路、TLS 终结和反代，不再挂任何 oauth2-proxy /
caddy-security / forward_auth 链。两个常见模板：

### 单域名 + 自带 LE 证书

```caddyfile
atlas.example.com {
    encode gzip zstd

    # 健康检查可独立路由（让监控不被全局 directives 影响）
    handle /api/health {
        reverse_proxy 127.0.0.1:4200
    }

    # 其余全部裸反代到 Go server，鉴权在 server 内部完成。
    handle {
        reverse_proxy 127.0.0.1:4200
    }
}
```

### 多线路 / 国内未备案节点（自签证书）

国内未备案 VPS 通常没法挂任何域名走 443，只能 IP + 非标端口 + 自签：

```caddyfile
# 内网走 Let's Encrypt 真证书（如果域名 A 记录指过来）
atlas.example.com {
    reverse_proxy 127.0.0.1:4200
}

# IP + 非标端口模式（自签 Caddy Local CA）
https://203.0.113.10:18443 {
    tls internal
    reverse_proxy 127.0.0.1:4200
}
```

client 端如果要走 IP + 非标端口入口，优先信任对应 CA；临时排查可用
`qatlas --insecure ...` 跳过证书校验（不要把它当成服务端 YAML 设置）。
想保留真证书验证又用第二条线路，就在本机 hosts 把 `atlas.example.com`
覆盖到对应 IP，TLS 走 SNI 仍然信任原证书。

### 加 raw 对象存储反代（启用 RustFS 时）

```caddyfile
raw.your-domain.tld {
    encode gzip zstd
    reverse_proxy 127.0.0.1:9000   # RustFS s3 endpoint
}
```

启用 RustFS 后端时，资源 presigned URL 会指向 `https://raw.your-domain.tld/...`
（5min TTL），bandwidth 绕开 server。

## 运行建议

- 反代上**不要**清洗或注入 `Authorization` header——server 要拿它做
  bearer 鉴权（PAT 或 session token），剥掉会全部 4xx。
- `/api/*` 中的写口 server 已经强制鉴权；反代上不要再叠 ACL，避免双重
  401 / 403 给 debug 添麻烦。
- 如果启用了 MinerU 并需要它回拉 PDF，YAML `public_url` 必须能从
  MinerU 所在环境访问到。
