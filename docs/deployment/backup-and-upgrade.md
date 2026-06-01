# 备份与升级

QuantumAtlas 有三层状态，各自的备份 / 升级语义不同：

| 层 | 在哪 | 怎么备份 | 怎么恢复 | 重要性 |
|---|---|---|---|---|
| **PocketBase pb_data** | 本机 SQLite | `cp` 或 PocketBase backup API | 复制回去 | 高（用户 / PAT / share 记录）|
| **RustFS bucket** | 对象存储 | bucket versioning + offsite mirror | 用 noncurrent version 回滚 | 高（PDF / Markdown / 元数据）|
| **Neo4j** | 本机 | `neo4j-admin database dump` | `neo4j-admin database load` | 中（可以从 Wiki 重 sync）|
| **Wiki repo** | Git remote (GitHub) | 跑 git remote 就行 | `git clone` | 高（但 GitHub 已经是备份）|

## pb_data 备份

### 离线 copy（最简）

```bash
# 1. 停 server
sudo systemctl stop qatlasd

# 2. 直接 cp（mode preserve）
sudo cp -a /home/timidly/.local/share/quantum-atlas/pb_data \
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

```bash title="/etc/cron.weekly/qatlas-pb-backup"
#!/bin/bash
set -euo pipefail
DEST=/var/backups/qatlas/pb_data-$(date +%F).tar.gz
mkdir -p $(dirname "$DEST")
systemctl stop qatlasd
tar czf "$DEST" -C /home/timidly/.local/share/quantum-atlas pb_data
systemctl start qatlasd
# 保留最近 4 周
find /var/backups/qatlas/ -name 'pb_data-*.tar.gz' -mtime +28 -delete
```

## RustFS bucket 备份策略

### Bucket versioning（自动 enabled）

server 启动时通过 `objstore.S3Store.EnsureVersioning` 自动开启 bucket versioning（详见 [RustFS](rustfs.md#versioning)）。意味着：

- 每次 `--overwrite` 上传 → 旧版本变成 noncurrent，**永久保留**
- 误删 → 留下 delete marker，但**原版本仍在**
- 用 `mc cp --version-id <vid>` 或 `aws s3api get-object --version-id` 回滚

**这本身就是一层备份**——大多数误操作可以靠 versioning 自救。

### 跨地域 mirror（可选）

如果想要离 RustFS 主机的离线备份，用 `rclone sync` 定时跑：

```bash title="cron weekly mirror"
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

详见 [RustFS / storage prune](rustfs.md#prune)。

## Neo4j 备份

```bash
# 停服务（保证一致性）
sudo systemctl stop neo4j

# Dump
sudo neo4j-admin database dump neo4j --to-path=/var/backups/neo4j-$(date +%F).dump

# 起回来
sudo systemctl start neo4j
```

**优先级最低**——挂了从 canonical Wiki 由服务端重建即可，不依赖快照恢复。

## Wiki repo 备份

Wiki 是 [GitHub 上的独立 repo](https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki)，GitHub 自己就是备份。

如果你 paranoid：

```bash
# 异地 mirror（每天）
git clone --mirror https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki.git \
                   /var/backups/wiki/QuantumAtlas-Wiki.git

# 之后定期 fetch
cd /var/backups/wiki/QuantumAtlas-Wiki.git
git remote update
```

## 滚动升级 binary

### 标准流程

```bash
# 1. 拉新 binary（覆盖旧的）
curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh -s -- --version v0.2.9

# 2. 看升级日志（如果有 breaking change）
gh release view v0.2.9   # 或在 GitHub 网页看

# 3. （可选）备份 pb_data
sudo cp -a /home/timidly/.local/share/quantum-atlas/pb_data \
          /var/backups/pb_data-pre-v0.2.9

# 4. Restart
sudo systemctl restart qatlasd

# 5. 等 PocketBase 自动跑 pending migrations
sleep 3

# 6. 验证
curl http://127.0.0.1:4200/api/health | jq .data.version
# "0.2.9"

# 7. 看日志确认 migration 顺利
journalctl -u qatlasd -n 30 | grep -iE 'migration|error'
```

总 downtime 一般 < 10 秒。

### 跨大版本升级（v0.x → v1.x，未来）

到时候 release notes 会有专门 migration guide。当前所有 0.x 升级都是同款流程。

### Rollback

```bash
# 装回老版本
curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh -s -- --version v0.2.7

# 恢复 pb_data（如果 migration 改了 schema）
sudo systemctl stop qatlasd
sudo rm -rf /home/timidly/.local/share/quantum-atlas/pb_data
sudo cp -a /var/backups/pb_data-pre-v0.2.9 \
          /home/timidly/.local/share/quantum-atlas/pb_data
sudo systemctl start qatlasd
```

**注意**：PocketBase migration 是向前的（up），通常没有 down migration。所以：

- 小版本回滚（v0.2.9 → v0.2.8）：通常 schema 不变，直接换 binary 就行
- 大版本回滚（v0.3.0 → v0.2.x）：可能需要恢复 pb_data；记得**升级前先备份**

## 灾难恢复演练

每季度跑一次：

1. 起一台新 VPS
2. 装 binary：`curl -fsSL ... | sh`
3. 恢复 pb_data：`tar xzf pb_data-latest.tar.gz -C <data_dir>`
4. 恢复 wiki：`git clone <wiki-repo>`
5. 指向同一 RustFS bucket 和 Neo4j（mesh 内网 IP）
6. `qatlasd service install --mode system --force ...`
7. 走 [健康检查 checklist](health-and-monitoring.md#self-check)

完整恢复时间应该 ≤ 30 分钟。

## 备份策略推荐组合

| 资源 | 频率 | 工具 | 保留 |
|---|---|---|---|
| pb_data | 每天（cron）| `tar czf` | 4 周 + 月度永久 |
| RustFS bucket | 实时（versioning）| 自带 | 永久；prune 跑 90 天 + keep-last 5 |
| RustFS bucket（offsite）| 每周 | `rclone sync` | 4 周 |
| Neo4j | 月度 / 大改前 | `neo4j-admin dump` | 3 个月 |
| Wiki | 每天 git mirror | `git clone --mirror` | 永久（GitHub 已备份）|

## 不要忘了备份的东西

- `.env`（含 GitHub OAuth secret / Neo4j password / RustFS svcacct）—— 存到 password manager / vault
- systemd unit（如果改过 default）—— commit 进运维仓库
- Caddy / nginx 配置 —— 同上
