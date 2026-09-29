# qatlas 块级评论：设计决定与分阶段实施计划

> **这是 qatlas 评论产品的唯一交接文档，设计和执行步骤都在本文件。** 不需要再找“讨论报告”或“执行说明”配套阅读。本文替代此前误放在 Lean 仓库的两份混合文档。
>
> **最终方向**：在 qatlasd、qatlas-cli 和 qatlas Web 实现原件读取、MinerU 块定位及评论闭环。**不用 system／NEXT，不开发 NEXT 插件、双 CLI 桥接或数据库 fork。** lib 是后续使用者，不是本项目的开发位置。
>
> 状态：需求讨论与源码调研已完成；以下新功能尚未实现或部署。字段、路由、命令示例都是设计示意，未指定的名称由 Q0 收敛。

## 1. 这份文件交给谁、各自做什么

| 接手 agent | 工作仓库 | 应执行的阶段 | 不应修改 |
|---|---|---|---|
| qatlasd／主仓负责人 | `QuantumAtlas` | Q0、Q1、Q2；协调 Q5 | lib 和旧 system／NEXT |
| Web 负责人 | `QuantumAtlas` 的 `web` | Q4；参加 Q0、Q5 | qatlas-cli、lib |
| CLI 负责人 | `qatlas-cli` | Q3；参加 Q0、Q5 | 主仓后端、lib，除非另有分工 |
| 单个跨仓实施 agent | 人类明确授权的上述两仓 | 按 Q0→Q5 顺序 | 未授权仓库、生产数据 |

**发给 QuantumAtlas agent 或 qatlas-cli agent 的都是本文件。** CLI agent 在本文件读共享合同和 Q3，不需要另复制一份设计到 CLI 仓库。完成后更新各自已有产品文档/help，不另造平行方案。

lib 接入和旧材料清理是另一项工作，单独交给 lib agent：[lib 接入与清理计划](<../../../../qatlas-lean-dev/qatlas-lean-lib/docs/plans/qatlas-paper-integration.md>)。本文件不包含 lib 的执行阶段；主仓和 CLI agent 不需要读它才能开发，也不能提前清理 lib。该跨仓链接仅方便本机定位，不是产品运行路径或额外阅读依赖。

开工时先在负责仓库确认位置、HEAD、工作树和最新约定：

```sh
pwd
git status --short
git log -1 --format='%H %s'
```

本报告不是提交、发版、生产部署、付费解析或批量数据迁移的授权。执行权限以人类本次分派及各仓规则为准。不要覆盖并发改动，也不要把旧调研快照当作必须回退到的基线。

## 2. 产品目的与已确认边界

### 2.1 目的

缓存可复用的阅读知识：某个具体内容被谁质疑、使用什么原始材料查证、得出什么结论。后来的用户和 agent 能回看证据，减少重复劳动，而不是只看到一条没有出处的“论文有错”。

最小闭环：

```text
qa_ 论文身份 → 明确来源版本/解析结果 → JSON block
              ↓                         ↓
           原 PDF/原图  ← 定位 → 讨论、回复、状态历史
                                   ↓
                           API / CLI / Web 共用
```

### 2.2 已确认决定

1. 论文业务身份只用 qatlas `qa_…`。PDF、解析修订、block 和评论有各自技术标识，不是第二套论文 ID。
2. 先保留可定位原始材料，再建立评论；原 PDF 和 MinerU 原始 JSON／MD／ZIP 不被评论覆盖。
3. 评论绑定**某份解析 JSON 中的 block**；一个块可以有多个独立讨论，各自回复和改变状态。
4. 第一版只做评论及回复、问题状态，不把笔记、摘要、标签、查证、勘误等拆成七套产品。
5. 少量预设类型：普通、转录有误、原文笔误；允许 agent 使用新类型。
6. 内容作用域如公共、Lean 只做分类和筛选，不隔离可见性，不是账号权限 scope。
7. 问题状态为待查证、已确认、已撤回；可重新打开，保留理由和历史。已确认不等于已修复，也不等于平台认证论文正确。
8. 发起者和维护者可改状态；其他有写权限者通过回复提供证据。
9. 评论记录时间、实际认证账户、模型信息和修订；正文有宽松上限。
10. 所有已登录普通用户可看原文；agent 按所属账户权限与获授 token scope 操作，不另设“agent 不可看原件”的限制。
11. REST API 同时提供原件读取和“内容＋相关讨论”；CLI 主要做请求选择、过滤和格式化，Web 使用同一 API。
12. 面板能看原文、显示 block、关联讨论。可从阅读进入讨论，也可从本文 Issue 式列表定位回块。
13. 在任务授权及评论写 scope 内，agent 有新发现或查证结果时可自主贡献；优先回复已有讨论，不要求每次阅读留评，不逐条重新询问。
14. 先开发并验收 qatlas；等可用后才安排 lib 接入及选择性旧材料清理。

**没有拍板的实现细节**：20,000 字符只是宽松上限建议；API/CLI/表名、维护者精确映射、自定义类型如何启用问题状态、正文编辑权限、面板默认布局及旧格式支持范围由 Q0 明确。

### 2.3 明确不做

- 不使用 NEXT 的工作区、fork、token、grant/inject 或运维 CLI；此前探索该方向是为了复用数据库 fork 和面板，之后用户明确放弃。不是删除或停用已有服务的授权。
- 不新增 Lean CLI 插件、`qatlas lean records`／`qatlas contrib lean` 这类旧桥接设计。
- 不做独立自动摘要/tag聚合、全站问题运营台、复杂通知、排行、信誉评分或按投票数认证真理。摘要/标签可作为普通正文或自定义类型，暂不建立专门功能。
- 不搬 PR 分支/合并/审批流程；借鉴 Issue 的讨论、回复、状态和历史即可。
- 不要求评论先审成 Claim，不增加数学准入门槛。
- 不自动纠正原始 MD/JSON。未来如做修正版，应是独立派生产物，不在第一版范围。
- 不等待全量论文重解析或旧 metadata 迁移才交付；不在本仓维护 lib 的迁移清单。

