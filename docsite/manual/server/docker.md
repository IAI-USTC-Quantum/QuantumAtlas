# Docker 部署

> 单 binary（`qatlasd`）的 docker 流派部署。**自 v0.16.0 起**正式支持，跟 systemd 流派并列；选哪个取决于你的运维偏好和数据规模，见下面表格。

## 何时选 docker / 何时选 systemd

| 维度 | systemd（裸 binary） | docker compose |
|---|---|---|
| 单进程极简部署 | 推荐 | 支持 |
| 一键起完整栈（含 PostgreSQL） | 不支持（手动多服务） | 推荐 |
| 多服务隔离 / k8s / 集群 | 不支持 | 推荐 |
| 升级 binary | `install-qatlasd.sh` 重跑 + `systemctl restart` | `docker compose pull && docker compose up -d` |
| 配置 hot-reload | 改 `.env` + `systemctl restart` | 改 `.env` + 重建 container |
| 资源占用 | binary ~50 MB RSS | + container overhead 5-10 MB / service |
| 默认日志去向 | journald (`journalctl -u qatlasd`) | stdout (`docker logs qatlasd` / log driver) |
| Caddy / nginx 反代 | host 上跑（推荐） | host 上跑或者 compose 内 sidecar |
| 备份 | rsync `~/.local/share/qatlasd/`（XDG）/ `/var/lib/quantum-atlas/`（FHS） | `tar czf data.tgz data/`（bind mount） |

简单原则：

- **个人 / 实验室 / 评估**：docker compose 全家桶 — `docker compose up -d` 一行起 PostgreSQL + qatlasd（对象存储留外部，如 NAS 上的 RustFS）
- **生产单边缘 / 多边缘 active-active**：systemd binary + 外部 RustFS / PostgreSQL（mesh 共享）
- **k8s / Nomad / Swarm**：docker image 作为 building block
- **想随时切换**：两种流派**完全兼容数据**（同一份 `pb_data` / RustFS bucket / PG database 可以今天 systemd 跑明天 compose 跑），不存在 lock-in

## 镜像与标签

服务端镜像由 GoReleaser `dockers_v2` 构建并发布到 GitHub Container Registry，默认平台为 `linux/amd64` + `linux/arm64`。版本标签来自 `.Tag` 和 `.Version`；只有非 prerelease 且非 snapshot 时另更新 `latest`：

```
ghcr.io/iai-ustc-quantum/qatlasd:vX.Y.Z   # 带 v 前缀的精确版本
ghcr.io/iai-ustc-quantum/qatlasd:X.Y.Z    # 去 v 的版本
ghcr.io/iai-ustc-quantum/qatlasd:latest   # 每次非预发布、非快照的镜像发布时更新
```

例如 `vX.Y.Z-rc.N` 只更新两个版本标签，不更新 `latest`。`latest` 不按版本大小排序，也不等待外部 smoke；GitHub Latest 另由 GoReleaser/平台默认处理，两者不是原子发布。部署应固定已核验的 tag 或 digest，不能把流程成功或 `latest` 更新当作生产运行验证。

`Dockerfile.goreleaser` 复用已经内嵌 UI/文档的 GoReleaser 预编译二进制，不重复源码编译；原 `Dockerfile` 保留为本地源码构建路径。发布镜像仍基于 **`gcr.io/distroless/static-debian12:nonroot`**，无 shell、默认 UID 65532，保留现有运行参数。真实多架构 manifest、拉取与运行仍须单独验证。

后续采用新 workflow 的版本在 GoReleaser 发布后，由官方 `actions/attest` 读取 `dist/digests.txt` 生成签名构建证明，默认登记到 GitHub，不推送 registry bundle。对已核对的镜像 digest，可用 GitHub CLI 验证：

```bash
gh attestation verify 'oci://ghcr.io/iai-ustc-quantum/qatlasd@sha256:<digest>' \
  --repo IAI-USTC-Quantum/QuantumAtlas
```

来源证明关联构建身份与镜像摘要，不保证程序无 bug。已发布的 `v0.35.0-rc.1` 不补签新 GitHub 证明；其已验证的 BuildKit 双架构 SBOM/provenance 不受影响，两类证明不能混同。attestation 失败时 Release 和镜像可能已公开，应先核对实际状态，不直接重跑覆盖；稳定版推送时更新 `latest` 的条件不因此改变。

## A. compose 全家桶（推荐起手式）

适合：单机评估、实验室、个人部署。

```bash
git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas
cd QuantumAtlas/deploy
cp .env.docker.example .env
$EDITOR .env                      # 改密码 + GitHub OAuth client
chmod 0600 .env

# Distroless nonroot UID 65532 — 必须 chown 不然 server 写不了
mkdir -p data/{raw,pb_data}
sudo chown -R 65532:65532 data/{raw,pb_data}

docker compose up -d
docker compose logs -f qatlasd    # 看启动 log，Ctrl-C 退出
curl http://localhost:4200/api/health
# {"status":"healthy",...}  （degraded ok — S3 还没 bootstrap）
```

### 首次启动后：bootstrap RustFS svcacct + buckets

