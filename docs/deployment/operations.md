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

QuantumAtlas server 是单个 Go 二进制 `qatlasd`（~35MB，CGO-free，
静态链接，自带 PocketBase + SQLite + 嵌入式 SPA 前端）。下游部署 =
拿到 binary + 装 systemd unit + 反代。这里给出一份**单机部署模板**，
路径都用占位变量（`<USER>` / `<APP_HOME>` / `<WIKI_DIR>` /
`<HTTP_PORT>`），运维替换后即可。

### 1. 获取 binary

按门槛从低到高四种方式，任选其一：

#### A. 一行 curl 装（**推荐 / 最快**）

CI release pipeline 已经把 3 平台预编译 binary（`linux/amd64`、`linux/arm64`、
`darwin/arm64`）发到了 GitHub Release。装脚本服务在
`<your-server>/install-qatlasd.sh`，会自动选 OS/arch、下载、SHA256 校验：

```bash
# 装 binary 到 ~/.local/bin/qatlasd，自动校验 SHA256
curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh

# 钉 release tag
curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh -s -- --version v0.2.5

# 改安装目录
curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh -s -- --dir /opt/qatlas/bin
```

支持的环境变量：`QATLAS_INSTALL_DIR`（默认 `~/.local/bin`）、`QATLAS_VERSION`
（默认 latest）、`QATLAS_REPO`（默认 `IAI-USTC-Quantum/QuantumAtlas`）。

装完 binary 后**手动**注册成 systemd 服务（脚本本身刻意不链式调用 `service
install`——见下文"为什么 install-qatlasd.sh 不自动起服务"）：

```bash
qatlasd service install                    # 交互模式，问 mode + .env path
# 或全自动：
qatlasd service install \
    --mode user --dotenv-path ~/QuantumAtlas/.env --force
```

底层就是从 `github.com/IAI-USTC-Quantum/QuantumAtlas/releases/<tag>` 下
`qatlasd-<os>-<arch>` 那个 asset，所以**目标机不需要装任何 toolchain**——
连 Go / npm / pixi 都不要，只要 `curl` 或 `wget` + `install`。也可以走 B/C/D
本地编译，但只在你想钉未发布 commit、或在隔离环境里复现 build 时才需要。

> **为什么 install-qatlasd.sh 不自动起服务**：把 `curl|sh` chain 进 `qatlasd
> service install` 在 dash（Debian / Ubuntu 的 `/bin/sh`）上不可靠——dash 是
> 流式 parser，会在执行到 `exec </dev/tty`
> 切换 stdin 之前已经从 pipe 预读了大量未消费字节，切换后这些字节既不能用作
> 脚本继续解析也不能用作终端输入，要么 hang 要么报 `Syntax error: word
> unexpected`。bash 因为预读整个脚本不受影响，但我们不能假设目标机有 bash
> （Alpine / BusyBox / macOS sh 都是 dash 风格的 POSIX shell）。所以拆成两步：
> install-qatlasd.sh **只装 binary**；service install 由 cobra 程序自己稳定处理
> TTY，没有 shell parser 冲突。

#### B. `go install`

目标机器有 Go 1.26+ toolchain 时，从 GitHub 拉源码 + 本地编译 +
落到 `$GOBIN`（默认 `~/go/bin/`）：

```bash
go install github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@latest
~/go/bin/qatlasd --help
```

钉版本：把 `@latest` 换成 `@v0.2.3` / `@<commit-sha>`。Go module
proxy（`proxy.golang.org`）会自动缓存与做哈希校验。

> ⚠️ `go install` **不**会把前端 SPA build 进 binary（`web/embed.go` 需要
> `web/dist/` 存在），所以这条路装出来的 binary 跑起来 `/{path...}` 会
> 404。需要 SPA 时用 A 或 C。

装完挪到 systemd 引用的路径：

```bash
install -m 0755 ~/go/bin/qatlasd ~/.local/bin/qatlasd
```

#### C. 源码 + `pixi run build`

适合贡献者、想钉本地未发布的 commit、或目标机没装 Go 但有 pixi：

```bash
git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas.git
cd QuantumAtlas
pixi run build                 # 自动跑 npm ci + npm run build + go build
install -m 0755 build/qatlasd ~/.local/bin/qatlasd
```