## 3. 现有基础、缺口及调研范围

这是静态代码结论，不代表现役服务状态。曾用本机 CLI 查询一次元数据，返回 401，并提示客户端版本落后；未升级认证、运行真实解析或做业务写入。

| 对象 | 核对快照 | 已有与缺口 |
|---|---|---|
| QuantumAtlas | `fa0202f99cc2f9c5698cef6e86442b5c16056747` | Go 服务、PostgreSQL registry、PocketBase 登录/PAT、对象存储和 React 面板；还没有本计划的块级评论 |
| qatlas-cli | `9b76d0d1ebb58e2be4b65164cd9dc86f39547ab9` | 源码声明 0.34.1rc1，本机 help 所用安装版 0.34.0；现有 paper/get/contrib 不含评论闭环 |
| MinerU | `fa91b100f5350731064bd267008ca44302589417`，版本声明 4.0.9 | 本次真正核对的新版；未安装或实际运行 |

沿用现有基础，不新建后端服务、账户库、OCR 或通用文档库。具体接点：

- [registry](<../../internal/registry/registry.go>)、[身份键](<../../internal/registry/identity.go>)、[表结构](<../../internal/registry/migrations/00001_init.sql>)：已有论文与资产身份；部分合并别名读取未贯通。
- [MinerU 结果读取](<../../internal/mineru/client.go>) 与 [上传路由](<../../internal/routes/papers.go>)：CLI 已上传完整 ZIP，服务端只取 `full.md` 和 images 后丢弃原包；不能假设现有数据已经有 JSON、坐标或稳定 block。写了 `mineru_json_path` 不证明对象存在。
- [普通 PDF 路由](<../../internal/routes/papers_pdf.go>) 当前固定 410；管理员原件预览是另一条受限管理通道，不可拿来代替普通用户 API。
- [用户认证](<../../internal/routes/auth.go>)：用户 PAT 绑定账户；系统 PAT 可能 `re.Auth=nil`；session 通过 scope 不等于拥有所有评论的修改权。既有 adminGuard 拒绝 PAT，不能直接用它限制“有管理权限的用户 agent”。
- [论文详情页](<../../web/src/routes/$lang.papers.$paperId.tsx>) 当前主要是身份、获取状态、资产表与管理员弹窗；尚不是块级阅读器。
- [现有预览边界](<../../web/MARKDOWN_PREVIEW.md>) 包含受限 Markdown/KaTeX、资源预算和账号隔离；新增评论和 PDF 功能不能破坏这些约束。
- CLI 的旧 arXiv 获取路径可能去掉 vN 后沿返回 PDF 链接下载，出现旧文件名配新内容的风险。指定来源必须核实，不能把输入字符串当 pin。
- 现有 `paper get markdown` 在缓存缺失时会触发下载/转换；只读 get 与 ensure/acquire 副作用需要明确，不自动扩展成投稿。

## 4. 论文、源文件、解析修订和块的身份

### 4.1 qa_ 与外部标识

`paper_id = qa_ + 26位小写ULID`，总长29，独立生成，不是 DOI/arXiv/OpenAlex 的编码或哈希。

论文表上的 DOI、arXiv、OpenAlex ID 各自可空且唯一；arXiv 存不带 vN 的基本 ID，不同 vN 是同一作品下的不同资产。DOI 去常见前缀并小写。OpenAlex 工作 ID 是外部关联，不是必需主键。

identity 表目前有 DOI、bare arXiv、versioned arXiv、title hash；OpenAlex 通过论文表列匹配。`ResolveOrMint` 当前需要 DOI 或 arXiv，仅标题/仅 OpenAlex 不能通过该入口 mint。`paper_ref` 按 OpenAlex→arXiv→DOI 选择外部引用，可能变化，不替代 paper_id。

跨标识合并保留最早创建的 qa_，旧行标记 `merged_into`。新 API 必须规范解析并保留可追溯别名；不能丢评论、复制讨论或将旧引用默默指向别的原件。

### 4.2 永久评论锚点

至少能确定：

```text
canonical qa_
+ 源PDF身份（来源版本/资产及SHA-256）
+ 不可变解析产物身份（修订/JSON SHA-256）
+ schema与schema_version
+ 原page_idx、顶层block.index
```

原 PDF 与解析结果要绑定实际输入。可返回可读 locator、bbox、短摘录和网页链接辅助使用，但它们不能代替上述身份。

原件 hash 证明字节身份，不独自证明 OCR 语义。新解析更新“当前结果”指针，不覆写旧产物；旧评论始终回到当时版本，不按相似文本或同号块自动迁移。

## 5. 新 MinerU CLI 与定位：不可混用的事实

本节固定到 4.0.9 调研提交。早先研究的 `mineru-open-api extract -f json` 属于 MinerU-Ecosystem Open API CLI，返回旧式 ContentList，不是这里的新 Doclib 机制。

| 新入口/产物 | 语义 |
|---|---|
| `mineru` | 本地 Doclib：文件入库、缓存、搜索、渐进阅读 |
| `mineru-kit` | 无状态解析、V1 解析服务、模型/WebUI工具 |
| `mineru parse --json` | 命令响应信封，含状态、阅读内容、locator、续读信息；不是原始全文 Middle JSON |
| Middle JSON | `schema=docvortex.middle`、`schema_version=2.0` 的原始结构化模型 |
| Structured Content / ContentList | 消费视图，与原始 Middle JSON 区分 |

### 5.1 Locator

```text
doc:<short_id>/tier:<tier>/page:<page_no>/block:<block_no>[/char:<offset>]
```

- short_id 从源文件 SHA-256 前7位开始，碰撞时加长后持久化，按精确值查询；不是任意hash前缀，也不是qatlas ID。
- public page/block 从1开始，对应 `page_idx+1`、`block.index+1`。根据 `index` 查块，不能使用数组下标或可见 Markdown 段落序号。跳过空输出不重编号。
- char 是单块渲染并 strip 后字符串的0起始字符偏移，不是 JSON/UTF-8 字节偏移。
- `read` 读已有缓存，不自动重新解析；Doclib search 是本地搜索，不是多源论文搜索。
- 新 Middle JSON PDF 顶层 bbox 用 `[0,1]`；旧 ContentList `[0,1000]` 或旧页面坐标需单独profile，不能硬套。
- locator 不含 parse ID/JSON hash；同PDF同tier重解析会选择当前新结果，故不能单独作为永久评论锚点。

