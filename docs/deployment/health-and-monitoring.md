# 健康检查与监控

`GET /api/health` 是 QuantumAtlas 的标准探活点。它**永远返回 200**（即使依赖挂了），真正的状态在 body 里。

## 响应形状

```json
{
  "code": 200,
  "message": "API is healthy.",
  "data": {
    "status": "healthy",
    "version": "0.2.8",
    "uptime_seconds": 12345,
    "time": "2026-05-29T03:00:00Z",
    "checks": {
      "rawstore": {
        "status": "ok",
        "backend": "s3",
        "endpoint": "http://10.144.18.10:9000",
        "bucket": "qatlas-raw",
        "latency_ms": 12
      },
      "neo4j": {
        "status": "ok",
        "uri": "bolt://10.144.18.10:7687",
        "database": "neo4j",
        "latency_ms": 8
      },
      "wiki": {
        "status": "ok",
        "dir": "/home/timidly/QuantumAtlas-Wiki",
        "commit": "abc12345",
        "commit_time": "2026-05-28T22:10:33Z",
        "branch": "main",
        "dirty": false
      }
    }
  }
}
```

| 字段 | 含义 |
|---|---|
| `code` | **永远 200**——不让上层 LB / Caddy trip 整条链路 |
| `message` | `"API is healthy."` / `"Dependency degraded."` |
| `data.status` | `healthy` 或 `degraded`（聚合状态）|
| `data.version` | server binary 版本 |
| `data.uptime_seconds` | 进程启动以来 |
| `data.checks.<probe>` | 各依赖探活结果 |

## 三个 probe

| Probe | 检查什么 | 失败的含义 |
|---|---|---|
| `rawstore` | S3 backend 时 `BucketExists` HEAD；LocalStore 时直接 ok | bucket 不存在 / svcacct 失权 / 网络 |
| `neo4j` | `NewClient + Connect + VerifyConnectivity` | URI 不通 / 密码错 / Neo4j 挂了 |
| `wiki` | `git rev-parse HEAD`、读 commit time | wiki dir 不存在 / 不是 git repo |

每个 probe 5s 硬超时（`probeTimeout`），三个**并行**执行，互不阻塞。

### 聚合规则

```
status = healthy    iff 所有 check 是 "ok" 或 "not_configured"
status = degraded   iff 任一 check 是 "error"
```

`not_configured`（例如 `NEO4J_URI` 没设）**不下拉聚合等级**——它是"刻意没启用"，跟"配置了但挂了"是两回事。

## 接监控告警

### Uptime Kuma / Healthchecks.io / 类似工具

监控 URL：`https://<server>/api/health`

| 工具方式 | 告警条件 |
|---|---|
| 简单 HTTP 状态码 | **不会触发**——code 永远 200 |
| Response body keyword | 用 "API is healthy." 必须存在 / "Dependency degraded." 必须不存在 |
| JSON path | `$.data.status == "healthy"` |
| 多 probe 单独告 | 写脚本拿 JSON → 拆 `data.checks.<probe>.status` |

**推荐**：用 JSON path `$.data.status == "healthy"` 而不是 status code。

### Prometheus / OpenTelemetry