#### D. 自带预编译 binary

build host 上编完再传给目标 host（典型场景：目标 host 资源紧张，跨网传输
代替本机交叉编译）。怎么把 binary 传到目标 host 本文不规定——`scp` / `rsync`
/ artifact 下载 / `kubectl cp` 都行。落到目标 host 后 `install -m 0755 <src>
~/.local/bin/qatlasd` 即可。

### 2. 目录布局（推荐）

按 XDG Base Directory（[freedesktop spec][xdg-spec]）+ FHS 拆分：git
checkout 只放代码 + 配置；用户级状态去 `$XDG_DATA_HOME`（默认
`$HOME/.local/share/`）；系统级状态去 `/var/lib/`。**不再**把 wiki /
raw / data / pb_data 默认塞进 git checkout 内。

[xdg-spec]: https://specifications.freedesktop.org/basedir-spec/latest/

用户级（per-user systemd 或 `--user` ExecStart）：

```
/home/<USER>/
├── QuantumAtlas/                  # 仅保留 .env；源码 checkout 仅 B 路径需要
│   └── .env                       # 运行配置；server 用 godotenv 读
├── QuantumAtlas-Wiki/             # 兄弟 checkout — WIKI_DIR 默认值
├── .local/
│   ├── bin/qatlasd           # binary（user-writable，sudoless deploy）
│   └── share/qatlasd/             # XDG_DATA_HOME 下，所有 stateful 状态（v0.17.0+；老 install 是 quantum-atlas/，参见 migration-storage-layout.md）
│       ├── raw/                   # RAW_DIR 默认值（PDF / MinerU 输出）
│       ├── data/                  # DATA_DIR 默认值（ingest claims / 运行时元数据）
│       └── pb_data/               # PBDataDir 默认值（PocketBase SQLite）
```

系统级（多用户共享，shared /var/lib 模式，类 Grafana / Gitea）：

```
/etc/quantum-atlas/.env            # 配置；ExecStart 用 QATLAS_DOTENV 指过来
/usr/local/bin/qatlasd        # 系统 binary
/var/lib/quantum-atlas/            # FHS 状态根
├── raw/
├── data/
├── pb_data/
└── QuantumAtlas-Wiki/             # 也可以放别处，用 QATLAS_WIKI_DIR 指
```

两种布局都不要求显式覆盖 `.env`：server 会按 `$XDG_DATA_HOME` /
`$HOME` 自动算出默认。**只**在需要存到非默认路径（FHS / 共享挂载点 /
独立分区）时显式覆盖 `QATLAS_RAW_DIR` 等。

binary 路径选 `~/.local/bin/` vs `/usr/local/bin/` 的取舍：

- `~/.local/bin/` 归运行用户所有，**滚 binary 不需要 sudo**——`go
  install` 默认就落在用户 `$GOBIN`，`install -m 0755 <src>
  ~/.local/bin/qatlasd` 全程普通用户身份。配 user-mode systemd
  单元时连 restart 也免 sudo。
- `/usr/local/bin/` 是 root-owned，每次 binary 滚动都得 sudo install。
  典型 system-mode 部署（FHS / 多用户共享）会这么放。
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
做 unit 生成 + systemctl 操作；装完之后 unit 跟原生 systemctl 100% 互通
（`qatlasd service start` ≡ `systemctl --user start qatlasd`，
都调同一个 systemd unit）。

```bash
# 完全交互式 — 自动检测 mode（按 uid）、自动检测 .env、渲染 unit 给你
# [Y/n] 确认后再写
qatlasd service install

# CI / 脚本式 — 全显式参数，零交互
qatlasd service install \
    --mode user \
    --dotenv-path ~/QuantumAtlas/.env \
    --bind 127.0.0.1:4200 \
    --force

# 只看会写什么（不写文件）
qatlasd service install --dry-run --mode user \
    --dotenv-path ~/QuantumAtlas/.env
```

flag 含义：