### 5.2 原图核对与完整性

```sh
# 已存在的MinerU4命令示意；short_id必须用实际返回值。
mineru parse paper.pdf --pages all --tier standard --json
mineru read 'doc:ab12cd3/tier:standard/page:5/block:12' --context 1 --json
mineru read 'doc:ab12cd3/tier:standard/page:5/block:12' --format image -o block.png
```

有有效 bbox 时，块图来自**原PDF渲染后裁剪**，不是识别出的LaTeX重绘。固定源码中无bbox时尝试内嵌 `image_base64`，并非旧 Draft 文档中的通用 sidecar fallback；有bbox但源缺失不会伪造图。缓存文字仍可读不等于原图可用。

Doclib 源路径选择检查记录的sha对应路径存在，但不在每次裁图前重新计算实际文件hash；qatlas必须保证源资产不可变或核实字节，不能照搬存在性判断当证据。

默认 parse 仅PDF前10页，全文需 `--pages all`；stdout约30,000字符且可续读，请求all不等于一次输出完整。文件导出只覆盖所请求页范围。等待超时不是任务取消，不能盲目创建新任务。结构JSON序列化也不保证内含全部图像，必须保管ZIP、成员和源PDF。

新V1是 uploads→parse jobs→files，旧 `/tasks`、`/file_parse` 与 hosted-v4 不是同一合同。首版须真实贯通至少一种新版产物来源，不能只重命名旧输出或只实现mock。

## 6. 评论设计：简单投稿，可追溯查证

### 6.1 最小组织

根评论发起讨论，绑定锚点、类型、内容作用域；回复归该讨论；状态属于讨论。普通笔记不强制问题状态。类型新增是新增有界数据，不加载代码或产生特权。

```text
公式块12
├─ 转录有误：根号范围错了？ → 已撤回
│   └─ 回复：对照原图发现是误读
├─ 原文笔误：是否漏负号？ → 待查证
│   └─ 回复：给出推导与位置
└─ Lean／普通：一个有限维实现的适用说明
```

公共/Lean 是共享分类，用户可以筛选；它们与 `papers:read` 等凭据 scope 是两种概念。建议面向Lean的读取包含公共和Lean讨论，具体默认值在Q0定。

### 6.2 状态与权限

| 状态 | 说明 |
|---|---|
| 待查证 | 提出了疑点，尚无明确结论 |
| 已确认 | 认为这个具体问题存在；不是平台认证，不等于已修复 |
| 已撤回 | 指控不成立或撤回；保留原内容和回复，不认证整篇论文无误 |

作者和维护者可改状态并留理由，可引用查证回复；新证据可重开。其他账户可回复，不取得改状态权限。

**正文编辑权限尚需明确，与改状态权限不同。** 建议按每条正文的作者校验：讨论发起者不能改别人回复；维护者代编如确有需求需显明编辑者，不伪装原作者。修订和结论引用绑定，不能无痕改掉“已确认”的依据。根主张编辑后是否自动重开，由Q0收敛。

### 6.3 留痕、上限与信任

- 服务端记录创建/修改时间、实际账户和认证方式、必要的非秘密凭据标识；不接收body自报author冒认。
- 客户端报告模型/版本，标为声明信息；人类可无模型，未知就未知。同账户不同模型仍是同账户，不自动算独立复核。
- 系统PAT无个人用户，必须有明确政策，不能伪装登录者。
- 正文宽松可配置；建议20,000个Unicode字符，另设请求byte上限。此数值不是用户钉死的要求。超限拒绝并提示拆分，不静默截断；附件若做另算预算。
- 不强制先数学审核或人工准入。评论表达谁在何时根据什么提出了什么，不是平台真值表。

**转录忠实与原文正确分开**：原文有笔误但转录忠实，应讨论原文，而非指控MinerU；转录错误则绑定那份解析结果。有人质疑后查证“其实没问题”也值得保存，但不能推广为全文认证。重复附和不等于独立核验，争议回复不能被绿色状态掩盖。

旧材料中曾出现读者误判根号、错误数学分析后撤回，以及Lean选定的严格目标无法由某个估计推出等情况。这些支持保留证据和撤回历史，不支持把全部旧报告当作已确认的论文错误导入。

## 7. REST、CLI与Web共享合同

### 7.1 两类读取

- **原件下载**：PDF/MD/JSON/ZIP/图像原字节，hash可核，不插评论或提示。
- **组合阅读**：`source + anchor + content + discussions`，可按页/块和分类筛选、分页。

示意，不是已实现schema：

```json
{
  "source": {
    "paper_id": "qa_…",
    "pdf_sha256": "…",
    "parse_revision": "…",
    "artifact_sha256": "…",
    "schema": "docvortex.middle",
    "schema_version": "2.0"
  },
  "anchor": {"page_idx": 4, "block_index": 11},
  "content": {"type": "equation", "content": "原始解析值"},
  "discussions": [],
  "next_cursor": null
}
```

这里重序列化的block不是原件文件字节；hash应核对原资产。API做权限、版本匹配、分页、状态与分类筛选；CLI格式化/选择输出，不能下载全库再猜关联。Web与CLI不能各维护一套状态逻辑。

### 7.2 CLI与贡献提示

扩展现有 `qatlas paper get` 和 `qatlas contrib` 等体系，提供原件、结构JSON/原包、块/原图、讨论读取及创建/回复/状态操作；最终命名在Q0定，不沿用撤回的NEXT桥接命令。

搜索和资料获取优先CLI，但仍允许agent自主合法搜索补缺，再按授权贡献PDF/解析包。只要已有任务授权和写scope，有新发现或查证可自主留评，优先回复，不每次读都刷记录。