RustFS 启动时只有 root 凭据（`.env` 里的 `RUSTFS_ROOT_*`）。qatlasd 必须用**service account** key（最小权限原则；root key 不能用），先建 svcacct 才能让 qatlasd 真正 write 进去：

```bash
# 用一次性 mc 容器（distroless image 没 shell，没法 docker exec 进 rustfs）
docker run --rm --network=container:qatlas-rustfs minio/mc \
    mc alias set local http://localhost:9000 \
    "$RUSTFS_ROOT_ACCESS_KEY" "$RUSTFS_ROOT_SECRET_KEY"

# 建 svcacct（输出会含一对 access/secret，记下来）
docker run --rm --network=container:qatlas-rustfs minio/mc \
    mc admin user svcacct add local --name qatlasd "$RUSTFS_ROOT_ACCESS_KEY"

# 建三个 buckets
docker run --rm --network=container:qatlas-rustfs minio/mc \
    mc mb local/qatlas-pdf local/qatlas-md local/qatlas-images
```

把 svcacct 的 access/secret 填回 `.env` 的 `QATLAS_S3_ACCESS_KEY_ID` / `QATLAS_S3_SECRET_ACCESS_KEY`，然后重启 qatlasd：

```bash
docker compose up -d qatlasd      # 只重建 qatlasd container，其他不动
curl http://localhost:4200/api/health | jq .data.checks.raw_store
```

如果嫌 mc 步骤烦，可以用仓库自带的 [`scripts/rustfs_bootstrap.sh`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/scripts/rustfs_bootstrap.sh) 一键搞定（指向 `http://localhost:9000`）。

### 完整 .env 字段

参见 [`deploy/.env.docker.example`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/deploy/.env.docker.example)，每个字段都带 inline 注释。最容易踩的两个坑：

- **`QATLAS_S3_*` 必须是 svcacct 凭据**，不要直接复制 `RUSTFS_ROOT_*`（root key 工作得了，但出事根本无法回滚）
- **`QATLAS_ALLOWED_GITHUB_LOGINS` 或 `QATLAS_ADMIN_GITHUB_LOGINS` 至少要填一个**，否则 fail-closed 谁都登不了（这是刻意的；防止配置失误导致公网暴露）

## B. compose standalone（qatlasd-only）

适合：已有外部 RustFS（NAS、公有云 S3、R2、…）和外部 PostgreSQL（专门 VPS、托管 RDS）的场景。

```bash
cd QuantumAtlas/deploy
cp .env.docker.example .env
$EDITOR .env                      # 填 QATLAS_S3_ENDPOINT / QATLAS_POSTGRES_DSN 等
chmod 0600 .env

mkdir -p data/{pb_data,raw}
sudo chown -R 65532:65532 data/

docker compose -f docker-compose.standalone.yml up -d
```

`QATLAS_S3_ENDPOINT` / `QATLAS_POSTGRES_DSN` 在 standalone compose 里**都是必填**（没有默认会让它意外回到全家桶模式）。

## C. `docker run` 单 image（k8s / Nomad / 自定义编排）

如果你不用 compose（kubernetes deployment / Nomad job / cap1ystone），直接 pull image：

```bash
docker run -d --name qatlasd \
    -p 127.0.0.1:4200:4200 \
    -v /srv/qatlas/pb_data:/data/pb_data \
    -v /srv/qatlas/raw:/data/raw \
    -e QATLAS_S3_ENDPOINT=https://rustfs.example.com \
    -e QATLAS_S3_BUCKET_PDF=qatlas-pdf \
    -e QATLAS_S3_BUCKET_MD=qatlas-md \
    -e QATLAS_S3_BUCKET_IMAGES=qatlas-images \
    -e QATLAS_S3_ACCESS_KEY_ID=... \
    -e QATLAS_S3_SECRET_ACCESS_KEY=... \
    -e QATLAS_POSTGRES_DSN=postgres://qatlas:secret@pg.example.com:5432/qatlas?sslmode=disable \
    -e GITHUB_CLIENT_ID=... \
    -e GITHUB_CLIENT_SECRET=... \
    -e QATLAS_ALLOWED_GITHUB_LOGINS=your-login \
    ghcr.io/iai-ustc-quantum/qatlasd:latest
```

或者用 `--env-file`：

```bash
docker run --rm --env-file /path/to/qatlasd.env \
    -v /srv/qatlas:/data \
    -p 127.0.0.1:4200:4200 \
    ghcr.io/iai-ustc-quantum/qatlasd:latest
```

健康检查：

```bash
docker run --rm ghcr.io/iai-ustc-quantum/qatlasd:latest --version
# qatlasd version X.Y.Z
```

完整 env 列表参考 [`docs/deployment/server-config.md`](server-config.md)。

## 加 Caddy 反代（可选）

compose 默认只把 qatlasd 4200 端口 bind 在 `127.0.0.1` —— 公网入口靠反代。最简方案是把 caddy 加进同一个 compose 文件：

```yaml
# 加在 deploy/docker-compose.yml 的 services 块里
  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data           # LE certs / OCSP cache，重启不丢
      - caddy_config:/config

volumes:
  caddy_data: {}
  caddy_config: {}
```