| flag | 默认 | 含义 |
|---|---|---|
| `--mode` | TTY 时按 uid 提示（root→system / 非 root→user）；非 TTY 必填 | `user` 或 `system` |
| `--dotenv-path` | TTY 时按 `$QATLAS_DOTENV` → `~/QuantumAtlas/.env` → `./.env` 顺序自动检测并确认 | 传给 server 的 `.env` 路径，用作相对路径 anchor |
| `--bind` | `127.0.0.1:4200` | server 监听地址（生产应配合 Caddy 反代用 127.0.0.1） |
| `--name` | `qatlasd` | systemd unit 名（生成 `<name>.service`） |
| `--dry-run` | false | 只打印渲染后的 unit，不写文件、不 reload | 
| `--force` | false | 跳过所有交互确认（覆盖既有 unit 也不问）；非 TTY 上下文必填 |

生成的 unit **跟 §3.C 手写模板字段语义完全一致**——含全部 7 条 hardening、
`Environment=QATLAS_DOTENV=`、`RestartSec=5` / `KillSignal=SIGINT` /
`TimeoutStopSec=15`、ReadWritePaths 自动从 .env 目录 + `$XDG_DATA_HOME` +
（如存在）`~/QuantumAtlas-Wiki` 推导。

**自动检测 ReadWritePaths** 仅覆盖默认布局；如果你的 .env 显式覆盖
`QATLAS_RAW_DIR` / `QATLAS_DATA_DIR` / `QATLAS_PB_DATA_DIR` / `QATLAS_WIKI_DIR`
到非默认目录，install 之后用 `systemctl edit qatlasd` 加 drop-in
追加 `ReadWritePaths=...` 即可（systemd 会合并）。

#### 3.B 其他管理命令

```bash
qatlasd service status      # = systemctl --user status qatlasd（含 cgroup + journal 最近几行）
qatlasd service start
qatlasd service stop
qatlasd service restart
qatlasd service uninstall   # stop + disable + 删 unit 文件 + daemon-reload
```

跟原生 `systemctl [--user] <verb> qatlasd` **完全等价**——任选其一，
不会冲突。`systemctl edit qatlasd` 添加 drop-in 文件后两边都看得到。

#### 3.C 手写 unit 模板（自定义 hardening 时的参考）

`qatlasd service install` 内部用的就是下面这个模板（user/system mode
分支几行差异）。直接手写 unit 适合需要**严格定制**的场景（额外的
`CapabilityBoundingSet=` / `SystemCallFilter=` / `MemoryMax=` 等
sandboxing；自定义 logging；与 monitoring agent 联动等）。

**A. user-mode** (`~/.config/systemd/user/qatlasd.service`)：