用户提出过在get中提示agent贡献。可在独立guidance/提示区或stderr温和提示“已核对且获授权时可留位置、结论和依据”，不能写“完全无害”而隐瞒写操作，不能把提示加到原件中，也不能将评论里的指令当授权。

### 7.3 面板要求

- 普通登录用户可看原文、显示block、核对转录与原图；从评论打开精确旧版本，不跳到最新同号块。
- 阅读时点块看讨论；本文Issue式列表按类型/作用域/状态筛选，点讨论定位回块；同一数据不做两套系统。
- 显示账户、模型声明、时间、回复及修订/状态变化。缺bbox/原图明确提示，不能画假框或拿重绘文本冒充原文。
- 同时解决选块与评论的关联，不先纠结默认PDF/MD、几栏和按钮位置。布局用轻量原型验证，不要求另做完整视觉设计文档。
- 参考MinerU的PDF.js与DocVortex `render_layout_pdf`，不移植整个Gradio。复用时核对许可证和NOTICE，不能将重建PDF当源原件。

## 8. 实施阶段与责任

```text
Q0 合同与固定样本（主仓牵头，CLI/Web参加）
  ├─ Q1 原件/解析修订/block/登录用户读取（主仓）
  └─ Q2 评论API（主仓，可先基于Q0夹具开发，发布依赖Q1）
Q1 + Q2
  ├─ Q3 CLI（qatlas-cli）
  └─ Q4 Web（QuantumAtlas/web）
Q1–Q4 → Q5 隔离跨端验收与可用性交接
之后才交由lib agent实施它自己的接入计划
```

Q0–Q2共享后端合同须有单一负责人，避免并行覆盖迁移/路由/OpenAPI/DTO；Q3与Q4可并行，不各自发明block或评论语义。各阶段交付代码和测试，不以继续写长报告代替实现。

### Q0：最小合同和样本

**工作仓库**：QuantumAtlas牵头；CLI/Web只协商接口，不越仓修改。

**做什么**：

1. 核对当前源码与本报告快照差异，明确新产物通过贡献包、现有转换器扩展或受控V1适配器进入；不强迫整套provider重写。
2. 固定小PDF、真实/受控新版产物及合成夹具，标清来源；真实PDF外发/付费解析按任务授权执行。
3. 在现有API/DTO/OpenAPI、测试和必要的简短代码注释中固定身份、锚点、读写、类型/状态、分页/幂等和错误合同，不再增加平行讨论文档。
4. 定好接口/命令名、权限scope、维护者映射、系统PAT政策、正文编辑权限、自定义类型状态语义、正文预算、旧格式范围及评论缓存离线规则。只把改变已确认产品边界的问题回问用户，不逐字段增加审批。
5. 沿用配置入口，暴露必要上限、兼容能力、缓存根；不硬编码部署URL、账号或home路径。

**验收**：夹具能区分源v2/v3、同PDF两份解析、非连续block.index、局部page数组；有一个真实原页区域对照；后续agent可用相同锚点得到同一结果。合成“转录错/原文错”只是测试，不构成真实论文勘误。

### Q1：原件与块定位

**输入**：Q0合同和固定样本。**主要范围**：`internal/registry`、其migrations、`internal/mineru`、`internal/routes/papers*`、必要的paperassets/objstore和OpenAPI。

**做什么**：

- 统一canonical resolver；保留请求别名，不用title模糊自动造身份。
- 接收并保存真实原ZIP/JSON/MD/images及hash清单，正确支持选定新profile；旧`full.md`和新`markdown.md/middle_json.json`不能冒充兼容。
- 增加不可变解析修订及源PDF绑定；更新当前指针不覆写旧材料。旧记录缺JSON时明确缺失，不从MD伪造原JSON，不全量强制重解析。
- 原件读取和块定位必须选择实际来源版本；指定vN失败不得换最新版/期刊版。
- 普通论文API提供登录用户/PAT scope可读的PDF与原页/裁图；不要整体放开admin资产通道。
- 原件hash验证、原子发布、bbox/profile和源页映射正确；不返回ready指向不存在对象。

**验收**：按qa_+修订读JSON并回看同源原图；测源版本/重解析、canonical merge、非连续index、坐标单位/缩放/旋转、缺原图、坏schema/hash、ZIP穿越/重名/解压预算/图片缺口。匿名拒绝、普通登录可读、read PAT不能写，Range同样鉴权。没有JSON的旧记录不会假称可定位。

### Q2：评论、回复、状态和组合阅读API

**输入**：Q0；发布前必须接Q1真实锚点。**主要范围**：现有registry附近的comment领域、迁移、routes、PAT scopes及注册/OpenAPI；不建第二服务或账户库。

**做什么**：

- 实现多讨论、回复、预设/自定义类型、共享内容作用域、讨论状态、正文修订及事件历史。
- 评论业务与历史在所选数据库事务边界内一致；PocketBase认证与PG数据不能假称跨库天然原子。
- actor和时间来自服务端；模型为声明。scope与作者/维护者权限分别校验；系统PAT无user不得panic或冒名。
- 改状态留理由/依据，正文编辑按每条作者的合同处理；幂等创建/回复，并发更新有revision冲突。
- 实现本文/块讨论列表、详情、回复和状态读取，API分页筛选；原件与组合结果并列，不混写正文。

**验收**：A建讨论、B回复，A不能因此改B正文，C不能越权改A/B；作者/维护者改状态符合scope；撤回一组不影响另一组；自定义类型不产生权限；重试不重复、CAS冲突明确；旧评论不误挂新版；原件hash始终不变。编辑权限仍是Q0定下的工程合同，不增加逐条学术审批。

### Q3：CLI原件/块/评论/贡献及通用缓存

**工作仓库**：qatlas-cli。**输入**：Q1/Q2 API、OpenAPI与固定样本；mock可先做，退出需实连。**主要范围**：`src/qatlas/cli.py`、client/paper/contrib/common/auth/config、必要的parser/arxiv_fetcher、tests和现有help/docs。

**做什么**：

