# 备份与升级

QuantumAtlas 有三层状态，各自的备份 / 升级语义不同：

| 层 | 在哪 | 怎么备份 | 怎么恢复 | 重要性 |
|---|---|---|---|---|
| **PocketBase pb_data** | 本机 SQLite | `cp` 或 PocketBase backup API | 复制回去 | 高（用户 / PAT 记录）|
| **RustFS bucket** | 对象存储 | bucket versioning + offsite mirror | 用 noncurrent version 回滚 | 高（PDF / Markdown / 元数据）|
| **PostgreSQL** | 本机 / 托管 | `pg_dump` | `pg_restore` / `psql` | 高（paper registry + OpenAlex corpus；registry 可从 bucket 重建，corpus 重灌成本高）|

## pb_data 备份

### 离线 copy（最简）

```bash
# 1. 停 server
sudo systemctl stop qatlasd

# 2. 直接 cp（mode preserve）
sudo cp -a /home/<USER>/.local/share/qatlasd/pb_data \
          /var/backups/pb_data-$(date +%F)

# 3. 起回来
sudo systemctl start qatlasd
```

5 秒 downtime。**周期：周一次 + 大动作前**。

### Hot backup（PocketBase API）

PocketBase 内置 backup API 可以在线热备：

```bash
# 用 admin session
curl -X POST https://<server>/api/backups \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"name":"weekly-$(date +%F).zip"}'

# 列备份
curl https://<server>/api/backups \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# 下载
curl -OJ https://<server>/api/backups/weekly-2026-05-29.zip \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

备份文件落到 `pb_data/backups/`。**记得定期 rotate**（PocketBase 自己不删旧的）。

### 自动定时（cron）

```{code-block} bash
:caption: /etc/cron.weekly/qatlas-pb-backup

#!/bin/bash
set -euo pipefail
DEST=/var/backups/qatlas/pb_data-$(date +%F).tar.gz
mkdir -p $(dirname "$DEST")
systemctl stop qatlasd
tar czf "$DEST" -C /home/<USER>/.local/share/qatlasd pb_data
systemctl start qatlasd
# 保留最近 4 周
find /var/backups/qatlas/ -name 'pb_data-*.tar.gz' -mtime +28 -delete
```

## RustFS bucket 备份策略

### Bucket versioning（自动 enabled）

server 启动时通过 `objstore.S3Store.EnsureVersioning` 自动开启 bucket versioning（详见 [RustFS](rustfs.md#versioning-qatlas-self-manages)）。意味着：

- 每次 `--overwrite` 上传 → 旧版本变成 noncurrent，**永久保留**
- 误删 → 留下 delete marker，但**原版本仍在**
- 用 `mc cp --version-id <vid>` 或 `aws s3api get-object --version-id` 回滚

**这本身就是一层备份**——大多数误操作可以靠 versioning 自救。

### 跨地域 mirror（可选）

如果想要离 RustFS 主机的离线备份，用 `rclone sync` 定时跑：

```{code-block} bash
:caption: cron weekly mirror

rclone sync myrustfs:qatlas-raw  \
            backup-s3:qatlas-raw-mirror \
            --transfers 8 --checksum
```

或用 RustFS 自己的 [replication](https://rustfs.com/docs/replication/) 功能（如果支持）。

### Prune noncurrent

定期跑 `qatlasd storage prune` 防止 noncurrent 堆爆：

```bash
# 干跑预览：90 天前 + 保留最近 5 个
qatlasd storage prune --older-than 90d --keep-last 5

# 满意了真删
qatlasd storage prune --older-than 90d --keep-last 5 --yes
```

详见 [RustFS / storage prune](rustfs.md#qatlasd-storage-prune)。

## PostgreSQL 备份

```bash
# 在线 dump（custom format，可并行恢复；不需要停库）
pg_dump -Fc -d qatlas -f /var/backups/qatlas-pg-$(date +%F).dump

# 恢复
pg_restore -d qatlas --clean /var/backups/qatlas-pg-YYYY-MM-DD.dump
```

**paper registry（papers / paper_assets）是可从对象存储重建的派生索引**
（`qatlasd papers sync --full --from-rustfs`），所以真正值钱的是 OpenAlex
corpus（重灌 ~10⁸ 行成本高）。预算紧张时可以只 dump corpus 相关的表，或干脆
接受"灾难后重跑 `qatlasd openalex bootstrap-pg`"。

## 升级 binary

### 标准流程：先说明与备份，再替换

```{admonition} 不要把新格式当作已经发布
:class: warning

