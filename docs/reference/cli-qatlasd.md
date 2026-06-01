# `qatlasd` 服务端 CLI 参考

`qatlasd` 是 Go binary，单文件 ~30 MB，自带 SPA + PocketBase + SQLite。继承 PocketBase 全部命令 + QuantumAtlas 自有的 `service` / `pat` / `storage` 子命令树。

```
qatlasd [global flags] <subcommand> [args...]
```

## 全局 flag（PocketBase 内置）

| Flag | 默认 | 含义 |
|---|---|---|
| `--dir <path>` | `<binary_dir>/pb_data` | PocketBase data 目录（自动从 `QATLAS_PB_DATA_DIR` 注入）|
| `--debug` | false | 详细日志 |
| `--encryptionEnv <var>` | — | DB 加密 key 来源 env var |
| `--queryTimeout <sec>` | 30 | SQL 查询超时 |
| `--version` | — | 打印版本 |

!!! tip "`--dir` 自动注入"
    server 启动时会按 `QATLAS_PB_DATA_DIR` 自动把 `--dir=<path>` 插到 cobra 命令行第一个位置——所以你**不需要**也**不应该**在 systemd unit 的 ExecStart 里硬写 `--dir=`。直接在 `.env` 改 `QATLAS_PB_DATA_DIR=` 就好。

---

## `serve`：启动 HTTP server

```
qatlasd serve [--http=<host:port>] [--dev]
```

| Flag | 默认 | 含义 |
|---|---|---|
| `--http <host:port>` | `127.0.0.1:4200`（从 `QATLAS_HTTP_ADDR` / `QATLAS_SERVER_HOST/PORT` 自动注入）| HTTP bind |
| `--dev` | false | 启用 PocketBase dev 模式（绕过一些安全检查，**不要在生产用**）|

例子：

```bash
# 默认 127.0.0.1:4200
qatlasd serve

# 监听公网（前面必须有反代）
qatlasd serve --http=0.0.0.0:4200

# 显式 .env 路径
QATLAS_DOTENV=/etc/quantum-atlas/.env qatlasd serve
```

---

## `service`：管理 systemd / launchd 服务