```ini
[Unit]
Description=QuantumAtlas server (Go + PocketBase)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple

# Server 用 github.com/joho/godotenv 加载 .env。把绝对路径作为
# QATLAS_DOTENV 传进来，server 会用它的所在目录作为相对路径 anchor
# （WIKI_DIR=../QuantumAtlas-Wiki 因此能解析到 %h/QuantumAtlas-Wiki）。
# 不要用 systemd 的 EnvironmentFile= 指令 —— 那个只把内容注入 env，
# 拿不到文件路径，server 就没办法做相对路径 anchor。
# %h 在 user-mode unit 里展开成 $HOME。
Environment=QATLAS_DOTENV=%h/QuantumAtlas/.env

# 仅在被 v4-only portproxy 包裹的 host (典型: WSL2 + Windows netsh)
# 才设这个。Plain Linux 云 VPS 不要打开，让 server 走 PocketBase 默认
# dual-stack v6 socket 同时服务 v4 + v6 client。
# Environment=QATLAS_FORCE_TCP4=1

WorkingDirectory=%h/QuantumAtlas

# pb_data 路径只通过 .env 里的 QATLAS_PB_DATA_DIR 控制（默认
# $XDG_DATA_HOME/qatlasd/pb_data），server 启动时自动转换为
# PocketBase 的 --dir= 参数。**不要**在 ExecStart 里硬写 --dir=...：
# cmdline 优先级最高，会让 .env 里的同字段失效，排障时容易踩。
ExecStart=%h/.local/bin/qatlasd serve --http=0.0.0.0:<HTTP_PORT>
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

Environment=QATLAS_DOTENV=/home/<USER>/QuantumAtlas/.env
# Environment=QATLAS_FORCE_TCP4=1

WorkingDirectory=/home/<USER>/QuantumAtlas
# pb_data 路径同 user-mode：靠 .env 的 QATLAS_PB_DATA_DIR 控制，
# 不要在 ExecStart 里写 --dir=...
ExecStart=/home/<USER>/.local/bin/qatlasd serve --http=0.0.0.0:<HTTP_PORT>
Restart=on-failure
RestartSec=5
KillSignal=SIGINT
TimeoutStopSec=15

# Hardening：read-only 系统目录 + 只把 stateful 路径打开写权限。
# ReadWritePaths 必须覆盖 .env 里所有非默认目录（RAW_DIR / DATA_DIR /
# PBDataDir / WikiDir），按实际部署调整。下面示例对应"全部走 XDG 默认"：
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=no
ReadWritePaths=/home/<USER>/QuantumAtlas /home/<USER>/.local/share/qatlasd /home/<USER>/QuantumAtlas-Wiki
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

按你在 §1 选的获取方式分支。**首次部署**直接走 §3.A 的
`qatlasd service install` 一键搞定（生成 unit + enable + start）。
**后续滚 binary** 只需要 binary 替换 + 重启 service：

**A. `go install` 直滚**——target 上一条命令：

```bash
# 在 target host 上跑
go install github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@latest
install -m 0755 ~/go/bin/qatlasd ~/.local/bin/qatlasd
qatlasd service restart       # 等价于 systemctl [--user] restart qatlasd
```

钉版本：`@latest` → `@v0.1.0` / `@<commit-sha>`。GitHub Action /
ansible / 任何远程执行框架同样适用。

**B. 自带 binary（B 或 C 路径产出的 `qatlasd`）**——target 已有
binary 时：

```bash
# binary 已用 pixi 或 CI 编出来；用你惯用的传输方式（scp / rsync /
# kubectl cp / S3 / artifact 下载）把它放到 target 的某个临时路径
# /tmp/qatlasd，然后在 target host 上跑：
install -m 0755 /tmp/qatlasd ~/.local/bin/qatlasd
qatlasd service restart
```

`qatlasd service restart` 跟 `systemctl [--user] restart
qatlasd` 完全等价（库内部就是调 systemctl）；任选其一不冲突。
读 systemd 状态用 `qatlasd service status` 或
`systemctl [--user] status qatlasd` / `journalctl [--user] -u
qatlasd`，都不需要 sudo。

### 5. .env 必填字段

参考 `.env.example`。Server 侧最小集（**只有真正想覆盖默认时才写**
`WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR`）：

```env
QATLAS_PUBLIC_URL=https://your-domain.tld
QATLAS_SERVER_HOST=0.0.0.0
QATLAS_SERVER_PORT=4200

# 显式覆盖示例（不写就走 XDG / sibling 默认）：
# QATLAS_WIKI_DIR=../QuantumAtlas-Wiki
# QATLAS_RAW_DIR=/srv/quantum-atlas/raw
# QATLAS_DATA_DIR=/srv/quantum-atlas/data
# QATLAS_PB_DATA_DIR=/var/lib/quantum-atlas/pb_data

GITHUB_CLIENT_ID=<oauth_app_client_id>
GITHUB_CLIENT_SECRET=<oauth_app_secret>
# 未来 admin 提权白名单，handler 待补；现在写了也不会生效。
# QATLAS_ADMIN_GITHUB_LOGINS=alice,bob
```

GitHub OAuth App callback URL 配 `https://your-domain.tld/api/oauth2-redirect`。

### 6. 从旧部署迁移到当前布局

如果之前 binary 装在 `/usr/local/bin/`、unit 写死 system-wide 路径、或
wiki / raw / data / pb_data 直接放在 git checkout 里——一次性迁移思路
见 [docs/migration-storage-layout.md](migration-storage-layout.md)，
该文档覆盖：

- 把 wiki / raw / data / pb_data 从仓库内搬到 `$XDG_DATA_HOME/qatlasd/`
- binary 从 `/usr/local/bin/` 挪到 `~/.local/bin/`
- systemd unit 调整 + 启动验证

每步都该有备份（`cp -a <path> <path>.bak-$(date +%s)`）。整个流程
**不写成提交进仓库的脚本**——下次迁移环境可能完全不一样，强迫维护者
重新读这一节比照本机情况自己拼脚本，更不容易把陈旧假设拷过去。

### 7. 对象存储（RustFS）

