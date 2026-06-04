# Terms of Service

> **版本**：v1（2026-05-31）
> **生效**：本文档发布起。重大修订会在
> [GitHub releases](https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases) 公告。

## 定位

QuantumAtlas 是 **University of Science and Technology of China (USTC) 量子算法
研究团队**维护的**研究 / 教育用**项目，不是商业服务。本服务**按现状（"as-is"）
提供**，不附带服务等级承诺（uptime / 响应时间 / 数据保留 SLA 均不保证）。

公开实例：

- <https://quantum-atlas.ai>

也可自行部署（见 [安装](https://github.com/IAI-USTC-Quantum/QuantumAtlas#安装)）。

## 允许的使用

- ✅ 个人研究、学习量子算法
- ✅ 学术论文引用、教学课件中引用
- ✅ 在 Wiki 中贡献概念 / 算法 / paper 笔记
- ✅ 通过 PAT 用脚本批量查询 metadata
- ✅ Fork 本项目自部署、二次开发

## 不允许的使用

- ❌ **商业产品集成**（包括 SaaS / 付费 API 转售）
- ❌ **大规模无视 rate limit 的爬取**（如果给服务造成负担，我们可能撤销 PAT）
- ❌ **滥用 `/api/graph/query`** 跑病态 Cypher（如无界笛卡尔积）拖垮 Neo4j——
  detail 见 [鉴权模型](../concepts/auth-model.md#graph-查询同-scope-下危害最大的那一档)
- ❌ **拿用户数据训练公开 LLM 模型**——Wiki 内容不构成"公开 corpus"

## 数据归属

详见 [License & Attribution](license-and-attribution.md)。摘要：

- 论文 **metadata** 来自 OpenAlex（CC0）、Crossref（CC0）、arXiv（read access）
- 论文 **PDF 字节与解析后的 markdown 全文** 在 **quantum-atlas.ai 公开实例上
  不通过 API 对外分发**——原始 PDF 请到 arXiv 等上游获取。Self-hosted 部署
  方可通过 `QATLAS_ASSET_DOWNLOADS_ENABLED` 在受控范围内启用对内下载，
  此时由部署方承担分发义务，详见 [License & Attribution · 资产下载开关](license-and-attribution.md#资产下载开关-self-hosted)
- **Wiki 内容** Apache-2.0，在 [QuantumAtlas-Wiki repo](https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki) 自维护

## 账户 & 凭据

- 通过 **GitHub OAuth** 登录（不存密码，登出即清 PocketBase session）
- **PAT（Personal Access Token）** 自助创建，强制选 scope，默认空集 = 什么都调不了
- PAT 默认有效期 1-365 天，**到期自动失效**，自己续期
- 你对自己 PAT 的使用负责；**怀疑泄露立即撤销**（在 `/pat` 页面或调 `DELETE /api/pat/{id}`）
- 我们保留**单方面撤销 PAT** 的权利（滥用、安全事件、合规要求等）

## 隐私

- 浏览公开端点（health / server-info / install-qatlasd / SPA 外壳）
  **不留可追踪日志**（只有 Caddy access log，常规 IP/UA/path/status，按 Caddy 默认轮转）
- 登录后调写口或受 scope 保护的读口：**会记录** `accessKey` / `principalId` /
  IP / 时间到审计桶 `qatlas-s3-events`（详见
  [RustFS 部署 · 写入留痕](../deployment/rustfs.md#写入留痕-audit-sink-t10)）。
  这是**反滥用 + 取证**用，不分析用户行为也不用于推荐 / 广告
- 上传到对象桶的内容**全员可见**（同登录用户读得到，本服务不做
  per-user 隔离）——不要上传敏感 / 私密内容

## 终止

- 你**随时**可停止使用，撤销所有 PAT
- **删除账号**：提 [GitHub issue](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues)
  或邮件联系（[维护者](credits.md#维护者)）；我们删 PocketBase 用户记录 +
  你创建的 claim / 上传记录。OpenAlex 上的 metadata 不归我们管，要删请直接联系上游
- 我们也保留**关停账号**的权利（违反本 ToS、安全事件、法律要求等）

## 免责声明

```
QUANTUMATLAS IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
```

（即 [Apache-2.0 LICENSE](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/LICENSE)
免责段。同样适用于 QuantumAtlas 公开实例提供的服务。）

## 修订

本 ToS 有重大改动时会：

1. 在 [GitHub releases](https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases)
   显式说明
2. 在 SPA 顶部 banner 公告（用 mkdocs-material `announce.dismiss` 等同机制，
   未实施但已规划）
3. 本文件顶部"版本"字段递增

继续使用本服务 = 接受最新版 ToS。

## 联系

- [GitHub issues](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues)（推荐）
- 维护者邮箱见 [致谢 · 维护者](credits.md#维护者) 中各人 GitHub profile
