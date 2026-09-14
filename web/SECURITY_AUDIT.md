# 前端依赖安全修复记录（2026-09-14）

> **历史范围**：下文记录 `77cadfd` 清理闲置依赖时的实现与验证结果，不是后续功能的现状。
> 后续按明确需求重新接入了有调用者的 Markdown / LaTeX 渲染，再迁移为 unified/remark
> 与单版本 KaTeX 的 Worker → HAST → React 方案；marked 与 DOMPurify 已无调用者并移除。
> 当前的树验证、Worker 隔离、原文模式与持续测试见 [Markdown 与公式预览](MARKDOWN_PREVIEW.md)。
> 下文“纯文本预览”“无源码调用”的描述仅适用于该历史提交。

## 范围与结果

基线：`d98fc73` 的 `web/package-lock.json`，Node **24.20.0**、npm **11.19.0**，
官方 `https://registry.npmjs.org/`。这是 npm 依赖审计，不是 Go、容器、CDN 或部署环境的全栈安全认证。

| 审计 | 修复前 | 修复后（重新 `npm ci`） |
| --- | --- | --- |
| `npm audit --json` | **15**：2 critical / 9 high / 2 moderate / 2 low | **0**，退出码 0 |
| `npm audit --omit=dev` | **1 moderate**（DOMPurify） | **0**，退出码 0 |

计数是 npm 的受影响包汇总，包含传递影响，不是 15 条可直接攻击生产的独立入口。
初次默认 npm 缓存不可写时，修复元数据未正确解析、汇总曾为 42；更换可写的本地缓存后
重跑得到上表的 15 项，不能把缓存异常产生的 `fixAvailable: false` 当作上游无修复。
后续所有安装、审计均使用可写缓存；没有关闭 audit、过滤 severity 或添加漏洞豁免。

**未使用 `npm audit fix`（包括 `--force`）、`--legacy-peer-deps` 或 `overrides`。**
不修改 Node、TypeScript、React 主版本、服务端 `VERSION`、生产配置或历史 Release。

## 依赖链、可利用条件与处理

| 包 / 原版本 | 类型与实际入口 | 处理后的锁版本 |
| --- | --- | --- |
| `@hey-api/openapi-ts` 0.64.15 | dev；唯一入口是未配置的 `gen:api`，未接入构建/CI；客户端类型由 `src/lib/api.ts` 手工维护 | 删除无效命令与依赖 |
| `handlebars` 4.7.8（critical） | 仅由上述生成器精确引入；模板/AST/partial 注入需实际调用编译器并接受攻击者输入 | 随生成器删除 |
| `c12` 2.0.1 → `giget` 1.2.5 → `tar` 6.2.1（critical） | 同一 dev 配置/远端模板加载链；恶意归档解包可造成路径逃逸、覆盖或 DoS；Go UI ZIP 打包不调用 node-tar | 整链删除 |
| `dompurify` 3.4.2 | manifest 中的运行时依赖，但 `src` 无 import/sanitize 调用；报告涉及 IN_PLACE、攻击者 DOM 对象、hooks/custom elements/Trusted Types 等配置 | 删除未用依赖，而非删除正在使用的消毒措施 |
| `@babel/core` 7.29.0 | Router/React 构建插件；攻击者 JS 的 sourceMappingURL 可读构建机文件，仍需披露出口 | 7.29.7 |
| `browserslist` 4.28.2 / `baseline-browser-mapping` 2.10.29 | Babel 目标解析；不可信 query/stats、重复高基数调用或非法参数可能导致崩溃/OOM | 4.28.9 / 2.11.23 |
| `brace-expansion` 1.1.14、5.0.6 | ESLint / typescript-eslint 的 glob 链；攻击者提供的模式可造成 CPU/内存 DoS | **分别** 1.1.18、5.0.9；未强行统一主版本 |
| `js-yaml` 4.1.1 | ESLint / 原生成器解析 YAML；恶意 merge/alias/omap 可造成 CPU DoS | 4.3.2 |
| `vite` 7.3.3 | Windows 可达开发服务器的路径/UNC 处理可导致文件或凭据披露；不是 Go 静态服务器执行路径 | 7.3.6，manifest 下限同步至 `^7.3.6` |
| `esbuild` 0.27.7 | Vite 构建依赖；该公告针对 Windows 的 esbuild 独立 serve，不等同于 Vite build | 0.28.2；Vite 7.3.6 明确允许 `^0.27.0 || ^0.28.0` |
| `postcss` 8.5.14 / `nanoid` 3.3.12 | Vite CSS 工具链；攻击者 CSS/source map 的文件读取、异常 ID size 导致死循环 | 8.5.28 / 3.3.19 |