PDF / MinerU 输出等大 blob 走 S3 兼容对象存储而不是本地 `RAW_DIR`。
Go server 通过 `internal/objstore` 抽象层接 minio-go SDK，**填齐
`QATLAS_S3_*` 四字段就切 RustFS，留空就 fallback 本地 `RAW_DIR`**
（dev / CI 无外部依赖）。**注意：四字段是 all-or-nothing**——半填会
启动直接报错退出，避免 reader / writer 跑两套后端。

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
# 之后写进 server .env：
#   QATLAS_S3_ENDPOINT=https://raw.your-domain.tld
#   QATLAS_S3_BUCKET_PDF=qatlas-pdf
#   QATLAS_S3_BUCKET_MD=qatlas-md
#   QATLAS_S3_BUCKET_IMAGES=qatlas-images
#   QATLAS_S3_ACCESS_KEY_ID=<上面打印的>
#   QATLAS_S3_SECRET_ACCESS_KEY=<上面打印的>
# 重启 server，启动 log 会打印每个 bucket 一行 `raw store: S3 backend ...` 确认切换成功
```

切到 S3 后端后，对应的 presigned URL（5 min TTL，绕过 server 节省 VPS 带宽）
由 server 内部签发；本地 RawDir 后端继续走 ServeFile。客户端拿到的资源 URL
不区分后端，redirect 由 server 透明处理。

边缘 Caddy 多加一个站点把 `raw.your-domain.tld` 反代到 RustFS `:9000`
即可，模板见 [`deployment/rustfs.md`](rustfs.md) 对应章节。

## 推荐的单机生产目录

代码 + 配置在 git checkout，stateful 状态走 XDG_DATA_HOME（用户级）或
`/var/lib/`（系统级）。默认值不需要在 `.env` 里显式写——`server` 启动
时按 `$XDG_DATA_HOME` / `$HOME` 自动算出来。只有当默认值不合适（共享
盘 / FHS / 独立分区）才在 `.env` 里覆盖。

```env
# 一切都跑默认时，server 侧 .env 只需要这点：
QATLAS_PUBLIC_URL=https://atlas.example.com
QATLAS_SERVER_HOST=127.0.0.1
QATLAS_SERVER_PORT=4200
NEO4J_URI=bolt://127.0.0.1:7687

