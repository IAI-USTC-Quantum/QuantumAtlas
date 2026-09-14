# Markdown 与 LaTeX 预览

论文详情页和管理员资产浏览共用 `src/components/markdown-preview.tsx`。
仍然只有管理员能使用现有资产预览入口；本功能不改变后端鉴权、资产下载 API 或存储内容。

## 使用方式

默认打开 **渲染预览 / Rendered**，可随时切换 **原文 / Source**。
原文按 React 文本节点显示获取到的字符串，不经 Markdown/公式解析，也不修改下载内容。
空文档与“正在加载”是不同状态。解析失败不会使整个页面崩溃，仍可查看原文或下载。

支持标题、列表、引用、GFM 表格、删除线、任务文本、代码块以及：

```markdown
行内公式：$E=mc^2$，也支持 \(\alpha+\beta\)。

$$
\lvert\psi\rangle = \frac{\lvert 0\rangle + \lvert 1\rangle}{\sqrt{2}}
$$

\[
\begin{pmatrix} a & b \\ c & d \end{pmatrix}
\]
```

`$$x$$` 同行形式、中文旁的 `\[x\]` 同样使用展示公式排版。
代码块（包括标记为 `math` 的代码块）、行内代码和转义的 `\$` 保持文本，
不扫描整篇源码或整个页面 DOM 做正则替换。
KaTeX 是数学排版器，**不是完整 LaTeX 文档编译器**；不支持的命令或无效公式回退为
带原始分隔符的公式源码，而不是显示包含未经转义源码的异常 HTML。

## 渲染与信任边界

```text
鉴权获取 Markdown 文本（Query key 按账号/论文区分，使用 AbortSignal）
  ├─ 原文：React <pre> 文本节点
  └─ 渲染：一次性 Web Worker
       remark-parse + remark-gfm + micromark 数学兼容扩展 → mdast
       → remark-rehype 受控 handlers → HAST
         仅数学节点：受限 KaTeX 公共 API → parse5 转为 HAST
       → 严格树验证/规范化 → 有大小上限的 JSON 字符串
       → 主线程独立校验消息、节点/深度/标签/属性/URL/样式
       → hast-util-to-jsx-runtime → React 元素
```

- **Markdown 只解析一次**：解析和数学排版留在可终止 Worker 内，主线程不再次解析
  Markdown，不存在解析器失败后在主线程重试的后门。
- **没有 HTML 显示通道**：生产代码不使用 `dangerouslySetInnerHTML`、`innerHTML`、
  `rehype-raw`、MDX 或自定义组件。原始 HTML 的 mdast handler 输出文本节点，
  因而标签、注释可见但不执行；不是 `skipHtml` 式静默删除。
- **Worker 不可信**：`markdown-tree.ts` 先检查字符串大小，再 JSON 解码，重建一个
  最小 HAST 子集。任何未知节点、活动标签、未知属性或错误类型使预览失败关闭；
  位置/扩展元数据不复制。不把 Worker 对象、style 对象或未知 props 展开给 React。
  Worker 自身也执行同一验证，主线程不会因此省略独立检查。
- **明确的 HTML / MathML / SVG 边界**：仅允许阅读结构、数学布局 span、必要的
  presentation MathML 和 SVG path/line；不允许 MathML/SVG 内嵌 HTML、
  `annotation-xml`、`foreignObject`、媒体、表单、事件、`is`、DOM clobbering
  的 `id`/`name`、React 特殊属性、任意 data/ARIA 属性。
  保留 `htmlAndMathml`、`aria-hidden` 和 TeX annotation；浏览器测试验证真实 DOM 命名空间。
- **样式不是任意 CSS**：仅保留已知数学布局类、代码语言类、图片/错误占位类；
  内联样式限于必要数值/颜色布局属性与无函数的值语法。禁止 `url()`、CSS 转义、
  变量、表达式、`!important` 与任意属性。SVG/MathML 的颜色也不允许 URL paint。
- **公式不受信任**：`trust: false`、`strict: error`；不允许 `\includegraphics`、
  `\href`、`\htmlStyle` 等信任命令加载资源或修改 HTML。
  每个公式使用新的宏对象；不允许 `\gdef` 跨公式或跨文档影响后续内容。
  只调用 KaTeX 的公开 `renderToString`，不依赖私有 DOM 树 API。
- **无自动内容网络请求**：Markdown 图片（含引用式图片）显示替代文本占位，
  不创建 `<img>`；不把论文相对路径误当作应用路径，也不请求外部图片或内部 API。
  未来图片支持须通过受控资产映射/鉴权通道，不能直接放开任意 `src`。
- **链接白名单**：只允许 HTTP/HTTPS、mailto 和本页 fragment；拒绝危险协议、
  协议相对 URL、应用/API 相对路径、控制字符和凭据 URL。检查 mdast 已解码、
  尚未 URI 规范化的目标（包括引用链接），主线程再次检查。
  外部链接固定 `noopener noreferrer nofollow`、`referrerpolicy=no-referrer`、
  `target=_blank`；不预取、不自动跳转。非法链接保留文本但不创建 anchor。
