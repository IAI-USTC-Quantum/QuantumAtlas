# `qatlasd` 服务端 CLI 参考

`qatlasd` 是本仓的 Go 服务端；独立 Python 客户端 `qatlas` 来自 `qatlas-cli`，两者命令与配置文件不同。预编译发行程序内嵌完整 UI，源码安装按版本自动补齐 UI，详见[安装说明](install.md)。

```text
qatlasd [global flags] <subcommand> [args...]
```

当前主命令为 `config`、`serve`、`service`、`pat`、`storage`、`papers`、`openalex`、`users`、`downloader`、`superuser`。以安装版本的 `--help` 为准，不假定 PocketBase 的所有可选命令都已注册。

## 全局参数与配置

| 参数 | 用途 |
|---|---|
| `--config <path>` | YAML 文件；默认 `~/.qatlas/config.yaml` |
| `--dir <path>` | 显式覆盖 PocketBase 数据目录；未给时由 YAML `paths.pb_data_dir` 注入 |
| `--dev` | PocketBase 开发日志/SQL 输出；不要在生产启用 |
| `--encryptionEnv <name>` | PocketBase 设置加密 key 所在的环境变量名，属于上游明确保留的接口 |
| `--queryTimeout <seconds>` | PocketBase SELECT 查询超时，默认 30 秒 |
| `--version` / `version` | 打印服务版本；独立于配置、数据库与 UI 下载 |
| `--help` / `-h` | 命令帮助 |

业务配置不再提供逐字段的 env 或 CLI 覆盖。`--postgres-dsn`、`--system-pat`、`--raw-dir`、`--dotenv-path` 等旧参数已移除；请使用[当前 YAML 配置](server-config.md)。凭据不要放入命令行参数或 shell 历史。

## `config`：初始化和检查 YAML { #config }

```bash
qatlasd config init
qatlasd config init --config /path/to/config.yaml
qatlasd config path --config /path/to/config.yaml
qatlasd config show --config /path/to/config.yaml
```

`init` 默认拒绝覆盖；只有明确要替换且已备份时才使用 `--force`。新文件为 `0600`。`show` 默认脱敏，非默认服务用户还需核对文件读权限。默认模板和仓库根 `config.example.yaml` 保持同步，但启动逻辑始终以 Go 配置结构与校验为准。

## `serve`：启动 HTTP server { #serve }

```bash
qatlasd --config /path/to/config.yaml serve
qatlasd --config /path/to/config.yaml serve --http=127.0.0.1:4200
```

- 应用默认 `http_addr` 为 `127.0.0.1:4200`；PocketBase 的通用帮助可能列出其自身默认值，真正的应用启动会注入 YAML 设置。
- `--http`、`--https`、`--origins` 等继承选项见 `serve --help`；显式 `--http` / `--dir` 胜过对应 YAML 值。
- 无内嵌资源时，serve 在监听之前校验/准备精确版本 UI。其他命令不为 UI 联网；本地 dev 构建需要先完成完整 UI 构建并使用 `-tags embedui`。
- 后端连接、迁移和后台任务只在服务启动路径按配置初始化。不要为了查看版本而启动生产服务。
- OAuth、PG、S3 等真实配置写 YAML；Docker 挂载该文件，不使用旧 `--env-file` 业务配置方式。

## `service`：管理 systemd / launchd 服务

```text
qatlasd service <install|uninstall|start|stop|restart|status>
```

### `service install`

```bash
# 只看将要写入的配置（非TTY也需mode和force）
qatlasd service install --mode user --config /path/to/config.yaml --dry-run --force
# 审阅并授权后，去掉dry-run才会注册并启动服务
qatlasd service install --mode user --config /path/to/config.yaml --force
```

| 参数 | 默认/行为 |
|---|---|
| `--name` | `qatlasd` |
| `--mode user\|system` | TTY 询问；非 TTY 必填 |
| `--config` | 已存在的默认 YAML 或显式路径；写进 unit 的程序参数 |
| `--bind` | `127.0.0.1:4200`，作为 serve 的 `--http` |
| `--dry-run` | 只显示，不写 unit、不 reload、不启动 |
| `--force` | 跳过确认、允许覆盖 unit；不是绕过 YAML 校验 |

