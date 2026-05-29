# Neo4j 接入

Neo4j 是 QuantumAtlas 的图数据库，存算法 / 原语 / 论文 / 人物的关系网。**Wiki 是 source of truth，Neo4j 是从 Wiki 派生**——可以随时重建。

## 你需要多少 Neo4j

| 部署形态 | 选哪个 |
|---|---|
| 个人 / 实验室 | 本机 Docker 起一个 Neo4j Desktop / Community 实例 |
| 生产单机 | apt 装 Neo4j Community 跑 systemd |
| **多边缘 active-active** | 一台中心 Neo4j 跨 mesh 暴露给所有边缘 |
| 不想要图谱功能 | **不装也行**——`/api/graph/*` 会返回 `{"error":...}` 200 / `/api/health` 报 `not_configured` |

## 最简：Docker

```bash
docker run -d --name neo4j \
    -p 7474:7474 -p 7687:7687 \
    -e NEO4J_AUTH=neo4j/your-strong-password \
    -v $HOME/.local/share/neo4j/data:/data \
    -v $HOME/.local/share/neo4j/logs:/logs \
    neo4j:5.26
```

访问 <http://localhost:7474> 用 `neo4j` / `your-strong-password` 登录浏览器。

server `.env`：

```bash
NEO4J_URI=bolt://localhost:7687
NEO4J_USERNAME=neo4j
NEO4J_PASSWORD=your-strong-password
# NEO4J_DATABASE=neo4j  # 可选，默认 "neo4j"
```

重启 server，看 `/api/health` 里 `checks.neo4j.status` 应该变成 `"ok"`。

## apt 装（Ubuntu / Debian）

```bash
# 加 GPG key + repo
wget -qO- https://debian.neo4j.com/neotechnology.gpg.key | sudo apt-key add -
echo 'deb https://debian.neo4j.com stable 5' | sudo tee /etc/apt/sources.list.d/neo4j.list
sudo apt update
sudo apt install neo4j

# 配置（编辑 /etc/neo4j/neo4j.conf）
# 至少改：
#   server.bolt.listen_address=0.0.0.0:7687    # 取消注释 + 改 0.0.0.0
#   server.jvm.additional=-Djava.net.preferIPv4Stack=true   # 末尾加这一行

# 初始密码
sudo neo4j-admin dbms set-initial-password "your-strong-password"

# 启动
sudo systemctl enable --now neo4j
sudo systemctl status neo4j
```

确认监听：

```bash
ss -tlnp | grep :7687
# 必须看到 0.0.0.0:7687（不是 *:7687）
```

!!! warning "WSL2 / dual-stack 坑"

    WSL2 里如果不加 `server.jvm.additional=-Djava.net.preferIPv4Stack=true`，JVM 默认 dual-stack v6 socket，`ss` 显示 `*:7687`，**Windows host 的 portproxy 转 v4 SYN 进来直接 RST**。

    完整说明：QuantumAtlas AGENTS.md "Neo4j 服务（@ 1810 WSL）" 段。两条配置缺一不可：

    ```ini
    server.bolt.listen_address=0.0.0.0:7687
    server.jvm.additional=-Djava.net.preferIPv4Stack=true
    ```

## 跨 mesh 暴露给多边缘节点

如果 qatlas-server 和 Neo4j **不在同一台机**（典型场景：Neo4j 跑在团队后端 1810 WSL，server 跑在 RackNerd / 阿里云），需要把 Neo4j 通过 EasyTier mesh 暴露：

```mermaid
flowchart LR
    QA_RN[qatlas-server<br/>RackNerd] -->|bolt://10.144.18.10:7687| MESH
    QA_AL[qatlas-server<br/>阿里云] -->|bolt://10.144.18.10:7687| MESH
    MESH[EasyTier mesh<br/>10.144.18.0/24] --> PROXY[Windows portproxy<br/>10.144.18.10:7687<br/>↓<br/>127.0.0.1:7687]
    PROXY --> NEO4J[(Neo4j @ WSL2)]
```

Windows host portproxy 配置（一次性，永久）：

