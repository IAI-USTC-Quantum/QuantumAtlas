# Markdown 与 LaTeX 预览

论文详情页和管理员资产浏览共用 `src/components/markdown-preview.tsx`。
仍只有管理员能使用这些资产预览入口；本功能不改变后端鉴权、下载 API 或存储内容。

## 当前方案：直接使用 react-markdown

`markdown-document.tsx` 直接渲染官方 **react-markdown 10.1.0** 组件，
Markdown 解析、unified 插件管线以及语法树到 React 的转换均由该组件执行。
不是给旧渲染器改名，也没有另行解析 Markdown 或手动调用 JSX 转换器。

```text
鉴权获取原始 Markdown
  ├─ 原文：React <pre> 文本节点
  └─ 按需加载 react-markdown、KaTeX、CSS 和字体
       react-markdown：remark-parse → remark 插件 → remark-rehype → rehype 插件 → React
          本项目插件/选项：GFM、数学分隔符兼容、URL/资源预算、受限 KaTeX 数学 handler
```

此前 `5c55fbe` 的 Worker/JSON 传输、独立树解码器及手动 HAST→JSX 路径已删除。
默认 HTML 转义、常规 Markdown 节点处理和 React 输出使用库的实现；不再维护第二套通用渲染器。
组件按内容 memo，避免无关父组件更新触发解析；**memo、Suspense 和异步 hooks 都不是后台线程**。

react-markdown 是成熟的默认选择，默认在主线程解析不代表其机制不好。
此次迁移以实际浏览器结果评估取舍；通过回归测试不等于证明某个方案普遍更快或更安全。

## 使用方式与兼容性

默认打开 **渲染预览 / Rendered**，可切换 **原文 / Source**。
原文逐字符显示获取到的字符串，不参与 Markdown/公式处理，也不修改下载内容。
空响应与加载状态分开；异常或预算超限由错误边界接住，仍保留原文/下载入口。
更换内容会重置错误边界，不能让上一份失败文档污染下一份。

支持标题、列表、引用、GFM 表格对齐、删除线、任务文本和代码块，以及：

```markdown
行内 $E=mc^2$，或者 \(\alpha+\beta\)。

$$
\lvert\psi\rangle = \frac{\lvert 0\rangle + \lvert 1\rangle}{\sqrt{2}}
$$

\[
\begin{pmatrix} a & b \\ c & d \end{pmatrix}
\]
```

- 保留同行 `$$x$$` 展示公式、中文旁的公式以及 `\(...\)`/`\[...\]`。
  `remark-math-compat.ts` 是 micromark/mdast 语法扩展，不是另一个 Markdown parser；
  不对整篇源码或 DOM 作正则替换。单美元和 `\(...\)` 不跨行，未闭合公式不吞掉余下文档。
- 所有代码块（包括标记为 `math` 的代码块）、行内代码和转义分隔符保持文本。
- KaTeX **不是完整 `.tex` 编译器**；无效/不支持/过长公式回退到带原始分隔符的代码文本。
- 任务项仍显示 `[x]` / `[ ]` 文本，不生成交互 input；脚注保留文字，不新增 ID/反链。
- react-markdown 将表格对齐转换为内联 `textAlign`；浏览器检查实际 computed style。

## 安全与资源边界

- **原始 HTML 可见但不执行**：使用 react-markdown 默认转义，不开启 `skipHtml`、
  `rehype-raw`、MDX 或任意外部组件/插件。生产代码没有 `dangerouslySetInnerHTML`。
- **图片不自动请求**：`components.img` 只输出 alt 文本占位，直接图片、引用图片一样处理；
  不生成 `<img>` 或 React 图片预加载，也不把相对图片地址请求到应用/API。
