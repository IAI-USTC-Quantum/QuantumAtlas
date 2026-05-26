# QuantumAtlas Agent Instructions

> Project-level context for AI coding agents (GitHub Copilot CLI, Claude Code,
> Cursor, etc.). Keep this file authoritative; if you discover the codebase
> contradicts something here, update the doc rather than just patching code.

## 项目角色

`atlas/` 是一份 client+server 共用的 Python 包，单一 entrypoint `qatlas`。
- **server 侧**：FastAPI + uvicorn，正式实例跑在团队内网 `1810` 主机
  （`quantum-atlas.service` systemd，`uv run uvicorn ... 0.0.0.0:4200`）。
- **client 侧**：所有 `qatlas <command>` 子命令默认通过 `QATLAS_SERVER_URL`
  指向远端 server；本机不需要任何 server 依赖。

## `.env` 字段分类（client vs server）

同一份 `.env` 既给 client 用，也给 server 用。项目自有字段统一加 `QATLAS_` 前缀，
旧名作 alias 保留；第三方/SDK 标准名（`NEO4J_*` / `OPENAI_*` / `ANTHROPIC_*` / `MINERU_*`）
不加前缀。按角色取舍：

| 字段 | client | server | 备注 |
|---|---|---|---|
| `QATLAS_SERVER_URL` (alias: `PUBLIC_BASE_URL`) | ✅ | ✅ | client 必填；server 用来生成 share URL |
| `QATLAS_INSECURE` | ✅ | — | 跳过 TLS 校验；等价于 CLI `--insecure` |
| `QATLAS_WIKI_DIR` (alias: `WIKI_DIR`) | ✅ | ✅ | client 也跑 `qatlas wiki list/show/search/lint`，全在本地 git clone 的 wiki repo 上 |
| `MINERU_API_TOKEN` 等 `MINERU_*` | ✅ | ✅ | client 跑 `qatlas mineru` 用自己的配额 |
| `QATLAS_RAW_DIR` / `QATLAS_DATA_DIR` (alias: `RAW_DIR` / `DATA_DIR`) | ❌ | ✅ | client 上传走 `--pdf <任意路径>`，不读 RAW_DIR |
| `QATLAS_SERVER_HOST` / `QATLAS_SERVER_PORT` / `QATLAS_SERVER_DEBUG` (alias: `SERVER_*`) | ❌ | ✅ | client 不监听端口 |
| `NEO4J_*` | ❌ | ✅ | client 不直连图库 |
| `QATLAS_SHARE_ACCESS_TOKEN` / `QATLAS_DEFAULT_SHARE_EXPIRES_IN` (alias: `SHARE_ACCESS_TOKEN` / `DEFAULT_SHARE_EXPIRES_IN`) | ❌ | ✅ | server 颁发 share URL |
| `QATLAS_USER_HEADER` (alias: `USER_HEADER`) | ❌ | ✅ | 反代/SSO 注入审计头 |
| `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | ❌ | ✅ | server 端 LLM 提取 |
| `QATLAS_REQUIRE_RELEASE_TAG` (alias: `QUANTUMATLAS_REQUIRE_RELEASE_TAG`) | ❌ | ✅ | 生产保护 |
| `CLI_TOKEN_*` | ❌ | ❌ | **已废弃**，鉴权由 Caddy OAuth 统一处理 |

**纯 client 最小 `.env`**：

```bash
QATLAS_SERVER_URL=https://quantum-atlas.ai
# QATLAS_INSECURE=1                    # 远端是自签证书时打开
# QATLAS_WIKI_DIR=../QuantumAtlas-Wiki # 本地 clone 了 wiki 仓库再加
# MINERU_API_TOKEN=<your_token>        # 本地跑 mineru 才需要
```

详细字段说明见 `.env.example`（顶部有相同表格，单字段注释里也标了角色）。

## 边缘节点 / 网络拓扑

`quantum-atlas.ai` 是**多线路**部署。两台边缘 VPS 各跑 Caddy，都做反代回源
到 `1810` 上的 `0.0.0.0:4200`：

| 边缘节点 | IP | 角色 |
|---|---|---|
| RackNerd | `107.173.13.248`（公共 DNS `quantum-atlas.ai` A 记录） | 海外节点 |
| Alibaba 杭州 | `47.102.36.175` | 国内节点 |

**两台都给 `quantum-atlas.ai` 这个域名通过 Let's Encrypt 申了证书。**
因此：

- 默认走 DNS → 拿到的就是 RackNerd 边缘。
- 想走 Alibaba 时，**用本机 hosts 覆盖**：

  ```text
  47.102.36.175  quantum-atlas.ai
  ```

  TLS 握手时 client 发的 SNI 仍然是 `quantum-atlas.ai`，Alibaba Caddy 拿出
  自己那张 Let's Encrypt 证书，**系统信任库直接过，无需 `--insecure`**。
  关键洞见：`TCP 连哪个 IP` 和 `HTTPS 用哪张证书` 是两件事——hosts 只控前者，
  TLS 信任只看后者的 SNI 与证书 SAN。

不要为了"用 IP"而把 `.env` 写成 `https://47.102.36.175`：
- Alibaba 上虽然挂了一份 `tls internal` 的 self-signed cert（IP SAN），
  但 client 系统信任库不会信任 Caddy Local CA，每次都要 `--insecure`。
- `http://47.102.36.175` 会被 Caddy 308 跳到 `https://47.102.36.175`，
  又踩到同样的问题。

**结论：`.env` 永远写 `https://quantum-atlas.ai`，靠 hosts 切线路。**

## 远端运维

- 部署 host 别名：`1810`（实际 `10.144.18.10:2222`，用户 `timidly`）。
- 项目目录：`/home/timidly/QuantumAtlas`，venv `.venv`（Python 3.13）。
- 服务：`quantum-atlas.service`（systemd，enabled）。
- **sudo 必须人工输密码**（无 NOPASSWD），按 `remote` skill Plain 模式：
  本地写 `/tmp/qa-<task>.sh` → scp 推 → 给用户一条
  `ssh -t 1810 "sudo bash /tmp/qa-<task>.sh"`。
- prod RAW_DIR 是 CIFS 挂载 `//Quantum/Team` → `/mnt/team`（autofs，账号 `tmy`）。

## 工作约束

- **不要写 `.env`** 到提交中；只改 `.env.example`。
- 改 client 代码（`atlas/client/`、`atlas/cli.py`、`atlas/client/_common.py`）
  时不要引入对 server 路径（RAW_DIR / DATA_DIR / Neo4j）的硬依赖。
- 删除文件用 `trash-put` 而不是 `rm`。
- 文档（README / `docs/`）和实际代码、`.env.example` 必须保持一致；
  发现不一致时同步更新而不是绕开。
