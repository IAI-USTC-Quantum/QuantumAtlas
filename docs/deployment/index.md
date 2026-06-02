# 部署运维

QuantumAtlas server 是一个 Go 单 binary。这一节覆盖从"裸 VPS"到"生产可用 + 监控 + 备份 + 升级"的完整流程。

## 快速路径

<div class="grid cards" markdown>

-   :material-package-down:{ .lg .middle } **[安装与 service 注册](install.md)**

    ---

    `install-qatlasd.sh` + `qatlasd service install` 的全自动 / 半自动 / 全手动三种模式（systemd 流派）。

-   :material-docker:{ .lg .middle } **[Docker 部署](docker.md)**

    ---

    `docker compose up -d` 一键起 qatlasd + RustFS + Neo4j 全家桶；ghcr.io 多架构 image；适合评估、k8s 友好部署。

-   :material-server-network:{ .lg .middle } **[反向代理模板](reverse-proxy.md)**

    ---

    Caddy + nginx 配置示例，包含 SigV4 Host header preserve 的关键细节。

-   :material-github:{ .lg .middle } **[GitHub OAuth 接入](github-oauth.md)**

    ---

    OAuth App 创建、callback URL、PocketBase provider 注入、多边缘节点的多 app 策略。

-   :material-database:{ .lg .middle } **[Neo4j 接入](neo4j.md)**

    ---

    自建 / 托管 / 跨 mesh 暴露，环境变量配置，常见 WSL2 dual-stack 坑。

-   :material-cloud-upload:{ .lg .middle } **[RustFS / S3 对象存储](rustfs.md)**

    ---

    bootstrap script 流程、IAM policy、bucket versioning、dual endpoint（presign 公网 + 内网传输）。

-   :material-heart-pulse:{ .lg .middle } **[健康检查与监控](health-and-monitoring.md)**

    ---

    `/api/health` 接监控告警；degraded 状态怎么处理；常见报警模板。

-   :material-backup-restore:{ .lg .middle } **[备份与升级](backup-and-upgrade.md)**

    ---

    pb_data SQLite 备份、RustFS bucket versioning + prune、Neo4j dump、binary 滚动升级。

</div>

## 完整运维参考

<div class="grid cards" markdown>

-   :material-server:{ .lg .middle } **[运维 ops 参考](operations.md)**

    ---

    完整长文：systemd、Caddy 模板、env 完整说明、RustFS 集成、Neo4j 集成、多边缘——目前主体内容仍在这里，新页面是它的拆分版。

-   :material-folder-arrow-up:{ .lg .middle } **[存储布局迁移](migration-storage-layout.md)**

    ---

    从老的"仓库内 wiki/raw/data/pb_data"迁到 XDG 路径的指南。仅老用户需要看。

</div>

## 部署形态

| 形态 | 推荐流派 | 拓扑示意 |
|---|---|---|
| 个人 / 实验室（评估） | **docker compose 全家桶** | qatlasd + rustfs + neo4j 一台 |
| 个人 / 实验室（长期） | systemd 单机 + LocalStore | qatlasd 一台，无对象存储 |
| 团队（数据规模 < 100k 论文） | systemd 三件套（分机） | qatlasd@A · rustfs@NAS · neo4j@内存大设备 |
| 生产单边缘 | systemd + 公有云 S3 / 自托管 RustFS | qatlasd@VPS · rustfs@专属 VPS / R2 · neo4j@专属 VPS |
| 多边缘 active-active | systemd × N + 共享存储 | qatlasd × N edges → 共享 rustfs + neo4j（mesh） |
| k8s / Nomad / Swarm | docker image | helm chart / nomad job — image 当 building block |

后续每个具体配置页都会说哪些选项适合哪个形态。

## 建议阅读顺序

**systemd 流派**（生产单边 / 多边 / 长期部署）：

1. **[install](install.md)** — 装好 binary + service
2. **[reverse-proxy](reverse-proxy.md)** — 前面挂 TLS
3. **[github-oauth](github-oauth.md)** — 用户能登录
4. **[neo4j](neo4j.md)** — 图谱功能（可选，但是核心；可放另一台机）
5. **[rustfs](rustfs.md)** — 对象存储（生产强烈建议；可放另一台机或公有云）
6. **[health-and-monitoring](health-and-monitoring.md)** — 接监控
7. **[backup-and-upgrade](backup-and-upgrade.md)** — 备份策略

**docker 流派**（一键评估 / k8s 友好）：

1. **[docker](docker.md)** — compose 起栈 / standalone / `docker run`
2. **[reverse-proxy](reverse-proxy.md)** — 公网 TLS（可选；compose 内也能挂 caddy sidecar）
3. **[github-oauth](github-oauth.md)** — OAuth app 注册（跟流派无关）
4. **[health-and-monitoring](health-and-monitoring.md)** — `docker logs` / 监控接入
5. **[backup-and-upgrade](backup-and-upgrade.md)** — bind mount 备份 + `docker compose pull`