- **链接按业务策略收紧**：只允许 HTTP/HTTPS、mailto、本页 fragment。
  remark 插件在 URI 规范化前检查直接链接和引用定义，防止控制字符被编码后绕过检查；
  再通过 `urlTransform` 与只挑选必要属性的链接组件过滤。拒绝相对/API 路径、协议相对地址、
  控制字符与凭据 URL。外链固定 `noopener noreferrer nofollow`、`no-referrer` 和 `_blank`。
- **受限数学排版**：每个公式独立 `macros: {}`，使用 `trust: false`、`strict: error`、
  `throwOnError: true`、`maxExpand: 1000`、`maxSize: 20`；不允许跨公式/跨文档宏污染或
  信任命令加载资源。`htmlAndMathml` 保留辅助技术使用的 MathML。
- **只有 KaTeX 的输出转为 HAST**：小型数学 handler 使用公共 `renderToString` 与
  `hast-util-from-html`；作者 HTML、异常消息不会送进该 HTML parser。
  KaTeX 输出安全依赖该维护中的库及受限选项，不再用自制 SVG/MathML 属性解码器替代它。
- **DOMPurify 不再需要**：没有 HTML 注入显示通道，也没有接收外部 AST 的 Worker 边界。
  如未来引入 raw HTML、MDX、外部插件或用户 AST，须重新评审信任边界，不能直接套用本结论。
- **账号与响应隔离不变**：Query key 按账号/论文区分，不包含 token；消耗 AbortSignal，
  关闭预览后清除 Markdown 缓存，账号切换关闭预览并重查权限，旧响应不能覆盖新文档。

| 预览预算 | 行为 |
| --- | --- |
| 输入 | 最多 200,000 UTF-16 code units；超过不调用 react-markdown，只提供原文/下载 |
| 单公式 | 最多 10,000 UTF-16 code units；超过回退该公式原文 |
| 公式数量 | 最多 200 个 math/inlineMath 节点；在 KaTeX 前检查 |
| TeX 开始标记预检 | 最多 200 个未被双反斜杠转义的 `\(`/`\[`；在 Markdown 解析前线性计数 |
| Markdown / 最终 HAST | 每棵树最多 20,000 节点，root 深度 0、最大深度 64 |
| 公式累计输出 | HTML 最多 2,000,000 code units；转换中累计检查数学节点数量 |
| 最终树内容 | 文本、raw 值与字符串/数组属性累计最多 2,000,000 code units |

预算由迭代遍历检查：先检查 mdast，再在公式转换过程中累计，最后在库递归生成 React 前检查 HAST。
每次处理都重置计数（包括 StrictMode 和复用同一 options 的重复渲染），而非在模块全局累计。
公式数量上限按密集样本的实测收紧，避免先花数秒排版再被节点预算拒绝。TeX 开始标记预检
用于廉价拒绝大量未闭合兼容分隔符；它不是语法解析，保守地连代码中的这类标记也计数，
超限时仍可看完整原文，不会改写代码或把它当公式渲染。
这些是应用保护阈值，不是 react-markdown 的固有限制，也不是性能 SLA。

**不再承诺 5 秒强行终止或解析中立即取消。** Markdown/KaTeX 在主线程同步执行，
定时器、切换标签和关闭按钮不能抢占正在执行的同步代码。加载懒模块期间可用原文，
解析返回或失败后也可切换，但长任务期间仍可能短暂失去响应。输入/结构预算不是硬时间或内存沙箱。
高密度公式可能在达到输入长度上限前被拒绝；对超限文档使用原文/下载，不截断内容冒充完整渲染。

## 长文档性能实测

对照旧 Worker 构建（源码 `5c55fbe`）与当前直接组件；保持 KaTeX 0.18.7、语法、样本
和浏览器相同。每版 **27 项语义测试 / 54 次打开**：42 次完整渲染、12 次明确拒绝。
不把拒绝计作成功，也没有省略文档末尾或公式来换速度。