`TAG=vX.Y.Z` 必须替换为**已经采用新流程、附件已公开**的目标 tag。
这套流程变更本身不是一次新发行，历史 `v0.34.0` 不会补发这些新格式产物。
不使用 `latest` 掩盖迁移窗口，也不重发旧 tag。
```

**1. 先读目标版本说明、核对兼容性。**

```bash
TAG=vX.Y.Z
VERSION=${TAG#v}
gh release view "$TAG" --repo IAI-USTC-Quantum/QuantumAtlas
```

逐版阅读从当前版本到目标版本的 release notes，确认 YAML / PocketBase /
PostgreSQL schema / 对象存储变更、停写要求及恢复办法。即使都是 0.x 或 patch
版本，也不能假定没有迁移。迁移和停机耗时取决于数据规模，不保证固定秒数。

**2. 准备同一 tag 的安装器，但还不要替换。** 先按[安装文档](install.md)
检查 curl / GNU wget、tar、SHA256 工具等依赖；BusyBox wget 需改用 curl。
请求 / 归档步骤与候选程序版本检查都有有限超时，按需只为安装器调用设置超时覆盖。

```bash
curl -fL --proto '=https' --proto-redir '=https' \
  "https://raw.githubusercontent.com/IAI-USTC-Quantum/QuantumAtlas/$TAG/cmd/qatlasd/install-qatlasd.sh" \
  -o install-qatlasd.sh
less install-qatlasd.sh
# 审阅后，先完成下面的备份，再执行安装。
```

旧实例的 `/install-qatlasd.sh` 是旧 binary 内嵌的旧格式安装器，**不能拿它装
新格式归档**。首次升级必须从目标新 tag 取脚本；首次升级后内嵌脚本才随 binary
切换。新脚本不兼容旧式附件，不能用它重新安装旧格式 release。

**3. 做一致性备份并保留旧程序 / 配置。**

按本文前面的备份方法保存实际部署的 `pb_data`、PostgreSQL 及对象存储，检查
备份可读、恢复路径和可接受的数据损失窗口。复制 SQLite 目录前停服务，或使用
受支持的在线备份；不能把运行中的 `cp -a pb_data` 当作一致性备份。
另外保存当前 binary、`config.yaml`、unit / drop-in（含实际 `ExecStart` 路径），
并记录旧程序 `--version`。备份应放在不被此次替换覆盖的位置，密钥受访问控制。

system unit 示例（user unit 对应 `systemctl --user stop qatlasd`）：

```bash
sudo systemctl stop qatlasd
# 现在按已审阅的备份方案完成停写 / pb_data 复制与其他状态备份。
# 任一备份失败，停止升级；不要继续执行安装与新版本启动。
```

**4. 备份完成后，安全替换 binary。** 以下在有安装目录写权限的账户下运行；
`--dir` 必须与 unit 的实际 binary 目录一致：

```bash
sh install-qatlasd.sh --version "$TAG" --dir "$HOME/.local/bin"
"$HOME/.local/bin/qatlasd" --version  # 应为 qatlasd version X.Y.Z（去掉 tag 的 v）
```

新流程采用 GoReleaser v2.18.1 默认归档
`qatlasd_<version>_<os>_<arch>.tar.gz`（三平台 `linux/amd64`、`linux/arm64`、
`darwin/arm64`），内含唯一普通 binary `qatlasd`。安装器校验同 tag
`qatlasd_<version>_checksums.txt`，严格检查 tar 成员，在**目标文件系统**暂存，
精确执行 / 核验 `--version` 后才 atomic rename。下载、SHA256、tar、版本或
替换任一步失败，不改旧 binary / 配置；停止排查，不要强行启动未经验证的目标。
脚本不自动 sudo、注册或重启服务，也不会重写 YAML。

同源 checksum 只是完整性校验，不是来源签名，也不证明源码 / 依赖安全或程序无 bug。
后续采用新 workflow 的版本在 GoReleaser 发布后生成 GitHub 签名构建证明，
可用 `gh attestation verify` 核验；已发布的 `v0.35.0-rc.1` 不追溯补签。
证明失败时 Release 可能已公开，先核对实际状态；校验方法见[安装文档](install.md)。

**5. 显式启动新版本并验证。** 若说明要求新增 YAML 字段，按说明合并，
**不要用 `config init --force` 覆盖已有配置**。若旧 unit 仍使用 dotenv，先迁移到
`qatlasd --config /path/to/config.yaml serve ...`，移除旧业务环境变量，检查文件
属主 / 权限；修改 unit 后 `daemon-reload`。重新执行 `service install` 会启动服务，
须等到准备就绪，而不是只为“生成模板”随手执行。

```bash
sudo systemctl start qatlasd
sudo systemctl status qatlasd --no-pager
journalctl -u qatlasd -n 50
curl -fsS http://127.0.0.1:4200/api/health | jq .data.version
```

检查实际运行版本、迁移日志、健康检查详情和首页 / JS / 文档。`--version` 成功
只验证程序可执行，不等于迁移或业务已通过；不要只等待固定几秒就宣告升级成功。

### Go 源码安装与 UI 缓存

普通 `go install github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@vX.Y.Z`
不需要 Node / Sphinx；Git / 模块仅含源码。升级时仍钉明确的公开新 tag，并将
候选程序装到独立暂存位置核验，不让 `go install` 直接覆盖 unit 正在引用的文件。
版本优先取有效 `main.version`，其次 Go build info `Main.Version`，显示去前导 `v`。

无内嵌 UI 的程序首次 `serve` 会下载：

```text
https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases/download/v<version>/qatlasd_<version>_web.zip
https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases/download/v<version>/qatlasd_<version>_checksums.txt
```

经过 SHA256 + zip comment 版本及资源结构验证后，写入运行用户的
`os.UserCacheDir()/qatlas/ui/v<version>/{bundle.zip,sha256}`。后续启动可按版本
校验并离线使用；升级使用新缓存，旧缓存不能代替新版本资源。可在隔离数据目录
和回环端口验证新版本，但不要让验证实例连接生产库或共享正在使用的 `pb_data`。

`dev` / `(devel)` / pseudo-version 不回退 latest；使用已公开且带资源的新 tag，
或开发者先构建 Sphinx 两站与 npm 全 UI，再 `go build -tags embedui ./cmd/qatlasd`。
官方预编译程序已内嵌和 zip 相同的资源，**首次启动不需要下载 UI**。
两种资源通过统一 `fs.FS` 提供 SPA / 文档，既有业务 YAML 不需要资源配置。
UI 缓存可重建，不是数据库备份；若保留旧缓存用于离线恢复，也必须保留完整
`bundle.zip` 与 `sha256`，不能混用版本或跳过验证。

### Rollback 的边界

**保留旧程序不等于数据库回滚。** PocketBase / PostgreSQL 的 schema 或数据
迁移没有通用的向后兼容保证，小版本也不能默认直接换回 binary。

1. 先停写 / 停服务，保留故障后的数据快照与日志，避免直接删除现场。
2. 根据该次 release notes 判断旧程序能否读取当前数据；没有明确保证时，
   先在隔离环境演练恢复升级前的一致性备份（可能同时涉及 SQLite、PG 和对象版本）。
3. 需要回退程序时，使用之前保存并核验过的旧 binary，或该旧 release 对应的安装
   方法；不要用新格式安装器下载旧格式附件。依然在目标文件系统暂存、核对版本后
   原子替换，配置 / unit 同步恢复到兼容状态。
4. 确认属主、权限、资源版本与服务配置后再启动，并重新做业务验证。

恢复升级前备份可能丢失升级后的写入，跨存储层恢复须协调同一个一致性时间点。
不能仅凭版本号大小决定只恢复 `pb_data`、不检查 PostgreSQL 或对象存储。

## 灾难恢复演练

每季度跑一次：

1. 起一台新 VPS
2. 按[安装文档](install.md)选定公开 tag，从同 tag 下载、审阅并执行安装器；若恢复旧格式发行，使用其对应产物 / 安装方式
3. 恢复 pb_data：`tar xzf pb_data-latest.tar.gz -C <data_dir>`
4. 恢复 PostgreSQL：`pg_restore` 最近的 dump（或建空库让 goose migrations 重建 schema）
5. 恢复受保护的 `config.yaml`，在隔离演练环境指向恢复出的 RustFS bucket 和 PostgreSQL，避免演练实例写生产状态
6. 检查权限后，从预定运行用户的 shell 执行 `sudo qatlasd service install --mode system --config /etc/quantum-atlas/config.yaml --force`（会启动服务）
7. 走 [健康检查 checklist](health-and-monitoring.md#自检-checklist部署后立刻跑)

完整恢复时间应该 ≤ 30 分钟。

## 备份策略推荐组合

| 资源 | 频率 | 工具 | 保留 |
|---|---|---|---|
| pb_data | 每天（cron）| `tar czf` | 4 周 + 月度永久 |
| RustFS bucket | 实时（versioning）| 自带 | 永久；prune 跑 90 天 + keep-last 5 |
| RustFS bucket（offsite）| 每周 | `rclone sync` | 4 周 |
| PostgreSQL | 每天（cron）| `pg_dump -Fc` | 4 周 + 月度永久 |

## 不要忘了备份的东西

- `config.yaml`（含 GitHub OAuth secret / PostgreSQL DSN / RustFS svcacct）—— 存到 password manager / vault，保持密钥访问控制；服务通过 `--config` 读取，不使用 `.env`
- systemd unit（如果改过 default）—— commit 进运维仓库
- Caddy / nginx 配置 —— 同上