```powershell
# 以管理员 PowerShell 跑
netsh interface portproxy add v4tov4 \
    listenport=7687 listenaddress=10.144.18.10 \
    connectport=7687 connectaddress=127.0.0.1
```

每台 qatlas-server `.env`：

```bash
NEO4J_URI=bolt://10.144.18.10:7687
NEO4J_USERNAME=neo4j
NEO4J_PASSWORD=your-strong-password
```

## 验证连通

server 起来后：

```bash
# health check 反映
curl http://127.0.0.1:4200/api/health | jq .data.checks.neo4j

# 也可以直接打 graph endpoint 触发一次 Bolt 连接
curl http://127.0.0.1:4200/api/graph/stats | jq
# {
#   "nodes": 0, "relationships": 0,
#   "labels": [], "label_counts": {}
# }
```

如果返回 `{"error": "..."}`，看 error 内容：

| Error | 原因 |
|---|---|
| `connection refused` | Neo4j 没起或端口没暴露 |
| `authentication failure` | 密码错 |
| `database not found: xxx` | `NEO4J_DATABASE` 设了不存在的数据库 |
| `context deadline exceeded` | 网络 / mesh 不通 |

## 初次 sync Wiki 到 Neo4j

Server 启动时**不会**自动 sync。Wiki → Neo4j 的派生是**服务端职责**：Go
``qatlas-server`` 持有 Neo4j 连接，基于 canonical Wiki（source of truth）重建图谱。
Python 客户端不再直连 Neo4j，也没有客户端 sync 命令。

后续 Wiki 通过 `POST /api/wiki/sync/pull` 触发 git pull 时，**会顺带 refresh in-memory cache**。完整 sync 策略见 [数据流 / Wiki→Neo4j](../concepts/data-flow.md#wiki-neo4j)。

## 备份

```bash
# Stop neo4j first（一致性 backup）
sudo systemctl stop neo4j
sudo neo4j-admin database dump neo4j --to-path=/var/backups/neo4j-$(date +%F).dump
sudo systemctl start neo4j
```

恢复：

```bash
sudo systemctl stop neo4j
sudo neo4j-admin database load neo4j --from-path=/var/backups/neo4j-2026-05-29.dump
sudo systemctl start neo4j
```

或者**不备份图数据库**——反正它是从 Wiki 派生的，挂了就重 sync。

## Cypher 例子（探索）

```cypher
// 列所有算法 + 它们用的原语
MATCH (a:Algorithm)-[:USES]->(p:Primitive)
RETURN a.id AS algo, collect(p.id) AS primitives

// 找最常被引用的论文
MATCH (paper:Paper)<-[:CITES|:REFERENCES]-(other)
RETURN paper.id, count(other) AS refs
ORDER BY refs DESC
LIMIT 10

// 找 Shor 算法的所有依赖
MATCH path = (a:Algorithm {id:"algo-shor"})-[:USES*1..3]->(p)
RETURN path
```

通过 server 跑：

```bash
curl -X POST https://<server>/api/graph/query \
  -H "Content-Type: application/json" \
  -d '{"query":"MATCH (a:Algorithm) RETURN a.id LIMIT 5"}'
```

或浏览器打开 Neo4j Browser <http://localhost:7474> 直接跑。

## 性能 / 容量

- 当前 schema 节点 / 边数都在万级，Community Edition 足够
- 内存：JVM heap 1-2 GB 足够（1810 WSL 实测）
- 磁盘：少于 100 MB
- Bolt connection pool：默认 100，QuantumAtlas 没有持续高并发图查询，不需要调

## 完全不要图谱也行

设 `NEO4J_URI=` 留空 / 不写。结果：

- `/api/health` 报 `neo4j: not_configured`（不下拉聚合等级）
- `/api/graph/*` endpoint 返回 `{"error":"NEO4J_URI not configured"}` 200
- Wiki 的 `[[page-id]]` 链接仍工作（不依赖 Neo4j）
- SPA 的 "Graph" 标签页空着

适合论文知识库 + Wiki 已经够用的场景。