- 原件、原JSON/ZIP、block、原图、讨论列表/详情；发起、回复、类型发现与状态操作。
- 原字节下载、完整机器JSON与人读格式明确区分；stdout/stderr分离，完整导出不取截断阅读窗口。
- 支持qa_和明确解析/来源修订；修正旧式arXiv去版本风险；未知记录可作本地候选，不伪造qa_。
- 复用既有HTTP/凭据/错误处理和版本协商；同major/minor或文档组件锁不代表新能力必然存在，需能力探测/明确unsupported。
- 通用缓存根可配置，按服务来源、qa_和固定hash复用，下载校验后原子发布，能并发去重。**CLI不硬编码Lean路径，不负责创建lib的软链接或改其.gitignore。**
- 原始资产与可变评论分开；最简可不持久缓存评论。若缓存，在线校验/刷新、离线标cached_at/stale、账户隔离，401不能用旧缓存伪装成功。不因token撤销删除此前合法下载原件。
- 外部合法补缺资料可经contrib上传，保持旧lease/幂等边界；不把读操作扩成自动投稿。

**验收**：字节hash、分页、Unicode上限、错误退出、旧服务不支持、401/403/409/413/429、写超时未知结果、并发缓存、错误HTML不当PDF、撤回后刷新、离线陈旧、切账户及撤销token；不泄露凭据。help只描述已实现命令。

### Q4：原文/block/讨论面板

**工作仓库**：QuantumAtlas/web。**输入**：Q1/Q2。**主要范围**：现有论文详情、components、API/queries、i18n和浏览器测试。

**做什么**：

- 普通用户通过普通API看原件，显示来源版本、解析修订和block；不只移除前端admin判断。
- block与讨论双向定位；本文讨论列表筛选，独立讨论各自回复/状态。
- 历史评论打开历史材料；能对照原图、转录及必要上下文，无法读取时清楚说明。
- 展示真实账户、模型声明、时间、修订和状态理由；保留相反证据，不用绿色状态掩盖争议。
- 防账号/论文切换、迟到请求或编辑中换块导致误显示/误投。复用受限Markdown/KaTeX、资源预算和blob生命周期。

**验收**：普通用户/作者/他人/维护者行为正确；PDF缩放旋转后框一致；多个讨论不串；旧链接准确定位；XSS/恶意链接只作数据；大文件渐进读取不冒充全文。布局不另成产品审批流程。

### Q5：跨端验收及给下游的交付

**负责人**：主仓牵头，CLI/Web协同。**输入**：Q1–Q4候选实现。

在隔离测试环境，以两个普通用户和一个维护者跑一篇论文：

1. 得到真实qa_与固定源PDF；通过支持路径保存新版原始解析。
2. CLI下载原件、定位block和原图；Web看到同一份材料。
3. 同块两个讨论，回复并撤回一个，另一个不变；模型/用户/时间/修订可查。
4. 另一agent通过组合API读内容+相关评论，读取不自动投稿。
5. 重解析/新源版本/合并别名不使旧评论串位、丢失或重复。
6. 权限、撤销token、并发/重试按合同生效；原件hash不变。
7. 两个普通客户端目录复用固定原件缓存；不需要先改真实lib。
8. 服务重启后历史原件与讨论仍可读，迁移/备份恢复或回滚边界有验证依据。

单元/合成测试、临时PG实测、浏览器mock、三端实连、真实MinerU烟测、发行构建和生产部署分别记结果。前一层不冒充后一层；没有环境的项目明确未跑，不降低门槛伪造成功。

**交给lib agent的输入清单**（在实际交接消息/PR中提供，不再新增重复设计）：

- 可使用的服务与CLI版本、已实现命令及API说明；
- qa_/source/parse/block固定引用格式，原件/组合读取差异；
- 可配置缓存根的方式、权限scope和错误行为；
- 一篇已跑通论文的非秘密样本引用、评论及网页深链接；
- 已知旧格式/旧资料缺失限制、是否需要按需重解析。

这些输入交付且用户安排后，才轮到lib计划。开发完成不自动授权发版、现役迁移、批量上传或停用旧服务。

## 9. 验证入口和失败行为

以下是已核对的现有入口，开工时以当前仓库配置复核。隔离HOME/配置，清理业务测试目标与凭据环境，不加载部署.env；真实集成必须指向明确授权的可丢弃资源。

### 主仓（Q1/Q2/Q5）