system mode 需要相应权限；从预定服务用户的 shell 运行 `sudo /absolute/path/qatlasd ... --mode system`，不要混用 `sudo ... --mode user`。具体身份/属主与迁移注意事项见[安装与 service 注册](install.md)。安装器本身不注册或重启服务。

### systemd hardening { #systemd-hardening }

生成的 unit 包括 `NoNewPrivileges`、`PrivateTmp`、`ProtectSystem=full` 等，数据路径来自已解析的服务身份与配置路径。`ReadWritePaths` 中的目录要预先存在；自定义 YAML 存储路径或额外 sandbox 要自行核对。无内嵌 UI 的安装还需要运行用户的缓存目录可写，见安装文档。

### 管理已安装服务

```bash
qatlasd service status --mode user
qatlasd service restart --mode user
# system服务明确选system，不要误查user unit
sudo qatlasd service status --mode system
```

`stop` / `uninstall` 会影响正在运行的服务；卸载不会删除配置、数据库、对象存储或 UI 缓存。它们不是离线开发检查命令。

## `pat`：本机 PAT 维护

直接访问此服务的数据目录，和浏览器 `/api/pat` 的操作边界不同。用 `qatlasd pat --help` 查看当前参数；示例：

```bash
qatlasd --config /path/to/config.yaml pat scopes
qatlasd --config /path/to/config.yaml pat list --json
# mint / revoke 会创建或删除凭据，只在明确授权后执行
```

`mint` 的明文仅输出一次，应安全保存；不要提交日志或将它用于不受控的 Vite 代理。scope 以当前输出为准，不再把已迁出的 theorem/wiki scope 当作主仓必备项。

## `storage`、`papers` 与 `openalex`

- `storage`：S3/RustFS 维护，包括版本清理。`storage prune` 默认预览；真正删除需要明确 `--yes`，见 [RustFS](rustfs.md#prune)。
- `papers`：PostgreSQL registry 维护、对象存储导入/迁移相关操作；具体命令见 `qatlasd papers --help`。
- `openalex`：OpenAlex corpus 导入；连接数据库、下载快照或创建索引会产生实际资源消耗，不属于默认单元测试。

```bash
qatlasd --config /path/to/config.yaml storage prune --older-than 90d --keep-last 5
qatlasd --config /path/to/config.yaml papers --help
qatlasd --config /path/to/config.yaml openalex --help
```

## `downloader probe`：明确的在线诊断 { #downloader-probe }

`qatlasd downloader probe` 会对论文执行真实的 OA/API/出版商策略链，可能调用浏览器、代理或 LLM。它不是本地 fixture，不因名字为 probe 就无需授权。需要 `paper_access.enabled` 等相关配置，参数见当前版本的 `downloader probe --help`，行为见[下载器说明](downloader.md)。

不要把在线 probe 加入普通 Go CI；其接入目标、凭据和可消耗额度须由操作者明确指定。

## `users`、`superuser` 与数据库迁移

`users` 检查此实例的 PocketBase 用户；`superuser` 管理 PocketBase 管理员。创建/修改管理员需要运维授权，密码不要写成可复制的固定示例。

主仓的 PocketBase 迁移在各领域 Go 包中注册；PostgreSQL goose SQL 位于 `internal/registry/migrations/`。当前主命令没有通用的 `qatlasd migrate down` 回滚入口，也没有主仓 `pb_migrations/` 源码目录。数据库降级必须按版本说明和一致性备份处理，换回旧 binary 不代表回滚了 schema。

## 查看准确帮助

```bash
qatlasd --version
qatlasd --help
# 子命令帮助可能需要可读YAML，但不会执行serve/管理动作
qatlasd --config /path/to/config.yaml serve --help
qatlasd --config /path/to/config.yaml service install --help
```

开发与测试入口见[贡献指南](../contributing.md)；独立客户端命令见 [`qatlas` 参考](../client/cli-qatlas.md)。