对应的 `./Caddyfile`：

```caddy
atlas.your-domain.com {
    reverse_proxy qatlasd:4200      # service name in compose network
}
```

Caddy 自动跟 Let's Encrypt 申请证书（前提：80/443 公网可达 + 域名 A 记录指过来）。

更复杂的拓扑（多边缘 / SSO / 双 endpoint）见 [反向代理](reverse-proxy.md)。

## 加 fluent-bit / RustFS audit notify（可选）

T10 审计 sink 用 fluent-bit 中继 RustFS notify webhook 到独立 audit bucket。完整 compose 片段见 [`deploy/nas-rustfs-compose.example.yaml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/deploy/nas-rustfs-compose.example.yaml)，可以整段复用进 `docker-compose.yml`。

## 备份 / 升级 / 回滚

### 备份

bind mount 模式备份就是 tar：

```bash
docker compose stop                       # 短暂停一致性 snapshot
tar czf qatlas-backup-$(date +%F).tgz data/
docker compose start
```

或者只挑 critical state（pb_data + postgres 的 named volume；raw 资产可从 S3 重建 registry）：

```bash
tar czf qatlas-critical-$(date +%F).tgz data/pb_data   # + docker volume 里的 pgdata
```

### 升级 qatlasd

```bash
# 升 image tag
$EDITOR .env                              # 改 QATLAS_VERSION=v0.17.0
docker compose pull qatlasd
docker compose up -d qatlasd              # 只重建 qatlasd，postgres 不重启

# 或者跟 latest 滚动
docker compose pull
docker compose up -d
```

### 回滚

```bash
# 紧急回滚到上个 version
QATLAS_VERSION=v0.15.0 docker compose up -d qatlasd
```

回滚前先检查目标版本的 [GitHub Release](https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases) 和对应版本的迁移/部署文档。现行 Release 正文由 GoReleaser 从 Git 提交生成，不等于完整的 schema 兼容性说明。换回旧镜像只回滚程序，**不会逆转 PocketBase/goose 数据迁移**，即使 patch 版本也不能保证回滚安全。升级前备份数据库与相关状态；必要时停止服务并从备份恢复，确认版本守卫允许后再启动。

### 升级 RustFS / PostgreSQL

各自有 changelog 看。RustFS 主要看 [`RUSTFS_DRIVE_TIMEOUT_PROFILE`](rustfs.md) 这种 hot path 兼容；PostgreSQL 跨 major 升级前先做 `pg_dump`，并遵循官方升级指引（registry 表结构由 qatlasd 内的 goose migrations 管理，启动时自动 apply）。

## 常见坑

### "permission denied" on `./data/*`

distroless `nonroot` 用户 UID 是 **65532**。`mkdir data/` 是当前用户拥有的，container 写不进去：

```bash
ls -ln data/raw
# drwxrwxr-x 2 1000 1000 ...    # 1000 = host user, not 65532

sudo chown -R 65532:65532 data/{raw,pb_data}
docker compose up -d qatlasd
```

`postgres` container 不受影响（official image 自己处理数据目录权限）。

### GitHub OAuth callback URL 必须是公网 URL

OAuth app 注册时填 `https://atlas.your-domain.com/api/oauth2-redirect`，**不能**填 `http://localhost:4200/...`。GitHub 不接受 localhost callback；最低门槛是带 TLS 的真域名。本地测：用 [caddy 反代 + 自签 cert](reverse-proxy.md) + `127.0.0.1 atlas.local.test` 写 hosts 文件。

### `docker exec -it qatlas-rustfs sh` 不行

distroless 没 shell。RustFS 用的是 `rustfs/rustfs:latest` image（不是 distroless），有 shell；但 qatlasd image 是 distroless static，**不能** exec 进去 debug。要 debug 时：

```bash
# 替换 entrypoint 拉一次性 image with shell（不能附在 running qatlasd 上）
docker run --rm -it --entrypoint=/qatlasd \
    ghcr.io/iai-ustc-quantum/qatlasd:latest \
    config show              # 任意 subcommand，看 env 配置
```

或者 sidecar 模式跑个 `busybox`/`alpine` mount 同样的 volume 做文件检查。

### `mc` 不能从 host 连 rustfs container

`rustfs` service 默认只在 compose 内部网络可见（没 publish 9000）。host 上的 `mc` 连不上 `localhost:9000`。两条路：

- 在 compose 文件给 `rustfs` 加 `ports: ["127.0.0.1:9000:9000"]`（仅 dev / 受信网络）
- 用 `--network=container:qatlas-rustfs` 跑一次性 mc container（共享 rustfs 的 network namespace，连 `localhost:9000` 就是连 rustfs 自己）—— 上面 bootstrap 步骤就用了这招

### 想看 server log

```bash
docker compose logs -f qatlasd
# 或保留最近 100 行：
docker compose logs --tail 100 qatlasd
```

journald 不存 — log 直接走 stdout 给 docker 的 log driver。生产可以挂 `json-file` driver + log rotate，或者推到外部 loki / ELK / cloudwatch。