- **本地完整资源**：KaTeX JS 在 Worker bundle，CSS 和字体随渲染组件懒加载；
  全部来自同一个锁定 npm 版本，随 Vite/Go UI 分发，无公式 CDN 请求。
  较小字体也输出为独立同源文件，不用 `data:` 内联，兼容 `font-src 'self'`。

### 资源与生命周期限制

| 限制 | 当前值 / 行为 |
| --- | --- |
| 整篇渲染输入 | 200,000 个 UTF-16 code units；超出只提供原文/下载 |
| 单公式 | 10,000 个 UTF-16 code units；超出回退该公式原文 |
| KaTeX 宏展开 | `maxExpand: 1000` |
| KaTeX 显式尺寸 | `maxSize: 20` em |
| Worker 时间预算 | 5 秒，包括模块启动；超时终止并保留原文入口 |
| 公式 HTML 中间结果累计 | 2,000,000 个 UTF-16 code units；超过不继续 HTML→树转换 |
| 完整树 JSON 消息 | 2,000,000 个 UTF-16 code units，含节点/属性开销；解码前检查 |
| 树节点 / 深度 | 20,000 个节点（含 root/text）；root 深度 0，最大 64 |
| 每节点属性 / class token | 最多 40 个属性 / 16 个 class token；无嵌套属性对象 |

JSON/节点/深度限制是 AST 架构新增的主线程资源边界，不按源码字符数假定展开结果很小。
主线程只执行有界 JSON 解码、验证、JSX 分配与 React DOM 更新；这部分同步工作没有可抢占
超时。Worker 超时也不是整个浏览器的内存沙箱。复杂大文档可能在达到源码阈值前回退；
可下载原文，未来可增加分段渲染，而不是解除预算。

切换到原文、关闭对话框或更换内容会终止旧 Worker；旧异步结果不能覆盖新文档。
管理员身份检查和 Markdown 查询按账号区分，不将 token 放进 Query key；账号切换时
关闭资产预览并清空组件状态，重新检查权限。Markdown 缓存不在最后一个观察者卸载后保留。
这些是预览限制，不是下载/资产保存限制。

## 依赖与架构选择

运行时固定：unified 11.0.5、remark-parse 11.0.0、remark-gfm 4.0.1、remark-rehype 11.1.2、
hast-util-from-html 2.0.3、hast-util-to-jsx-runtime 2.3.6、decode-named-character-reference 1.3.0、
KaTeX 0.18.7。标准 math/inlineMath 节点类型来自仅开发期的 mdast-util-math 3.0.0。
确切传递版本以 `package-lock.json` 为准，CI 审计完整依赖图。

代价不是零：本次构建的独立 Worker 约 621 kB（minified、未压缩），此前 marked Worker
约 310 kB；懒加载 React 渲染 JS 约 37 kB（此前约 34 kB），公式 CSS/字体不变。
新增的语法树工具链、DOM-free HTML parser 与实体表换来明确的结构边界，但并非包体优化；
它们仍只在打开预览时加载。另需维护受测试约束的数学语法兼容层和窄树解码策略。

- **为什么不再用 marked**：阅读界面可以直接从受控语法树构造 React 元素，不必将整篇
  Markdown 序列化为 HTML 再交给浏览器解释；原文、安全策略与结构化回归更容易分层。
- **为什么不直接套 react-markdown 组件**：它默认在调用线程解析字符串。这里使用相同
  unified/HAST→JSX 生态，但把解析搬到 Worker，避免丢失超时/取消或主线程重复解析。
  `hast-util-to-jsx-runtime` 也是 `rehype-react` 的底层转换器，已有 HAST 无需额外包装。
- **为何是小型 KaTeX adapter**：本次重新核对 npm registry，`rehype-katex` 7.0.1
  仍声明 KaTeX `^0.16.0`，当前 KaTeX 为 0.18.7。未用 `overrides` 强行跨版本，
  未保留两个公式/CSS版本。adapter 只在真正 math 节点调用受限公共 API，再用
  DOM-free 的 `hast-util-from-html`（parse5）转换公式输出；不把 Node-only DOM/jsdom
  或依赖浏览器 document 的 HTML parser 带进生产 Worker。
- **兼容语法的维护代价**：remark-math 默认不支持 `\(...\)`/`\[...\]`，
  同行 `$$x$$` 默认也是 inline，且单美元允许跨行。`remark-math-compat.ts` 用
  micromark 的 token/state 与 mdast 扩展覆盖这些差异；代码、转义、容器和 GFM 仍交给
  维护中的上游 parser。只支持单/双美元，不新增任意长度美元 fence 或未闭合公式。
  由于兼容层已替换 stock 数学语法，**不再注册或依赖 remark-math**：其传递依赖
  micromark-extension-math 自带 KaTeX `^0.16.0`，保留不用的解析器会引入第二版本。
  升级 micromark/标准 math 节点类型时须重跑语法测试；本扩展只用于阅读，不提供源码回写。