# 想覆盖默认时：
# QATLAS_WIKI_DIR=../QuantumAtlas-Wiki                # 默认就是这个
# QATLAS_RAW_DIR=/srv/quantum-atlas/raw               # 默认 XDG，FHS 覆盖
# QATLAS_DATA_DIR=/srv/quantum-atlas/data
# QATLAS_PB_DATA_DIR=/var/lib/quantum-atlas/pb_data
```

> 旧名（`WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` / `SERVER_HOST` / `SERVER_PORT` / `USER_HEADER`）仍作 alias 保留，新部署推荐用 `QATLAS_*` 前缀。`NEO4J_*` / `OPENAI_*` / `ANTHROPIC_*` / `MINERU_*` 等第三方 SDK 标准名保持原样。v0.19.0 起 `QATLAS_SERVER_URL` 已重命名为 `QATLAS_PUBLIC_URL`（旧名 `QATLAS_SERVER_URL` / `PUBLIC_BASE_URL` 在服务端**不再读**——名字改成 `QATLAS_PUBLIC_URL` 是为了准确反映"我对外公布的 canonical URL"语义；client 完全不读 env，跟这一项无关）。

建议：

- 应用仓库按 release tag 或受控分支部署。
- Wiki 仓库单独 checkout，并允许更高频更新；server 侧 checkout 应保持干净，只通过 `git pull --ff-only` 消费远端内容。
- 运行 QuantumAtlas 的服务用户默认只需要读取 `WIKI_DIR`；如果启用 `/api/wiki/sync/pull`，还需要对该 Git checkout 有 fast-forward 更新权限。服务端不会生成或修改 Wiki 页面，Wiki 内容修改应在用户端或独立的 `QuantumAtlas-Wiki` checkout 中完成。
- 运行 QuantumAtlas 的服务用户应对 `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` 有写权限。三者默认都落在 `$XDG_DATA_HOME/qatlasd/`（即 `$HOME/.local/share/qatlasd/`）下，正常的 systemd `User=<svc>` 已经自动满足；只在显式覆盖到 FHS / 独立分区时检查权限。
- 内容生产、LLM 生成、人工编辑和审阅走 `QuantumAtlas-Wiki` 的普通 Git 流程；QuantumAtlas server 不提供 push API，也不通过 Web UI 直接写 Wiki 页面。
- 若 `/api/wiki/sync/status` 提示 Wiki checkout 不在 `main` 或 `master`，应检查部署分支是否符合预期。
- Neo4j 仅对后端服务暴露，不直接开放到公网。
- 公开访问统一走 `QATLAS_PUBLIC_URL`。

## 核心环境变量

字段语义、是否必填、是否分 client/server 角色等完整说明以
[`.env.example`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.env.example) 顶部对照表为准（同时也是 client / server
共享的 canonical 文档；Go server 的运行时默认在
`internal/config/config.go`）。本节只列出公网部署最容易踩的几个：

| 变量 | 何时需要 | 备注 |
|---|---|---|
| `QATLAS_PUBLIC_URL` | 必填 | server 自报的对外 canonical URL；用于构造 OAuth 回调、外链等需要绝对 URL 的地方（反代场景必备——server bind 在 localhost，必须显式告诉它"我对外是谁"）。v0.19.0 改名（旧名 `QATLAS_SERVER_URL`），跟 client 侧的 `server_url:` YAML 字段（"我要联系的 server"）在概念上独立 |
| `QATLAS_SERVER_HOST` / `QATLAS_SERVER_PORT` | 默认 `127.0.0.1:4200` | 直接面向公网通常改 `0.0.0.0:<port>`，反代场景保留 `127.0.0.1` |
| `NEO4J_URI` / `NEO4J_USER` / `NEO4J_PASSWORD` | 启用图谱时必填 | 不连图库可留空 |
| `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` | 启用 GitHub OAuth 登录时必填 | 启动时由 `internal/auth/oauth.go` 注入 users collection |
| `QATLAS_USER_HEADER` | 上游反代/SSO 注入审计身份头时设 | 不参与鉴权，仅用于日志 |
| `QATLAS_FORCE_TCP4` | WSL2 + Windows netsh portproxy 场景设 | 普通 Linux VPS 不要打开 |

其余字段（`QATLAS_WIKI_DIR` / `QATLAS_RAW_DIR` / `QATLAS_DATA_DIR` /
`QATLAS_PB_DATA_DIR` / `QATLAS_S3_*` / `MINERU_*` / `OPENAI_*` 等）都在
`.env.example` 里有详细注释，按需取消注释即可。

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
  的写口（papers / wiki sync），强制 scope opt-in；`sessionGuard` 给"PAT
  不可调"的写口（PAT 自管理本身、admin 操作），只接受 session token。

如需在边缘补一层 IP/路径 ACL、按域名分流多服务、或做 raw 对象存储反代
（`raw.your-domain.tld` → RustFS），Caddy 模板下面给出。

### 路径分类与对应处理

| 路径 | 鉴权层 | 反代怎么写 |
|---|---|---|
| `/api/health` | open | 直接 reverse_proxy；监控可读（返回 `{code, message, data:{status, version, uptime_seconds, checks{rawstore, neo4j, wiki}}}`） |
| `/install-qatlasd.sh` | open | 直接 reverse_proxy；公开的 `curl \| sh` 安装脚本 |
| `/{path...}`、`/_/`、`/auth-with-oauth2` 等 SPA + PocketBase 内置 | open / 自管 | 直接 reverse_proxy；OAuth 由 server 自己处理 |
| `/api/wiki/...`、`/api/pages`、`/api/search`、`/api/stats`、`/api/graph/*` | server 内 `authGuard + scope:read` | 直接 reverse_proxy |
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

client 端如果要走 IP + 非标端口入口，需要 `qatlas --insecure ...` 或 .env
里 `QATLAS_INSECURE=1` 跳过证书校验；想保留真证书验证又用第二条线路，
就在本机 hosts 把 `atlas.example.com` 覆盖到对应 IP，TLS 走 SNI 仍然信
任原证书。

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
- 如果启用了 MinerU 并需要它回拉 PDF，`QATLAS_PUBLIC_URL` 必须能从
  MinerU 所在环境访问到。
