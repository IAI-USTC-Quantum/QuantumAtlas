# Paper catalog 重构：代理主键 papers + 子表 paper_assets

_细化 ADR `0006` 中 `paper_works` catalog（目录）的物理形态；其中"PostgreSQL，一个中心化存储"的决策不变。_

paper catalog 最初是一张单独的 `paper_works` 表，以 `arxiv_id text PRIMARY KEY` 为键，带有
`identifier_scheme` 判别列，为仅 DOI 的贡献生成合成主键 `"doi:<doi>"`，并且**内联所有 asset 状态**
（`has_pdf` / `pdf_path` / `md_path` / `image_count` / …）。这种形态混淆了三件可分离的事
——paper 的*身份*、某个具体的*外部 id*、以及单个 *asset*——也无法表示真实 OpenAlex 世界中一个 work
拥有**多个 PDF**（arXiv v1/v2/… 加上出版版本）的情况。我们将它拆成一个使用代理主键的 **`papers`**
catalog 和一个子表 **`paper_assets`**（grill Q1–Q21）。

## 决策

**`papers`** —— 每个 work 一行，身份与任何外部 id 解耦：

- `paper_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY` —— 代理主键。PostgreSQL 没有
  `AUTO_INCREMENT`/`UNSIGNED`；惯用写法是 `IDENTITY`（Q1）。**qatlas-lean 通过这个数字型
  `paper_id` 引用 QA paper**（记录用 paper 指针），*不是* `kind:id` 字符串——这不同于 Claim
  的书目 `references`，后者仍保持 `kind:id`（ADR `0007`）。
- 三个可空外部 id 列，**每个都是 `UNIQUE`**：`paper_arxiv_id` / `paper_doi` /
  `paper_openalex_id`。**列名就是 scheme**，所以没有 `scheme` 列，也没有 id 子表；三个独立的
  `UNIQUE` 已经同时回答"根据这个 id 找 paper"和"取这个 paper 的 X 类 id"（Q2）。PostgreSQL
  `UNIQUE` 允许多个 `NULL`，所以**不要**使用 `NULLS NOT DISTINCT`。`paper_arxiv_id` 存储**基础、无版本**
  id（`2208.06941`），从 OpenAlex arXiv location URL 提取，绝不取自 `ids.arxiv`（Q17）。
- `paper_openalex_id` 带有一个**可空 FK → `openalex_works(openalex_id) ON DELETE SET NULL`**
  （Q14）： "有 openalex_id ⟺ 该 work 在 corpus 中"，因此 FK 永不阻塞写入；如果该 work 之后从快照中移除，
  该列会变为 `NULL` 并等待回填。corpus 是一个**启动时创建、懒加载填充的 write-through cache**（ADR
  `0006`）：`openalex_works` 在启动时存在，而任何赋值 `paper_openalex_id` 的查询都已经抓取并写入该
  work（fetch-on-miss write-through），所以不需要批量预加载也能保持不变量。因此**没有启动顺序依赖**——
  FK 在两张表都存在后由幂等、有 guard 的 `DO` block 添加，而 `ensureCatalogSchema` 会先于 catalog
  应用 corpus 基础模式。
- `paper_ref text GENERATED ALWAYS AS (…) STORED` —— 规范 `kind:id`，优先级为
  **openalex > arxiv > doi**（Q2）。
- `paper_default_asset_id bigint REFERENCES paper_assets(asset_id)` —— 裸 by-paper 读取解析到的
  asset（Q19），由 trigger 维护（策略见下）。
- `CHECK (paper_arxiv_id IS NOT NULL OR paper_doi IS NOT NULL OR paper_openalex_id IS NOT NULL)`。
- 列名保留 `paper_` 前缀（Q3）；表从 `paper_works` 重命名为 `papers`（Q4，一次重命名迁移）。
  这里不再放任何 asset 列——它们全部迁到 `paper_assets`（Q18）。

**`paper_assets`** —— 每个 PDF 一行，因为一个 work 可以有多个 PDF（Q18）：

- `asset_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY`;
  `paper_id bigint NOT NULL REFERENCES papers(paper_id) ON DELETE CASCADE`.
- `source text NOT NULL CHECK (source IN ('arxiv','published'))`;
  `arxiv_version int` —— 对 `arxiv` 非空，对 `published` 为 `NULL`（Q20）。