跨平台 service 管理（用 [kardianos/service](https://github.com/kardianos/service)）。

```
qatlasd service <install|uninstall|start|stop|restart|status>
```

### `service install`

```
qatlasd service install [--name qatlasd] [--mode user|system]
                              [--dotenv-path <path>] [--bind <host:port>]
                              [--dry-run] [--force]
```

| Flag | 默认 | 含义 |
|---|---|---|
| `--name` | `qatlasd` | service unit 名（Linux 上是 `<name>.service`）|
| `--mode user\|system` | TTY 下交互式询问；非 TTY 必填 | user-level systemd unit vs system-level（system 需要 sudo）|
| `--dotenv-path` | `$QATLAS_DOTENV` → `~/QuantumAtlas/.env` → `./.env` | 写入 unit 的 `Environment=QATLAS_DOTENV=` |
| `--bind` | `127.0.0.1:4200` | `serve --http=` 的值 |
| `--dry-run` | false | 渲染 unit 到 stdout，不写盘 |
| `--force` | false | 已有同名 unit 直接覆盖（**非 TTY 必填**）|

行为：

1. 解析 mode（user / system）
2. 解析 .env 路径
3. 渲染 unit 内容（含 [hardening](#systemd-hardening)）
4. TTY 模式下问 `[Y/n]` 确认
5. 写 unit + `systemctl daemon-reload` + `systemctl start`

**非交互模式**（CI / `curl|sh` 后跑）：

```bash
sudo qatlasd service install \
    --mode system \
    --dotenv-path /etc/quantum-atlas/.env \
    --force
```

### `service install` 的 systemd hardening { #systemd-hardening }

Linux 上渲染的 unit 自带这些 sandbox 选项：

```ini
[Service]
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=no
LockPersonality=true
RestrictRealtime=true
ReadWritePaths=<.env dir> <wiki dir> <data dir>
```

!!! warning "ReadWritePaths 缺一会让 restart 失败"
    `ReadWritePaths=` 里列的**任何目录不存在都会让 service restart 拿 `status=226/NAMESPACE`**。改 `.env` 的存储路径后必须确保新路径存在 + `systemctl daemon-reload`。

### `service start|stop|restart|status`

```bash
qatlasd service start
qatlasd service stop
qatlasd service restart
qatlasd service status
```

跟 `systemctl <op> qatlasd` 100% 等价（library 是 systemctl 的薄 wrapper）。

### `service uninstall`

```bash
qatlasd service uninstall
```

停服务 + 删 unit 文件 + `daemon-reload`。**不会删 pb_data / raw / data**——那是数据，需要你手动 trash-put。

---

## `pat`：直接操作 PAT（救急用）

绕开 `/api/pat` 直接读写 pb_data。**需要在 server 主机上跑**（因为依赖 PocketBase DB 文件）。

```
qatlasd pat <mint|list|revoke|scopes>
```

### `pat mint`

```
qatlasd pat mint --user <email|id> --name <name>
                       --scopes <s1,s2,...> --expires-in-days <N>
                       [--description <text>]
```

| Flag | 必填 | 含义 |
|---|---|---|
| `--user` | ✅ | 目标用户（email 或 users record id）|
| `--name` | ✅ | token 显示名（≤80 字符）|
| `--scopes` | ✅ | 逗号分隔的 scope，如 `papers:write,shares:write` |
| `--expires-in-days` | ✅ | 1–365 |
| `--description` | ❌ | 备注（≤200 字符）|

输出包含明文（仅一次）。

### `pat list`

```
qatlasd pat list [--user <email|id>] [--json]
```

按 user 过滤；`--json` 出机读格式。**不包含明文 / 哈希**——只有 prefix + 元数据。

### `pat revoke`

```
qatlasd pat revoke <id>
```

硬删除该 PAT record，下次该 token 调任何端点立刻 401。

### `pat scopes`

打印当前编译进 binary 的 scope 词表（同 `GET /api/pat/scopes` 返回内容，但不用起 HTTP）：

```bash
qatlasd pat scopes
# papers:write    Upload paper PDFs / Markdown and run MinerU jobs
# shares:read     List share tokens you created
# shares:write    Create and revoke share tokens (includes read)
```

---

## `storage`：对象存储维护

仅 S3 / RustFS 后端可用。

```
qatlasd storage <prune> [options...]
```

### `storage prune`

删除 noncurrent S3 object versions（即被 `--overwrite` 覆盖掉的旧版本）。

```
qatlasd storage prune [--prefix <key-prefix>] [--older-than <duration>]
                            [--keep-last <N>] [--yes] [--dry-run] [--json]
```

| Flag | 默认 | 含义 |
|---|---|---|
| `--prefix <path>` | "" (整个 bucket) | 只处理这个 key 前缀 |
| `--older-than <dur>` | "" (不限) | 仅删比此年龄更老的版本（Go duration 或 `30d` / `1y`）|
| `--keep-last <N>` | 0 (不限) | 每个 key 保留最近 N 个 noncurrent 版本 |
| `--yes` | **false** | 真删（不带 = dry-run）|
| `--dry-run` | true | 干跑预览；**没有 `--yes` 即使 `--dry-run=false` 也不删** |
| `--json` | false | 每行一个 JSON 对象（机读）|

!!! warning "默认 dry-run + `--yes` 双保险"
    `storage prune` 默认 dry-run。要真删必须 `--yes`，避免手抖删数据。

**示例**：

```bash
# 预览：删 90 天前的所有 noncurrent
qatlasd storage prune --older-than 90d

# 真删，每个 key 保留最近 5 个 noncurrent，超出的删
qatlasd storage prune --keep-last 5 --yes

# 只处理 2025-11 的 cohort
qatlasd storage prune --prefix pdf/2511/ --older-than 30d --yes
```

**永远不会删的对象**：current version、delete marker、metadata sidecar（LocalStore）。

详见 [RustFS 部署](../deployment/rustfs.md#prune)。

---

## `superuser`：PocketBase 内置

```
qatlasd superuser upsert <email> <password>
qatlasd superuser create <email> <password>
qatlasd superuser delete <email>
```

管理 PocketBase admin UI 登录（`/_/`）。日常运维**不依赖** admin UI，仅在需要看 DB 结构或临时调试时用：

```bash
# 改密码（已有就改，没有就建）
qatlasd superuser upsert admin@example.com NewSecurePass!
```

---

## `migrate`：PocketBase 数据库迁移

```
qatlasd migrate up
qatlasd migrate down [N]
qatlasd migrate collections
```

迁移文件在 `pb_migrations/`（迁移目录默认在 pb_data 旁）。**每次启动 server 时自动跑 pending migrations**，所以手工调用通常不必要。

---

## `--help` / `-h`

每个 subcommand 都有 `--help`，输出真实当前版本的 flag 集合（这份文档可能滞后于代码）：

```bash
qatlasd --help
qatlasd serve --help
qatlasd service install --help
qatlasd storage prune --help
```