环境：Linux x64，报告 CPU 为 Xeon Gold 5118 / 48 logical CPUs，Node 24.20.0，
Playwright 1.62.0 / Chromium 151.0.7922.34，1280×900；每个案例新 context，重复 3 次。
首次打开含懒模块/文档获取/字体；再次打开仅模块和字体已热，Markdown **仍重新获取**。
计时由浏览器实际点击开始，到 DOM 就绪、字体就绪及双 rAF 的“绘制机会”为止，
不等于所有屏外像素完成栅格化。DOM/公式/终点标记和原文保真在计时窗外检查。

下表单位 **ms，中位数 / 最大值，n=3**。这是应用组合的合成技术文档测量，不是裸库排名。

| 样本与 CPU / 打开方式 | Worker 完成 | react-markdown 完成 | Worker 最长主线程任务 | react-markdown 最长主线程任务 |
| --- | ---: | ---: | ---: | ---: |
| 混合 10k，6 公式，本机首次 | 819 / 839 | 583 / 714 | 117 / 172 | 166 / 186 |
| 混合 50k，36 公式，本机首次 | 1172 / 1198 | 1224 / 1252 | 209 / 230 | 396 / 411 |
| 混合 100k，75 公式，本机首次 | 1655 / 1753 | 1454 / 1488 | 442 / 470 | 468 / 517 |
| 混合 200k，153 公式，本机首次 | 2135 / 2486 | 3015 / 3066 | 618 / 786 | 1059 / 1081 |
| 混合 200k，本机再次打开 | 1421 / 1638 | 1324 / 1583 | 327 / 422 | 1164 / 1452 |
| 纯文本 200k，本机首次 | 1103 / 1195 | 966 / 1159 | 285 / 290 | 301 / 362 |
| 混合 50k，4×降速首次 | 1804 / 1847 | 3899 / 3941 | 514 / 516 | 1696 / 1715 |
| 混合 200k，4×降速首次 | 4203 / 4371 | 5769 / 9410 | 1819 / 1823 | 2776 / 4615 |
| 混合 200k，4×降速再次打开 | 3100 / 3306 | 4379 / 5223 | 1448 / 1506 | 3989 / 4671 |

成功渲染时，混合 200k 有 **10,203 个 DOM 元素和 153 个公式**，纯文本 200k 仅有
168 个元素。长度都为 200k，负载并不相同。4×混合 200k 再次打开的 heartbeat gap
中位数从约 2195 ms 增至 4210 ms，最大约 4927 ms，不能称为交互流畅。

**结论：小文档、本机部分场景和部分热模块重开更快，但长富文本及慢 CPU 会明显阻塞；
没有全面性能提升，也不以这些结果宣称官方库机制不好或定制方案普遍更好。** Worker 基线
也有主线程 DOM/布局卡顿；直接组件省掉传输/二次遍历，却将解析/排版放到主线程。
本次按需求选择官方组件的维护方式，同时保留预算并公开这项交互代价。若需要大型文档
持续流畅交互，后续应评估明确的渲染 opt-in、分段/虚拟化或服务端预处理，而非加一个无效定时器。

密集 50k 样本含 **399 个公式**，两版都拒绝渲染，原文均完整可用。初版直接组件会先展开
再撞节点上限，4×再次打开时最长任务中位数/最大值为 2578 / 4070 ms；将数量预检收紧为
200 后降为 **1656 / 1676 ms**。这只是拒绝路径的改善，仍存在同步 Markdown 解析成本，
不是“成功支持了 399 个公式”或“拒绝已无卡顿”。正常对照样本最多 153 个公式，不受数量收紧影响。

测量时没有并行运行本会话的构建/其他测试，但不是独占硬件实验；只有三次重复，波动明显，
不能推断 p95/p99 或统计显著性。CDP 4×是模拟，不能等同于某款手机或保证 Worker 线程也按同倍率降速。
fixture 不覆盖真实论文分布、网络延迟或生产后端，不把实验毫秒数作为通用 SLA。
完整方法与命令见 `tests/performance/README.md`。