主要公告：
[Handlebars AST 注入](https://github.com/advisories/GHSA-2w6w-674q-4c4q)、
[node-tar DoS](https://github.com/advisories/GHSA-23hp-3jrh-7fpw)、
[node-tar 路径逃逸](https://github.com/advisories/GHSA-34x7-hfp2-rc4v)、
[OpenAPI 生成器原型链问题](https://github.com/advisories/GHSA-hhx9-57xq-r5rw)、
[DOMPurify IN_PLACE](https://github.com/advisories/GHSA-55q2-fjhq-7xh7)、
[Babel source map](https://github.com/advisories/GHSA-4x5r-pxfx-6jf8)、
[Browserslist](https://github.com/advisories/GHSA-c83g-rgw3-j3cx)、
[brace-expansion](https://github.com/advisories/GHSA-rgw5-rvv9-x895)、
[js-yaml](https://github.com/advisories/GHSA-2883-xcg3-v3hh)、
[Vite](https://github.com/advisories/GHSA-fx2h-pf6j-xcff)、
[esbuild](https://github.com/advisories/GHSA-g7r4-m6w7-qqqr)、
[PostCSS](https://github.com/advisories/GHSA-fxqj-rqcc-2cmp)、
[nanoid](https://github.com/advisories/GHSA-2v37-7h3g-55p8)。完整公告以对应时点的 npm 审计为准。

构建工具不进入浏览器 bundle，但不可信 PR、模板、依赖源文件仍可能攻击开发机/CI，
所以没有因 `dev: true` 而忽略它们。带凭据的 Vite 代理不得对公网开放或默认指向生产；
`allowedHosts`、fake auth 都不提供后端权限隔离，见 [开发边界](README.md#代理与认证边界)。

### 移除失效功能的依据

- `marked`、`marked-katex-extension`、DOMPurify 无源码调用；KaTeX 仅剩 `main.tsx` 的 CSS 导入。
- 旧 wiki 已独立维护；删除该 CSS 导入及无使用者的 `.markdown` 样式，连同四个 npm 包。
- 论文详情与管理员资产页面的 Markdown 预览继续使用 React `<pre>{text}</pre>`，
  不解析 HTML、公式或 Markdown；**没有把未经消毒的 HTML 注入 DOM**。
- 独立 MkDocs 的公式资源不属于 npm 图，本次未改动。未来若恢复富文本渲染，必须重新选择
  维护中的解析/消毒库并加 XSS 测试，不能直接换成 `dangerouslySetInnerHTML`。

### 兼容性与锁文件

先升级使用中的上游包，再刷新仍在上游允许范围内的传递版本：

```bash
# web/；受限环境可逐条追加 --cache <可写的本地缓存目录>
npm uninstall dompurify marked marked-katex-extension katex @hey-api/openapi-ts
npm update vite @vitejs/plugin-react @tanstack/router-plugin @tanstack/router-cli eslint @eslint/js typescript-eslint
npm update brace-expansion esbuild nanoid postcss
# 同步 Vite manifest 下限后
npm install --package-lock-only
npm ci
npm ls --all
npm audit --json
npm audit --omit=dev --json
```

以上是本次操作记录，不是要求未来执行无版本审查的批量更新。最终复现使用提交的 lock 和 `npm ci`。

直接依赖保持各自主版本：Router runtime 1.169.2 → 1.170.36、CLI 1.166.43 → 1.167.36、
plugin 1.167.35 → 1.168.38；ESLint / `@eslint/js` 9.39.4 → 9.39.5，
typescript-eslint 8.59.2 → 8.70.0。Router 运行时同时按上游声明更新 router-core
1.169.2 → 1.171.30、react-store/store 0.9.3 → 0.11.1；0.x minor 仍有兼容风险，
不能只靠类型检查，需要浏览器验证路由与状态更新。
Router 工具上游同时迁移了内部 Zod 3 → 4、
Chokidar 3 → 5 / readdirp 3 → 5；这些不是应用直接 API，已通过 Node 24 的生成、类型检查和构建验证。
esbuild 的 0.x minor 变化由新版 Vite 明确支持，并经过真实打包验证。

生成器重排了 `src/routeTree.gen.ts`：22 个 route 的 id/path/parent 映射与 import 集合不变。
按仓库约定提交生成的路由**源码**，不手工还原排序或忽略构建漂移。

## 回归验证

所有 Go/构建进程用 `env -i` 及隔离 HOME/XDG；无真实 PostgreSQL/S3/OAuth/检索目标或生产凭据。
环境模板和完整步骤见 [开发入门](../docsite/dev/development.rst)。

- `npm ci`、`npm ls --all`：通过，没有 invalid/missing peer。
- `npm exec -- tsc -b --force`：通过。
- `npm run lint`：退出 0；升级前后同样是 **0 error / 4 warning**，均为现有 Fast Refresh 导出规则。
- Vite 实际开发服务器启动、主入口/论文详情 TSX 转换：通过；无代理目标或凭据，非允许 Host 返回 403，测试后关闭服务器。
- 两次从零构建 Sphinx 公共站 + 开发站（`-W --keep-going`），各接一次 `npm run build`：通过。
- `artifacts.py compare`：两次完整 UI 树 **175 条目**（包含隐藏文件/目录）路径、字节一致。
- `uibundle` 生成源码 VERSION 0.34.0 的本地 ZIP；`restore-ui` 后与 `web/dist` 比较：175 条目一致。
- `go test ./internal/... ./cmd/... ./web ./tests/...`、同范围 `go vet`：通过。
- `go test -tags=integration ./internal/... ./cmd/... ./web ./tests/... -run '^$'`：仅编译通过，未执行真实集成测试。
- `go test -tags=e2e ./tests/e2e -run '^TestSmokeFixture' -count=1`：通过；未跑真实生产 smoke。
- `go test -tags=embedui ./web ./cmd/qatlasd/...`：通过，包含嵌入 FS → Pack → 校验/解包后的逐文件一致性。
- `go build -tags=embedui`：通过，使用上述新资源。
- CI 标准库 fixture：21 项通过。CI 新增全依赖 `npm audit` 和 `npm run lint`，对应源契约断言随之更新。
- Playwright 1.62.0 / Chromium 152.0.7977.8：**66 项断言、33 个页面/交互场景通过**，65 个真实 JS/CSS 资源均成功加载。
  - 真实隔离后端：health / server-info / authMethods 返回 200，10 项匿名受保护 API 请求返回 401，
    两个 devdoc 入口返回 403；真实匿名浏览器跳转登录且显示未配置 OAuth 的状态。
  - 业务 fixture：中文/英文首页、论文列表/详情、搜索及提交、账号页、管理员概览/用户/插件/
    管线/工作节点/数据表/文档入口通过；会话 refresh 与业务响应由显式 route mock 提供，
    不是生产 OAuth/DB/S3/搜索集成。非管理员的预览与管理员页面门控分别验证。
  - 论文详情和管理员资产两处 Markdown 预览均测中英文：包含 img/onerror、script、svg/onload、
    javascript 链接的原文 SHA256 保持一致，未生成相应 HTML 元素、未执行 sentinel、未请求外部图片。
  - 未捕获 JS 异常、资源请求失败、未知 API、外部请求均为 0。保留并核对匿名跳转时
    whoami/stats/papers 的 **3 条预期 401 console error**，不将拒绝认证伪报为全部 console 零错误。
  - 所有测试只监听 loopback 随机端口；测试配置唯一额外项是 `plugins.rpc_ws_bind: "127.0.0.1:0"`，
    避免默认固定 RPC 端口。浏览器和后端均已退出，HTTP 端口关闭，本次临时 HOME/PocketBase 已删除。

浏览器最终证据：`build/security-browser-final.log` 和
`build/security-browser-results/run-5pxctm_p/results.json`。早期失败分别来自测试脚本的
Playwright handler 参数捕获、搜索选择器未限定 main、匿名拒绝请求未正确分类；修正测试夹具后
全量重跑退出 0，未以修改应用代码或吞掉未知请求换取通过。

`build/security-*` 保存本次本地审计、日志、UI 与回归证据，**全部不提交 Git**。
ZIP 仅为本地验证，未创建/覆盖 GitHub Release、tag、镜像或任何生产文件。

## 剩余风险与后续计划

截至上述审计时点，npm 报告内**没有未修复漏洞**；这不保证零未知漏洞或全栈无风险。
没有为零告警而隐藏以下非 audit 维护事项：

1. **ESLint 9 已被 npm 标注停止支持**。本次保留兼容 9.x，锁定已无已知告警的树；
   后续单独迁移 ESLint 10、React Hooks 插件 peer/config 和规则，不混入安全补丁。
   期间 CI 持续审计；如出现无 9.x 修复的新公告，优先安排迁移，不作永久豁免。
2. **Router CLI 的 CommonJS 循环加载 warning**：`--trace-warnings` 定位到
   `@tanstack/router-core/dist/cjs/router.cjs` 的非生产 HMR `_replaceRouteChunk` 初始化。
   路由生成与两次生产构建成功；未用 `NODE_NO_WARNINGS` 或 patch-package 掩盖。
   后续跟进上游修复再兼容升级；浏览器运行结果独立验证，不能把 CLI 成功当作运行时证明。
3. **现有构建体积/Fast Refresh 警告**：主 JS chunk 约 528 kB（gzip 172 kB），
   以及 theme-provider/badge/button/tabs 的四项导出 warning；不改变阈值或关闭 lint 规则。
   浏览器另观察到论文详情 Markdown 弹窗缺少 Description/aria-describedby 的 **2 条 Radix warning**
   （中英文各一次），影响辅助技术描述；后续补充本地化描述，不关闭可访问性检查。
   这些按性能/开发体验/可访问性任务处理，不与已知漏洞数混算。
4. **npm 11 安装脚本提示**：esbuild postinstall 尚未被环境的 allowScripts 批准；
   本机 optional 原生包安装及实际 Vite build 已成功。本次未扩张安装脚本授权或增加全局配置。
5. **验收边界**：不访问生产；业务数据与 OAuth 联动若使用 fixture，不代表生产数据库、
   外部 OAuth、对象存储或检索服务已联调。未来发版前仍需在获准的预发布环境执行真实集成验收。