QuantumAtlas **没有内置 Prometheus exporter**，但可以用 [blackbox_exporter](https://github.com/prometheus/blackbox_exporter) 把 `/api/health` 当 generic HTTP probe 转 metrics：

```yaml title="prometheus.yml"
- job_name: qatlas-health
  metrics_path: /probe
  params:
    module: [http_2xx]
  static_configs:
    - targets:
        - https://quantum-atlas.ai/api/health
  relabel_configs:
    - source_labels: [__address__]
      target_label: __param_target
    - target_label: __address__
      replacement: blackbox:9115
```

如果想细到每个 dependency，自己用 `scripts/qatlas_health_exporter.py` 拿 JSON → 转 metrics → expose 给 Prom。社区当前没有标准 exporter；欢迎 PR。

### Bash 巡检脚本（最朴素）

```bash title="check-qatlas.sh"
#!/bin/bash
set -uo pipefail
URL="${1:-https://quantum-atlas.ai/api/health}"
RESP=$(curl -fsS --max-time 10 "$URL")
STATUS=$(echo "$RESP" | jq -r .data.status)

if [ "$STATUS" != "healthy" ]; then
    echo "[ALERT] QuantumAtlas status: $STATUS"
    echo "$RESP" | jq .data.checks
    exit 1
fi
echo "[OK] healthy ($(echo "$RESP" | jq -r .data.version), uptime $(echo "$RESP" | jq .data.uptime_seconds)s)"
```

cron 每分钟跑一下，输出有 ALERT 就邮件/Slack。

## 常见 degraded 场景与对应处理

| 症状 | 检查 | 处理 |
|---|---|---|
| `rawstore: error: bucket does not exist` | bucket 名错或 RustFS 重启丢了 bucket | 跑 [`scripts/rustfs_bootstrap.sh`](rustfs.md#bootstrap) 重建 |
| `rawstore: error: SignatureDoesNotMatch` | svcacct 凭据错 | 校验 `.env` 里 `QATLAS_S3_ACCESS_KEY_ID/SECRET` |
| `rawstore: error: connection refused` | RustFS 挂了 / mesh 断了 | `systemctl status rustfs` / `ping 10.144.18.10` |
| `neo4j: error: connection refused` | Neo4j 没起 | `systemctl status neo4j` |
| `neo4j: error: authentication failure` | 密码改了 | 校验 `.env` 里 `NEO4J_PASSWORD` |
| `neo4j: error: context deadline exceeded` | mesh 不通 / portproxy 失效 | [Neo4j 部署](neo4j.md) 的连通性章节 |
| `wiki: error: wiki directory is not a git repository` | `WIKI_DIR` 指错了 / dir 被删了 | 重新 `git clone QuantumAtlas-Wiki` 到该路径 |

## SLO 推荐

对**生产部署**：

- **`data.status == healthy`** 在 99.5%/月 时间内为真 → 4 小时 downtime/月 余量
- **依赖单独**没有强 SLO（research infra，不是 OLTP）

如果某 probe 长期 error，**先看是不是 not_configured 的边界条件**——多边缘节点可能不全都有 Neo4j。

## 日志

`journalctl -u qatlas-server -n 100` 看 server 日志。关键模式：

| 日志关键字 | 含义 |
|---|---|
| `raw store: S3 backend ...` | S3 backend 启动成功 |
| `raw store: local backend ...` | LocalStore 启动成功 |
| `bucket versioning: enabled` | versioning 自管成功 |
| `paperindex: catalog ready (N rows)` | parquet 索引启动成功 |
| `wiki: cache initialized` | Wiki cache 启动成功 |
| `pat: built scope enforcer` | scope enforcer 启动成功 |
| `pocketbase: serving on ...` | HTTP server 就绪 |

启动序列任何一步 `Fatal` 就是配置错——根因都很明确，按 message 改 .env 即可。

## 自检 checklist（部署后立刻跑） { #self-check }

```bash
# 1. 健康全 ok
curl -fsS https://<server>/api/health | jq .data.status
# "healthy"

# 2. 写口可达（需要带 PAT）
curl -X POST https://<server>/api/pat \
  -H "Authorization: Bearer $SESSION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"smoke-test","scopes":[],"expires_in_days":1}' | jq

# 3. Wiki cache 有数据
curl -fsS https://<server>/api/stats | jq

# 4. 图谱通了
curl -fsS https://<server>/api/graph/stats | jq

# 5. share + presign 通了
TOKEN=$(curl -X POST https://<server>/api/shares/ \
  -H "Authorization: Bearer $PAT" -H "Content-Type: application/json" \
  -d '{"paths":["pdf/2501/2501.00010v1.pdf"],"expires_in":300}' | jq -r .token)
curl -sIL https://<server>/share/$TOKEN | tail -5
# 应该 307 → 200
```

任何一步红了就照对应章节 fix。