本地原始记录：`build/react-markdown-results/worker-baseline/`、`react-markdown-final/`；
最终比较为 `comparison-worker-baseline-vs-react-markdown-final.md` / `.json`。
`react-markdown/` 是收紧密度预算前的探索记录，不替代最终结果；诊断中止的运行不计入基线。
样本含精确 UTF-16 长度、UTF-8 bytes、SHA256；报告含构建资源指纹，比较器拒绝不一致的输入/环境。

最终懒加载渲染 JS 约 **600 kB**（minified、未压缩），旧 Worker 约 621 kB 加 UI 约 37 kB；
KaTeX 字体版本不变。包体变小不等于主线程更流畅；Vite 的 >500 kB 提示保留，未调高阈值。

## 依赖与其他文档站

直接组件固定 `react-markdown` **10.1.0**，数学排版固定 KaTeX **0.18.7**，GFM 为
`remark-gfm` **4.0.1**；完整版本以 package.json/lock 为准。KaTeX JS/CSS/字体使用同一版本，
均本地打包、按需加载；小字体也保留为独立同源文件，兼容 `font-src 'self'`。

迁移和性能对照没有同时更换数学引擎。`rehype-katex` 7.0.1 及 `remark-math` 的传递图
仍带 KaTeX `^0.16.0`；本次不引入第二版本或强行 overrides，继续保留受测试覆盖的
数学分隔符兼容层和小型公共 API adapter。其维护成本仍存在，不声称所有代码都是官方插件。

MkDocs 独立使用 Python Markdown/arithmatex 和 CDN KaTeX auto-render；Sphinx 使用
reStructuredText 数学节点，HTML 默认 MathJax。即使 Sphinx 静态页随 Web/Go 一起分发，
也不经过 React 预览器。本次不迁移这两套引擎；npm 审计不覆盖其 CDN 或整个部署。

## 验证与复现

在 `web/`，使用 `.node-version` 指定的 Node：

```bash
npm ci
npm audit
npm run lint
npm test
npm run build
npx --no-install playwright test
# 单独运行计时，不和构建/其他测试并行：
MARKDOWN_BENCH_LABEL=react-markdown npm run test:performance
```

首次运行浏览器前可用 `npm exec -- playwright install chromium` 安装测试浏览器。
性能测试固定浏览器版本并要求新基线后才能更换，具体 pin/方法见其 README。

- 单元回归直接渲染实际 `MarkdownDocument`：公式/代码/HTML/图片/URL、宏隔离、预算、
  重复 options/StrictMode，以及确实使用官方组件且无第二套渲染器的依赖契约。
- Chromium 使用真实构建、两个现有入口、独立 synthetic OAuth/API fixture；覆盖中英文/主题/窄屏、
  MathML/SVG 命名空间、字体、表格对齐、原文、复杂度失败恢复、账号/旧响应隔离。
  Worker 消息/终止测试已改为真实输入预算与不依赖 Worker 的测试，不用已删除机制的测试数量包装结果。
- 浏览器只访问 loopback 的无代理静态 preview；外部/未知请求、CSP 违规、资源失败和未捕获异常均失败。
  **这不代表生产 OAuth、数据库、S3 或后端鉴权集成验证通过。**
- 普通 CI 运行类型、lint、单元和浏览器回归；计时基准是显式 opt-in，不把受机器影响的毫秒数设为 CI 门禁。
  `.github/scripts/build-docs.sh` 会清除 `web/dist`，不能在浏览器/性能测试期间运行。

本地新证据位于 `build/react-markdown-results/`，截图/普通浏览器报告位于 `web/test-results/`。
此前 `build/markdown-ast-results/` 记录的是 Worker 方案，不是当前实现的验收报告。
构建产物/原始计时记录不提交；提交、推送和部署是独立操作，本地通过不表示线上已更新。

参考：[react-markdown 架构与安全](https://github.com/remarkjs/react-markdown)、
[KaTeX 安全](https://katex.org/docs/security.html)、[KaTeX 选项](https://katex.org/docs/options.html)。
