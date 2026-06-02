# 部署运维

QuantumAtlas server 是一个 Go 单 binary。这一节覆盖从"裸 VPS"到"生产可用 + 监控 + 备份 + 升级"的完整流程。

## 快速路径

<div class="grid cards" markdown>

-   :material-package-down:{ .lg .middle } **[安装与 service 注册](install.md)**

    ---

    `install-qatlasd.sh` + `qatlasd service install` 的全自动 / 半自动 / 全手动三种模式。

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

## 建议阅读顺序

第一次部署：

1. **[install](install.md)** — 装好 binary + service
2. **[reverse-proxy](reverse-proxy.md)** — 前面挂 TLS
3. **[github-oauth](github-oauth.md)** — 用户能登录
4. **[neo4j](neo4j.md)** — 图谱功能（可选，但是核心）
5. **[rustfs](rustfs.md)** — 对象存储（生产强烈建议）
6. **[health-and-monitoring](health-and-monitoring.md)** — 接监控
7. **[backup-and-upgrade](backup-and-upgrade.md)** — 备份策略

## 部署形态

| 形态 | 适用 |
|---|---|
| **单机** | 个人 / 实验室 1 个 server + 1 个 Neo4j + LocalStore（不用 RustFS） |
| **单机 + S3** | 生产入门：1 个 server + RustFS（保留 versioning）|
| **多边缘 active-active** | 跨地域：多个 server + 共享 RustFS + 共享 Neo4j，详见 [多边缘部署](../concepts/multi-edge.md)|

后续每个具体配置页都会说哪些选项适合哪个形态。
