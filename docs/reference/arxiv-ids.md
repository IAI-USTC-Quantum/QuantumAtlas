# arXiv ID 格式

QuantumAtlas 以 arXiv ID 为论文的主键。两种格式 + 版本后缀规则需要规范化。

## 两种格式

| 时代 | 格式 | 例子 |
|---|---|---|
| **旧式**（≤ 2007-03） | `<category>/<7-digit>` | `quant-ph/9508027`、`cs.IT/0501045` |
| **新式**（2007-04 起） | `<YYMM>.<4 或 5-digit>` | `2501.00010`、`2403.12345` |

注意旧式的 `category` 部分**含斜杠**，会出现在 URL path 里。server 端 `splitPapersPath` 按"最后一段"做 action / arxiv_id 切分能处理。

## 版本后缀 `vN`

任何 arXiv 论文都可以有多个版本（v1、v2、...）。

- **`qatlas ingest <id>`** —— 可不带版本，server 自动取 arXiv 当前最新版
- **`qatlas upload pdf/markdown <id>`** —— **必须带版本**（`v1` 等），因为对象寻址按 `<id>v<n>` 命名
- **`qatlas mineru <id>`** —— 同样要带版本
- **`qatlas wiki show paper-arxiv-<id>v<n>`** —— Wiki paper page 也按版本区分

## 对象寻址映射

server 端把 arxiv_id 拆成 prefix + filename：

| arxiv_id | prefix | object key（PDF） |
|---|---|---|
| `quant-ph/9508027v1` | `9508`（前 4 位）| `pdf/9508/9508027v1.pdf` |
| `2501.00010v1` | `2501`（前 4 位）| `pdf/2501/2501.00010v1.pdf` |
| `2403.12345v2` | `2403` | `pdf/2403/2403.12345v2.pdf` |

跟 arxiv.org 的 [bulk-source](https://info.arxiv.org/help/bulk_data.html) 目录约定一致。

完整规则在 `internal/paperassets/path.go`。

## kind 后缀

每篇论文除了 PDF 还可能有：

| kind | key 模板 | 内容 |
|---|---|---|
| `pdf` | `pdf/<prefix>/<id>v<n>.pdf` | 原始 PDF |
| `md` | `md/<prefix>/<id>v<n>.md` | 解析后的 markdown（MinerU）|
| `json` | `json/<prefix>/<id>v<n>.json` | arXiv 元数据（题目 / 作者 / abstract 等）|
| `images/...` | `images/<prefix>/<id>v<n>/...` | MinerU 解析出的图片 |

`qatlas upload pdf --pdf` 把 PDF 字节落到 `pdf/`。论文元数据（题目 / 作者 / 摘要 / 引用）走 OpenAlex 上游同步进 Neo4j catalog，不再通过 upload 端点写 `json/`（v0.7.0 起；该前缀仅保留兼容历史对象的读路径）。

## 规范化函数

server 端 `internal/paperassets/path.go::Canonical` 把外部输入规范化：

- 去多余空白
- 转小写
- 统一 separator
- 旧式补全 category 大小写

client 端不强制规范化——传啥就发啥，server 端兜底。

## 常见坑

!!! failure "upload 时漏掉 `v1`"
    ```
    qatlas upload pdf 2501.00010 --pdf paper.pdf
    ```
    server 返回 400 `arxiv_id must include version suffix`。改成 `2501.00010v1`。

!!! failure "用了 `arxiv.org/abs/...` 完整 URL"
    server 只接受 ID 部分。从 URL `https://arxiv.org/abs/2501.00010` 抽出 `2501.00010` 就行。

!!! failure "旧式带版本"
    `quant-ph/9508027v1`（含斜杠 + 版本）—— 是合法的，正常工作。

## 跟 Wiki page id 的关系

Wiki paper 页面 id 规则：`paper-arxiv-<规范化 id 含 v>`

| arxiv_id | Wiki page id |
|---|---|
| `2501.00010v1` | `paper-arxiv-2501.00010v1` |
| `quant-ph/9508027v1` | `paper-arxiv-quant-ph-9508027v1`（斜杠转 `-`）|

文件名同上，`.md` 结尾。