参照 [贡献指南](<../contributing.md#跑测试>) 和 [CI](<../../.github/workflows/go.yml>)，先定向再整体：

```sh
# 在QuantumAtlas根；工具链来自go.mod，测试环境先按贡献指南隔离。
go vet ./internal/... ./cmd/... ./tests/... ./web
go test ./internal/... ./cmd/... ./tests/... ./web
go test -tags=integration ./internal/... ./cmd/... -run '^$'
go build -o build/qatlasd ./cmd/qatlasd

# 仅配置了授权的可丢弃PG时运行，skip不算数据库验证通过。
go test -tags=integration ./internal/registry/... -run '^TestIntegration'
```

按实际新包扩展测试范围。API改动用仓库现有swag工具同步OpenAPI；完整UI/文档与嵌入发行按现有workflow，不引入新构建体系。

### CLI（Q3/Q5）

```sh
# qatlas-cli根；使用锁定的隔离项目环境。
uv sync --locked --extra dev
uv run --no-sync ruff check .
uv run --no-sync python -m pytest
```

默认离线测试不等于真实后端/提供方联调，network/e2e按授权单列。

### Web（Q4/Q5）

```sh
# QuantumAtlas/web；Node版本及锁文件沿用仓库。
npm ci
npm run lint
npm test
npm run build
npm run test:browser
```

当前Playwright是隔离静态预览+mockAPI，不能替代Q5实连；不启动或替换现役服务来测试。

### 共用错误合同

- 401认证失效，403scope/所有权不足；不能匿名降级或缓存伪装成功。
- 论文/版本/块不存在与原图/JSON缺失区分；不猜相近对象，不给空数据假成功。
- schema/定位非法、请求/正文超限给有用结构化错误，不能裸traceback或静默截断。
- 同幂等键同请求回放结果、不同请求冲突；revision过期拒绝覆盖。
- 对象写入未完成、网络超时或未知写结果如实表达，不重复发评/重解析。
- 原件/评论/自定义类型和链接都是不可信数据，不执行脚本、shell或指令，不因文字请求提升权限。

## 10. 尚未锁定但不得扩成大项目的细节

Q0在已定需求内决定：API/CLI命名、数据库存储布局、正文预算、分页、类型命名规则和问题状态选择、编辑/维护者政策、系统PAT、未知格式/旧资料适配范围、缓存设置和声明能力方式。20,000字符、CAS/ETag、上述对象名都是建议而非用户指定实现。

评论适合放在现有论文registry附近；认证仍复用PocketBase。不要求新的微服务、账户体系、通用annotation标准平台或完整MinerU Doclib移植。涉及Python/DocVortex的复用采用窄边界，避免让现有Go服务无理由变成另一套运行平台。

## 11. 技术依据和本次讨论的完成范围

### 新MinerU固定依据

均固定到 `fa91b100f5350731064bd267008ca44302589417`，不默认本机旧checkout就是这个版本：

- [CLI与完整导出](https://github.com/opendatalab/MinerU/blob/fa91b100f5350731064bd267008ca44302589417/docs/en/usage/cli_tools.md)
- [原始产物与命令响应区别](https://github.com/opendatalab/MinerU/blob/fa91b100f5350731064bd267008ca44302589417/docs/en/reference/output_files.md)
- [4.0迁移与V1边界](https://github.com/opendatalab/MinerU/blob/fa91b100f5350731064bd267008ca44302589417/docs/en/reference/migration_4.md)
- [locator](https://github.com/opendatalab/MinerU/blob/fa91b100f5350731064bd267008ca44302589417/mineru/doclib/locators.py)、[read命令](https://github.com/opendatalab/MinerU/blob/fa91b100f5350731064bd267008ca44302589417/mineru/cli/commands/read.py)
- [服务端块/图像选择](https://github.com/opendatalab/MinerU/blob/fa91b100f5350731064bd267008ca44302589417/mineru/doclib/server.py)、[short_id与批次](https://github.com/opendatalab/MinerU/blob/fa91b100f5350731064bd267008ca44302589417/mineru/doclib/services/parse_svc.py)
- [PDF布局叠框](https://github.com/opendatalab/MinerU/blob/fa91b100f5350731064bd267008ca44302589417/mineru/kit/gradio/preview.py)、[PDF.js适配](https://github.com/opendatalab/MinerU/blob/fa91b100f5350731064bd267008ca44302589417/mineru/kit/gradio/pdf_preview.py)

Draft与实现冲突时以固定源码/测试为准。复制/引入代码前核对该版本许可证和归属，不能笼统假定全部纯Apache或MIT。

### CLI固定依据

- [paper读取](https://github.com/IAI-USTC-Quantum/qatlas-cli/blob/9b76d0d1ebb58e2be4b65164cd9dc86f39547ab9/src/qatlas/client/paper.py)
- [contrib](https://github.com/IAI-USTC-Quantum/qatlas-cli/blob/9b76d0d1ebb58e2be4b65164cd9dc86f39547ab9/src/qatlas/client/contrib.py)
- [arXiv获取](https://github.com/IAI-USTC-Quantum/qatlas-cli/blob/9b76d0d1ebb58e2be4b65164cd9dc86f39547ab9/src/qatlas/parser/arxiv_fetcher.py)
- [工程与测试说明](https://github.com/IAI-USTC-Quantum/qatlas-cli/blob/9b76d0d1ebb58e2be4b65164cd9dc86f39547ab9/docs/overview.md)

本报告依据本会话37条用户消息、交互式选择和源码调研整理，已经区分最终决定、撤回路线和实现建议。原始会话仅在本地回读，未作为交付附件；没有实现这些新接口、跑新功能测试、发版或部署。

给执行者的任务只需写：“阅读本文件，在已授权仓库执行Q<N>；前置输入为<实际提交/接口/夹具>，允许的验证为<范围>。”结束时交付实际修改、提交/工作树状态、命令退出码、mock/实连/未跑项及下一阶段输入。不要再新增重复的总览、讨论报告或拆出第二份实施计划。

## 12. Q0 决议（2026-09-29，用户拍板后锁定）

本节是 Q0 的正式输出，与正文冲突时以本节为准。实施模式：**两仓全栈 Q0→Q5，智能体团队并行，速度优先**——主路径与核心验收先通，边缘情况列 TODO 清单不展开；Q0 未定事项执行者自行从简决定并记入 commit message，不阻塞等待。

### 12.1 用户已拍板

1. **维护者映射**：讨论 root 作者 + 平台管理员（users 表 is_admin/is_superadmin）。权限整体从简，不过度设计。
2. **正文编辑**：每条正文（root 与回复）作者可编辑自己的；服务端留修订历史并标注实际编辑者；root 编辑不自动重开状态。若修订历史实现过重可降级为仅记录编辑时间戳（难度取舍授权给实现者）。
3. **系统 PAT**：对评论一律只读；一切写操作要求用户 PAT/session，系统 PAT 写入返回 403。
4. **类型与状态正交**：类型只是有界字符串标签（预设 `normal`/`transcription_error`/`typo_in_original` + 自定义 slug，≤64 字符，`[a-z0-9_:-]+`）；所有讨论统一携带**可选**状态机（`pending`/`confirmed`/`retracted`，可空=无状态笔记），不按类型做属性开关。状态变更必留 reason 与历史，可重开。
5. **前端**：方案 A 偏中幅——单应用（现有 React 19 + TanStack + Tailwind/radix 栈）内做新阅读模块，新增 `pdfjs-dist` 6.x；PDF.js 交互参考 lean-system-next 的 `PdfViewer.tsx`（该仓无 LICENSE，只参考模式不逐行复制）与 MinerU `pdf_preview.py`；**原型先行**：Q4 先交可点击布局原型（mock API）供用户确认再接真 API。作用域读取默认：不筛=全部；筛 Lean=公共+Lean。

### 12.2 API 命名合同（v1，均挂 `/api/` 前缀，错误沿用现有 envelope）

**Q1 原件与块**（登录或 papers:read 可读，Range 同样鉴权）：

| 方法与路径 | 语义 |
|---|---|
| `GET /api/papers/{paper_id}/sources` | 源 PDF 资产列表（来源版本、sha256、size、当前指针） |
| `GET /api/papers/{paper_id}/sources/{source_id}/pdf` | PDF 原字节；`?version=vN` 指定失败不得换最新版 |
| `GET /api/papers/{paper_id}/parses` | 解析修订列表（schema、artifact_sha256、is_current） |
| `GET /api/papers/{paper_id}/parses/{revision}/json` | 原 JSON 字节（hash 可核） |
| `GET /api/papers/{paper_id}/parses/{revision}/blocks?page_idx=&cursor=&per_page=` | 顶层块分页（keyset 游标=最后块 id） |
| `GET /api/papers/{paper_id}/parses/{revision}/blocks/{page_idx}/{block_index}` | **组合阅读单块**：source+anchor+content+discussions+next_cursor |
| `GET …/blocks/{page_idx}/{block_index}/image` | 原图裁剪（原 PDF 渲染裁剪，非重绘）；缺 bbox/源缺失如实报错，不伪造 |

**Q2 评论**（读同上；写要求用户身份 + `comments:write` scope）：

| 方法与路径 | 语义 |
|---|---|
| `GET /api/papers/{paper_id}/discussions?scope=&type=&status=&page_idx=&block_index=&cursor=` | Issue 式列表 |
| `POST /api/papers/{paper_id}/discussions` | 创建根讨论；幂等键头 `Idempotency-Key` |
| `GET /api/discussions/{discussion_id}` | 详情（含回复分页） |
| `POST /api/discussions/{discussion_id}/replies` | 回复；同幂等键规则 |
| `PATCH /api/discussions/{discussion_id}/status` | 改状态（root 作者或管理员）；body 必含 `reason` |
| `PATCH /api/discussions/{discussion_id}/body`、`PATCH …/replies/{reply_id}/body` | 作者编辑自己正文；`If-Match: <revision>`，失配 409 |
| `GET /api/discussions/{discussion_id}/revisions` | 修订与状态历史 |

**通用合同**：正文上限 20,000 Unicode 字符（config `comments.max_body_chars` 可配）+ 请求体 1MB；分页 per_page 默认 20 上限 100；幂等键 SHA-256(方法+路径+body) 比对，同键同请求回放原结果、不同请求 409；ETag=revision 整数。模型声明字段 `model`（可选字符串，纯声明）。

### 12.3 存储布局（registry migrations，编号预分配防并行冲突）

- **00007（Q1）**：`paper_sources`（source_id、paper_id、origin 如 `arxiv:v2`/`upload`、sha256、objstore_key、size_bytes、created_at）；`parse_revisions`（revision_id、paper_id、source_id、schema、schema_version、artifact_sha256、objstore_key、created_at、is_current 唯一部分索引）。
- **00008（Q2）**：`comment_discussions`（discussion_id ULID、paper_id、parse_revision、page_idx、block_index、type、scope∈{public,lean}、status 可空、body、created_by、model、revision、created_at、updated_at）；`comment_replies`（reply_id、discussion_id、body、created_by、model、revision、created_at、updated_at）；`comment_status_events`（from/to/reason/actor/created_at）；`comment_body_revisions`（target/target_id/old/new/editor/created_at）。

### 12.4 CLI 命令（Q3，命名可微调但 help 需一致）

`qatlas paper pdf|parse-list|block-list|block-get|block-image`；`qatlas comments list|show|create|reply|status|edit`。缓存根可配置，按 服务来源+qa_+固定hash 复用，下载校验后原子发布。

### 12.5 夹具（`tests/fixtures/blockcomments/`，全部标 synthetic 来源）

`minimal-2page.pdf`（确定性生成、2 页已知文本）、`parse-a.middle.json`（2 页、非连续 block index 1/2/5、equation+text、bbox `[0,1]`）、`parse-b.middle.json`（同 PDF 另一份解析：块序不同、page 2 缺失、含无 bbox 案例）、`sources.json`（模拟 arXiv v2/v3 两源）、`golden-anchors.json`（统一锚点语义的金标准）。真实 MinerU 4 产物接入留待受控环境（Q1 验收时标注未跑项）。

### 12.6 并行结构与写范围（Lead 集成，本地分支不 push）

| 角色 | 位置 | 分支 | 写范围 |
|---|---|---|---|
| Lead | 主检出 | main（Q0 提交）+ 集成 | 全仓审查、门禁、Q5 |
| q1-originals | worktree `../QuantumAtlas-Q1` | feat/q1-block-originals | internal/registry、internal/mineru、internal/routes/papers_{sources,parses,blocks}*.go、paperassets/objstore、migrations/00007、apidocs |
| q2-comments | worktree `../QuantumAtlas-Q2` | feat/q2-comments | internal/comments/、internal/routes/comments*.go、internal/routes/auth.go（scope 注册）、migrations/00008、apidocs |
| q3-cli | `../qatlas-cli` | feat/q3-cli-comments | src/qatlas/{cli.py,client/,parser/}、tests、docs |
| q4-web | worktree `../QuantumAtlas-Q4` | feat/q4-reader | web/src/routes/$lang.papers.*、web/src/components/reader/、web/src/i18n、web/package.json（仅加 pdfjs-dist）、web/src/lib |

OpenAPI 用 `go tool swag init -g main.go -d ./cmd/qatlasd,./internal/routes -o internal/apidocs --parseInternal --parseDepth 1` 同步。集成顺序 Q1→Q2→Q4；Q3 独立仓并行。

## 13. 存储布局研究备忘：解析产物的 md 与 middle JSON（2026-09-29，后端加固轮）

> **状态：调研与选项，未拍板。** 本节只记录现状与利弊，供用户决策；不替代 §12 的已锁定决议。当前代码（00009 + ingest 接线提交）默认运行在选项 A 的 bundle 布局上，切换成本见各选项。

### 13.1 现状（两条并存的产物路径）

**旧 default_asset 布局（v0.7.0 起，`paper_assets` 行驱动，可变单份）**

| 产物 | 对象 key | 写入方 | 语义 |
|---|---|---|---|
| 源 PDF | `pdf/<yymm>/<stem>.pdf` | upload-pdf / 下载管线 | 每源版本一份（`paper_assets.pdf_path`），基本不再写 |
| MinerU markdown | `markdown/<yymm>/<stem>.md` | upload-mineru（旧格式路径）/ 服务端转换器 | **同 key 覆盖**：重解析直接替换，靠 bucket versioning 兜底 |
| images | `images/<yymm>/<stem>.zip`（合成单 zip） | 同上 | 同 key 覆盖 |
| JSON | `json/<yymm>/<stem>.json` | 已弃用 | v0.7.0 丢弃该 kind；`mineru_json_path` 列仍在但长期为空 |

S3 模式下 `objstore.Router` 按 key 首段路由到 `qatlas-pdf` / `qatlas-md` / `qatlas-images` 三桶，kind 前缀在桶内被剥掉；本地模式单目录保留完整前缀。`papers.default_asset_id` + `paper_assets` 的 CHECK 约束把 md 与 json 绑在同一资产行上。

**新 parse-revision bundle 布局（00007 + 本轮 ingest 接线，不可变）**

| 产物 | 对象 key | 语义 |
|---|---|---|
| middle JSON | `papers/<qa>/parses/<rev>/middle.json` | 解析修订的锚定对象，`parse_revisions.artifact_sha256` 逐字节 pin |
| markdown | `papers/<qa>/parses/<rev>/markdown.md` | bundle 成员（zip 里有才存） |
| images | `papers/<qa>/parses/<rev>/images/<rel>` | bundle 成员，逐文件 |

`papers` kind 目前注册在 `qatlas-pdf` 桶（`initRawStore`）；重解析 = 新 `<rev>` 目录 + `is_current` 翻转，旧对象永不覆盖。旧读路径（`GET /markdown`、figures、images zip）**不会**看到新 bundle——它们只认 default_asset 布局。

### 13.2 选项

**A. 全 bundle 不可变（当前代码默认）**：middle.json、markdown、images 全部进 `papers/<qa>/parses/<rev>/`。

- 利：与 §4.2 锚点身份严格一致——旧评论永远回看当时字节；重解析/多 tier/多引擎天然并存；无覆盖竞态，不需要 versioning 兜底；遗留 default_asset 可冻结为只读遗产。
- 弊：存储放大——每修订一份 markdown+images（图像占大头，重复解析同 PDF 时 md 几乎相同）；旧 `/markdown`、figures 端点不受益于新解析，需要双读或指针迁移；三桶策略之外多出一个 papers 前缀域。
- 迁移成本：零（已是现状）；旧数据的 md/images 留在原位，需要时按需重解析。

**B. 沿用 default_asset 可变单份**：新解析的 markdown/images 仍写 `markdown/<yymm>/<stem>.md` 等覆盖位，仅 middle.json 入 bundle。

- 利：零新布局；旧读路径立即拿到最新解析；存储最省（每论文一份 md+images）。
- 弊：**直接违反 §4.2/§8 Q1 的不可变承诺**——覆盖后旧评论锚定的修订只剩 middle.json 完好，md/images 与锚点脱钩；依赖 bucket versioning 才能恢复历史，而版本化恢复不在 API 合同内（客户端无法寻址旧版本）；`parse_revisions` 行声称不可变、对象可变，两套真相。
- 结论倾向：除非用户明确接受“md/images 不参与锚点、仅 latest-view”，否则不推荐。

**C. 混合：middle.json 永远 bundle；md/images 内容寻址（推荐候选）**：markdown 与 images 按 `sha256` 寻址（如 `papers/<qa>/parses/<rev>/` 内符号性引用 + 内容存 `papers/<qa>/blobs/<sha>`），修订目录只记成员清单；`default_asset` 读取逐步改指 parse_revisions 当前指针。

- 利：保住不可变锚点（middle.json 独占修订身份）；同 PDF 重解析 md 几乎不变时存储零放大（内容寻址自动去重）；旧读路径可通过当前修订指针获得一份“最新视图”；未来勘误版/二次加工也走内容寻址。
- 弊：需要一次小的读路径改造（default_asset → parse_revisions 指针 + blobs 解引用）；成员清单需入库或写入 bundle 索引对象；调试直观性下降（目录里是 sha 名）。
- 迁移成本：中等；可在 A 的现状上演进（旧 bundle 目录本身就是成员清单的天然载体）。

### 13.4 拍板结果（2026-09-29，用户决定）

1. **布局选 A**（全 bundle 不可变），**不采用 C 的内容寻址/哈希去重**——接受重解析时 md/images 的存储重复作为代价。B 维持否决。
2. middle.json 独占锚点身份；md/images/content_list 为修订 bundle 成员（随修订不可变，但不参与锚点合同）。
3. MinerU 设计的两种 JSON 均为设计产物：middle_json=锚点，content_list（structured content）=消费视图——ingest 自本次起将 zip 内 structured_content.json/content_list.json 作为 bundle 成员原样保留（content_list.json 键）。
4. 独立桶、旧 default_asset 冻结时点：随 v0.37.0 转正 + lib 接入确认后再议（当前 papers 前缀仍在 pdf 桶）。
5. 同轮记录的倾向（未见异议）：REST 不做 git 式重构，v0.38 增 `?since=` 增量同步端点；git 只读镜像为 v0.38 可选项（权威恒为 objstore+PG）；CLI `contrib mineru --zip` 放开 ARXIV_ID + `--tier` 透传 + 新格式预检。

### 13.3 待用户拍板

1. 选 A / B / C（或提出 D）。
2. 若保留 A 或 C：`papers` 前缀是否独立成桶（`S3BucketParses`，影响 `initRawStore` 与部署 compose）；还是继续与 pdf 同桶。
3. 旧 default_asset 的冻结时点：何时停止旧格式 upload-mineru 写 `markdown/`（保留只读兼容多久）。
4. md/images 是否参与永久锚点合同（决定 B 是否合法）。