- `pdf_path text NOT NULL` —— **object-store key，不是 URL**（Q5）；`mineru_md_path` /
  `mineru_json_path` 也都是 key。asset 是否存在从 `*_path IS NOT NULL` **派生**（Q6）：
  不设 `has_pdf`/`has_md` 布尔值。
- 约束（Q7/Q20）：`CHECK ((mineru_md_path IS NULL) = (mineru_json_path IS NULL))`（MinerU
  要么两者都产出，要么都不产出）；上面的 source/version CHECK；`UNIQUE (paper_id, source,
  arxiv_version)`；一个**部分** `UNIQUE (paper_id) WHERE source='published'`（每个 paper 一个已出版 PDF，
  避开 `NULL` 版本带来的唯一性缺口）；以及显式的 `INDEX (paper_id)` —— PostgreSQL **不会**自动给 FK
  列建索引，否则"某个 paper 的所有 assets"会退化为 seq-scan。
- **默认 asset 策略（Q21）：** 如果存在 **published** asset，`paper_default_asset_id` 指向它；
  否则指向**最新** `arxiv`（最大 `arxiv_version`）。trigger 在 `paper_assets` insert/delete 时重新计算。

## 为什么选择代理主键 + 子表，而不是 arxiv-PK 单表

- **一个 work 不是一个 id，也不是一个 PDF。** 旧的 `arxiv_id` PK 强迫 paper *等同于*它的 arXiv
  id，并把仅 DOI 的情况藏在合成 `"doi:<doi>"` key 后面——这是把 scheme 泄漏进主键的字符串 hack。
  代理主键 `paper_id` 允许同一行同时拥有 arXiv id、DOI 和 OpenAlex id，且三者各自独立唯一。
- **多个 PDF 是常态。** arXiv v1/v2 加上出版版本是三组不同字节，且各有不同 MinerU 输出；在 paper
  上内联一组 `pdf_path`/`md_path` 无法容纳。子表让每个 PDF 都成为一等行，拥有自己的版本和转换状态。
- **跨仓引用需要稳定身份。** 数字型 `paper_id` 是紧凑、无 scheme 的 handle，qatlas-lean 可以存储它，
  而不耦合到该 paper 恰好拥有哪些外部 id。

## 备选方案

- **保留 `arxiv_id` PK + `identifier_scheme` 判别列（现状）。** 否决：无法建模 multi-asset works；
  合成 `"doi:"` PK 是 hack；身份被焊死在单个 external id 上。
- **在 `papers` 上内联 asset 列。** 否决（Q18）：一个 work → 多个 PDF；一组 `pdf_path`/`md_path`
  列只能容纳一个。
- **外部 id 子表（`paper_ids(paper_id, scheme, value)`）。** 否决（Q2）：三个以 scheme *作为列名*
  的 `UNIQUE` 列更简单、索引更干净，而且正好只有三个 scheme——通用 key/value 子表只会增加 join，没有收益。

## 影响

- **破坏性迁移 + 数据迁移（Phase A）。** `paper_works` → `papers` 重命名；从旧内联列回填
  `paper_assets`；从 `arxiv_id` / `identifier_scheme` / `doi` / `openalex_id` 填充三个外部 id 列；
  删除 `"doi:<doi>"` 合成 key。涉及 `internal/papers/{schema,store,doi_store,sync,lookup,claims,ids}.go`、
  `internal/routes/papers.go` + `papers_lookup.go` 及其测试。按 `docs/adr` 相邻计划排序执行；
  不是一次性完成。
- **ADR `0007` 被细化。** 其中的"记录用 paper `paper.id`，由 QA 的 asset pipeline 版本化"现在更精确：
  记录用 paper 是**代理主键 `papers.paper_id`**（永不版本化）；PDF **版本存在于每个 asset**
  的 `paper_assets.arxiv_version` 中。只读 SQL 协议的表清单变为 `papers` + `paper_assets`
  （+ `openalex_works`）。
- **读取路径（ADR `0011`）。** by-id 解析沿 `papers`（三个 UNIQUE 列）→
  `paper_default_asset_id` → asset 的存储 key。服务由 `QATLAS_PAPER_ACCESS_ENABLED` 门控（compliance）；
  当提供服务时，PDF 默认返回 RustFS 链接，markdown/JSON 默认返回字节流，二者都可复写。