- **Worker 的条件导出**：decode-named-character-reference 的 `browser` export 在加载时
  使用 `document`，浏览器 Worker 没有这个对象。Vite 配置仅对该包用 Node resolver 选择
  包公开的 DOM-free default export（与它的 worker export 相同），开发/构建均适用；
  不全局修改解析条件、不 patch node_modules、不引入 DOM polyfill。
  Node 单测无法发现 browser 条件分支错误，必须保留真实构建后 Chromium 回归。
- **DOMPurify 已移除**：所有 HTML 显示调用者均已删除，不再需要 DOM 消毒库。
  不以“AST 天然安全”为理由删除防线：用严格树解码器取代原 HTML 消毒边界，
  接收器独立验证 URL、样式、命名空间、类型与资源预算。
  没有采用通用 `rehype-sanitize` 的宽泛后处理/数学白名单顺序；未知元素/属性直接拒绝，
  仅规范化明确允许的布局和链接属性，随后交给库转换为 JSX。新增插件不得绕过此末端边界。

GFM 任务保持 `[x]` / `[ ]` 文本，无可交互 input；脚注扩展保持原样文本，不新增
ID、反链或未本地化的脚注区。正常渲染不会人为添加旧 HTML serializer 的尾部换行；
原文模式仍逐字符保真。

这不是回滚 `77cadfd`：当时清除无调用者依赖是合理的；此次继续保留依赖审计成果，
按功能需求迁移现有预览。旧 marked 类型适配和路径映射随库一起删除。
**其他文档站独立**：MkDocs 的 KaTeX CDN 和 Sphinx 构建路径不属于此 npm 渲染器；
本次不迁移它们。`npm audit` 为零不覆盖这些 CDN 或整个部署的安全状态。

## 持续验证

在 `web/`，使用 `.node-version` 指定的 Node：

```bash
npm ci
npm audit
npm run lint
npm test
npm run build
npm exec -- playwright install chromium
npm run test:browser
```

- `tests/markdown.test.ts`：公式、所有分隔符、代码、GFM、HTML/图片文本、
  URL/实体/控制字符、信任命令、宏隔离、边界大小与公式结构。
- `tests/math-syntax.test.ts`：语法 token 与位置、中文、CR/LF/CRLF、容器/代码/转义、
  不闭合/相邻公式；升级插件不能用新默认值静默替换原契约。
- `tests/markdown-dependencies.test.ts`：单版本 KaTeX/CSS、旧依赖移除、无 HTML sink、
  无主线程 parser fallback 和 DOM-free 构建约束。
- `tests/markdown-tree.test.ts`：不经 Markdown renderer，直接注入伪造 Worker 树；
  覆盖活动标签、React 特殊 props、原始 HTML/MDX、对象属性、命名空间、CSS/URL、
  节点数/深度/消息大小、重复验证和失败关闭。
- `tests/browser/`：真正 Chromium 渲染构建后的 Worker/React 资源；两处入口、中英文、
  明暗主题、窄屏、键盘切换、原文保真、MathML/SVG/本地字体、非管理员、空文档、
  失败/超时、Worker 终止、非法消息、账号切换与旧响应竞争。
- 浏览器测试启动 loopback-only 的静态 Vite preview，关闭配置/.env 读取和代理，
  全部 OAuth/业务 API 使用明确 synthetic fixture；未知 API、外部请求、CSP 违规均失败。
  **这不代表生产 OAuth、数据库、S3 或后端鉴权集成测试已通过。**
- 报告和截图位于被忽略的 `web/test-results/`，不提交构建资源。
  CI 第一次构建后执行浏览器回归，再做第二次双文档站/Web 构建和完整 UI 一致性比较。
  `.github/scripts/build-docs.sh` 会清除 `web/dist`，不可和浏览器测试并行执行。

本次 AST 迁移的本地验证记录：`build/markdown-ast-results/verification.md`。
`build/math-test-results/verification.md` 仅记录此前 marked 实现，不用于证明本次迁移通过。
本地验证不代表线上已更新；提交、推送和部署须分别获得授权。

## 官方依据

- [react-markdown 的架构与安全边界](https://github.com/remarkjs/react-markdown)
- [remark-math / rehype-katex](https://github.com/remarkjs/remark-math)
- [HAST 到 JSX runtime](https://github.com/syntax-tree/hast-util-to-jsx-runtime)
- [DOM-free HTML 到 HAST](https://github.com/syntax-tree/hast-util-from-html)
- [树级消毒与插件边界](https://github.com/rehypejs/rehype-sanitize)
- [KaTeX 安全说明](https://katex.org/docs/security.html)
- [KaTeX 参数与宏状态](https://katex.org/docs/options.html)
