# QuantumAtlas Web

QuantumAtlas 的 React SPA；API、PocketBase、静态资源和文档由 Go 服务端提供。
从新 checkout 开始的完整命令见 [Go 原生开发入门](../docsite/dev/development.rst)，
本页侧重当前前端路由、工具与本地联调。

## 工具与依赖

本次依赖链、利用条件、修复版本及回归结果见 [安全审计记录](SECURITY_AUDIT.md)。

- Node 版本唯一取自 [`.node-version`](.node-version)，用 `npm ci` 安装
  [`package-lock.json`](package-lock.json) 锁定的依赖；不要用一次构建顺带升级依赖。
- [`package.json`](package.json) 当前使用 Vite 7、React 19、TypeScript 5.6、
  Tailwind CSS 4（`@tailwindcss/vite`）、TanStack Router 与 TanStack Query。
- shadcn/ui 原语在 `src/components/ui/`，底层使用 `radix-ui`；
  `components.json`、`src/index.css` 管理组件路径及主题变量。
- PocketBase JS SDK 管理会话；`react-i18next` / `i18next-browser-languagedetector`
  负责语言；`next-themes`、`lucide-react`、`sonner` 分别提供主题、图标与通知。
- 论文详情和管理员资产共用 Markdown / LaTeX 预览，保留原文切换；实现与安全边界见
  [Markdown 与公式预览](MARKDOWN_PREVIEW.md)。unified/remark/KaTeX 在可终止的 Worker 中
  生成有界 HAST；主线程独立验证树后转换为 React 元素，不注入 HTML。
  原始 HTML 仅作可见文本，图片不自动加载；无需 marked 或 DOMPurify。
  KaTeX 脚本、CSS 和字体均从同一 npm 锁版本本地打包，不依赖 CDN。
- `@/*` 对应 `src/*`，配置位于 `vite.config.ts`、`tsconfig.json` 和
  `tsconfig.app.json`。Go 工具链门槛以根 `go.mod` 为准，不另设 Go 版本。

## 代码布局

```text
web/
├── .node-version                 Node 版本
├── package.json / package-lock.json
├── vite.config.ts                React/Tailwind/Router 插件、别名、开发代理
├── embed.go                      仅 -tags embedui 时嵌入 dist
├── embed_none.go                 普通源码构建，不要求 dist
├── resolve.go / bundle.go         精确 Release UI 的下载、校验、缓存和 fs.FS
└── src/
    ├── main.tsx                  Router/Query/Theme/Tooltip/Toaster providers
    ├── index.css                 Tailwind 与明暗主题 token
    ├── routeTree.gen.ts          TanStack 生成并按约定跟踪
    ├── i18n/index.ts             语言初始化与 namespace 声明
    ├── i18n/locales/{zh,en}.json
    ├── hooks/use-lang.ts
    ├── lib/api.ts                API 类型和请求封装
    ├── lib/queries.ts            Query hooks
    ├── lib/pb.ts / auth.ts        PocketBase 单例、OAuth 与会话校验
    ├── components/               页面组件、侧栏、顶栏及 ui 原语
    └── routes/                   下表中的文件路由
```

## 当前路由

普通界面使用 `/{lang}`（`zh` / `en`）前缀。`/` 按
`localStorage.qatlas_lang` → `navigator.language` → 默认 `zh` 选择语言；
这不是服务端读取 `Accept-Language` 的路由协商。登录和 OAuth 回调不带语言前缀，
`/pat`、`/device` 是保留查询参数的语言跳转入口。

下列文件均位于 `src/routes/`：

| URL | 路由文件 |
|---|---|
| `/` | `index.tsx`（跳转） |
| `/login` | `login.tsx` |
| `/auth/callback` | `auth.callback.tsx` |
| `/pat`、`/device` | `pat.tsx`、`device.tsx`（跳转） |
| `/{lang}` | `$lang.index.tsx` |
| `/{lang}/dashboard` | `$lang.dashboard.tsx` |
| `/{lang}/papers` | `$lang.papers.index.tsx` |
| `/{lang}/papers/search` | `$lang.papers.search.tsx` |
| `/{lang}/papers/{paperId}` | `$lang.papers.$paperId.tsx` |
| `/{lang}/downloader` | `$lang.downloader.tsx` |
| `/{lang}/device` | `$lang.device.tsx` |
| `/{lang}/pat` | `$lang.pat.tsx` |
| `/{lang}/admin` | `$lang.admin.index.tsx` |
| `/{lang}/admin/assets` | `$lang.admin.assets.tsx` |
| `/{lang}/admin/db/{table}` | `$lang.admin.db.$table.tsx` |
| `/{lang}/admin/docs` | `$lang.admin.docs.tsx` |
| `/{lang}/admin/downloader-workers` | `$lang.admin.downloader-workers.tsx` |
| `/{lang}/admin/pipelines` | `$lang.admin.pipelines.tsx` |
| `/{lang}/admin/plugins` | `$lang.admin.plugins.tsx` |
| `/{lang}/admin/users` | `$lang.admin.users.tsx` |

`__root.tsx` 提供认证门控，`$lang.tsx` 提供语言化布局。界面门控不代替后端鉴权。
`/api`、`/_`、`/share`、`/swagger`、`/doc`、`/devdoc` 是后端入口，
不是 TanStack 页面；`/{lang}/admin/docs` 是开发文档的前端入口。

## 本地开发：先准备 UI，再启动隔离 Go 后端

`npm run dev` 只启动 Vite，没有内置 API。没有代理时，API 请求可能落入 SPA
fallback 并返回 HTML，出现 `Unexpected token '<'`。默认只连接本机隔离后端，
**不要把生产或携带生产凭据的远端服务作为日常前端开发目标**。

1. 在仓库根按 [完整 UI 步骤](../docsite/dev/development.rst) 构建：
   Sphinx 公开站与开发站 → `npm ci` / `npm run build` → `-tags embedui`。
   只运行 npm 不会生成两个 Sphinx 站点。
2. 本地 `go run ./cmd/qatlasd` 通常是 `dev` 版本，没有对应 Release UI；
   即使 Vite 提供页面，Go 的 `serve` 仍会验证完整 UI，**不能省略 `-tags embedui`**。
3. 在独立终端运行下面的隔离配置示例（仓库根、Bash）。
   `env -i` 清除真实测试目标和凭据，只复用 PATH / Go 缓存；临时 HOME/XDG
   防止读取默认配置、已有文档覆盖或业务数据。Ctrl-C 退出后只删除本例创建的临时目录。

```bash
(
  set -euo pipefail
  DEV_HOME="$(mktemp -d)"
  trap 'rm -rf "$DEV_HOME"' EXIT
  env -i PATH="$PATH" HOME="$DEV_HOME" \
    XDG_CONFIG_HOME="$DEV_HOME/.config" \
    XDG_DATA_HOME="$DEV_HOME/.local/share" \
    XDG_STATE_HOME="$DEV_HOME/.local/state" \
    XDG_CACHE_HOME="$DEV_HOME/.cache" \
    GOPATH="$(go env GOPATH)" GOCACHE="$(go env GOCACHE)" \
    GOMODCACHE="$(go env GOMODCACHE)" CGO_ENABLED=0 \
    bash -eu -c '
      go run ./cmd/qatlasd config init --config "$HOME/config.yaml"
      go run -tags embedui ./cmd/qatlasd serve \
        --config "$HOME/config.yaml" --http=127.0.0.1:4200
    '
)
```

生成的默认 YAML 不配置 PostgreSQL、S3、OAuth、MinerU 或远端微服务；
本地文件存储与 PocketBase 数据在临时目录。它适合界面检查，不代表 API 已有测试数据，
也不代表所有页面都可用；不要触发外部检索或下载。需要持久数据或权限联调时，单独
准备已审核的开发 YAML 和可丢弃数据源，仍显式传 `--config`，不要复制生产配置。
服务端默认配置是 `~/.qatlas/config.yaml`；`QATLAS_SYSTEM_PAT` 等配置形状的
环境变量已被拒绝，system PAT 应写在 YAML 的 `system_pat.token` / `scopes`。

另一个终端在仓库根执行：

```bash
cp web/.env.development.example web/.env.development.local
# 审核本地文件：TARGET 保持 http://127.0.0.1:4200；只看界面可设 FAKE_AUTH=1
(cd web && npm ci)
(cd web && npm run dev -- --host 127.0.0.1)
```

访问 Vite 输出的本机地址（通常 `http://127.0.0.1:5173`）。修改 env 文件后重启 Vite。
前端修改通过 Vite HMR 生效；Go handler 修改需重启 Go 进程。

### 代理与认证边界

实现以 [`vite.config.ts`](vite.config.ts) 和 `src/lib/auth.ts` 为准；
[`.env.development.example`](.env.development.example) 不含凭据。

| 变量 | 实际行为与限制 |
|---|---|
| `VITE_DEV_API_TARGET` | 将 `/api`、`/_`、`/share`、`/swagger` 代理到指定后端；推荐本机 `http://127.0.0.1:4200`。所有代理项当前 `secure: false`，即不验证上游 HTTPS 证书，不是生产安全默认。 |
| `VITE_DEV_FAKE_AUTH` | 值为 `1` 且 `import.meta.env.DEV` 为真时，`useAuth()` 返回前端 stub；不生成真实后端会话，不授予读、写或管理员权限。 |
| `VITE_DEV_API_PAT` | 配置代理的 `/api` Authorization Bearer 请求头；不向 `/_`、`/share`、`/swagger` 注入。使用后端认可且最小权限的开发 token。 |
| `VITE_DEV_ALLOWED_HOSTS` | 逗号分隔的 Vite `allowedHosts` 配置；未设置时为 `localhost` / `127.0.0.1`。这不是认证或网络隔离措施，只有确需受控反向代理预览时才填写。 |

- **fake auth 只解除前端门控**。未提供真实凭据的受保护 API 仍会拒绝请求；
  注入 PAT 或浏览器持有真实会话后，权限由后端及该凭据决定，可能执行写入。
  不能宣称“fake auth 下写操作必定 401”或“读操作必定成功”。
- 最小权限开发 PAT（例如按需配置只读 scope）也不能代替所有用户会话/管理员路由的
  鉴权，部分页面需要真实开发账号。不要为让页面工作而直接搬用生产 system PAT。
- `VITE_` 是 Vite 默认可暴露给客户端的环境变量前缀。
  代理使用 `VITE_DEV_API_PAT` **不构成永不入包保证**；build 的 mode、
  `.env` 文件、shell 注入和后续客户端引用都影响泄漏风险。
  仅在隔离开发环境的 `.env.development.local` 使用 token；该文件虽被忽略，
  打包前仍须确保当前 shell 与所有会加载的 env 文件不含 token，勿用带凭据的
  development mode 构建生产资源。不要把 token 写入源码、命令行、截图或日志。
- Vite 服务只监听 loopback；它不是可公开部署的带凭据 API 网关。
  代理下的真实 Bearer 会把相应后端权限暴露给能使用这个开发服务的调用者。
- HMR WebSocket 不走 `/api` 代理；`/api` 的后端 WebSocket 则已启用 `ws: true`。
  `/doc` / `/devdoc` 没有配置 Vite 代理，请在 Go 后端地址验证文档门控。

## 常用命令与资源分发

在 `web/` 执行：

```bash
npm ci
npm run dev -- --host 127.0.0.1
npm run lint
npm test       # jsdom 下的 Markdown / 公式 / XSS 单元测试，不加载应用代理配置
npm run build  # tsr generate && tsc -b && vite build
npm exec -- playwright install chromium  # 首次安装测试浏览器
npm run test:browser  # 本机静态构建 + 显式 API fixture，无生产连接
npm run preview -- --host 127.0.0.1
```

`preview` 仅预览静态构建，不应假定具有开发 API 代理，也不代替 Go 的鉴权验证。
前端 API 类型在 `src/lib/api.ts` 手动维护。未配置的旧 `gen:api` 命令及
`@hey-api/openapi-ts` 已移除，不是通过忽略审计来保留旧生成链。
后端规范的权威生成命令仍在仓库根运行：

```bash
go tool swag init -g main.go -d ./cmd/qatlasd,./internal/routes \
  -o internal/apidocs --parseInternal --parseDepth 1
```

`web/src/lib/api.ts` 的客户端类型按实际 API 变更维护。
`src/routeTree.gen.ts` 按约定跟踪，构建后审核其差异；其他站点/分发资源不提交 Git。

- 普通 `go build` / `go test` 不要求 `dist`、Node 或 Python；
  完整 UI 构建后，用 `CGO_ENABLED=0 go build -tags embedui -o build/qatlasd ./cmd/qatlasd`
  构建本地服务（仓库根）。Go 运行中的内嵌文件不会随 Vite HMR 改变。
- 采用此分发格式且附件已公开的精确 tag，可通过 `go install ...@vX.Y.Z` 源码安装；
  安装不运行 npm。首次 `serve` 从同一 Release 下载 UI ZIP 与 SHA256 清单，
  校验 SHA256、包内版本及完整性后按版本缓存，后续启动仍校验缓存，不回退 latest。
  `dev` / 伪版本必须完整构建两文档站和前端，再使用 `embedui`。
- GoReleaser tar.gz 中的程序已经内嵌与独立 `_web.zip` 一致的 UI 资源。
  正式版本仅从已审核的 `v<version>` Git tag 派生，不维护根版本文件或为版本单独提交 bump。
  本地 UI 构建不是发布授权；不重发 `v0.34.0`，不修改历史 tag。
- `uibundle -version` 要求显式 SemVer。完成完整 UI 构建后，无正式 tag 时在仓库根执行：

  ```bash
  UI_VERSION="0.0.0-ci.g$(git rev-parse HEAD)"
  go run ./internal/cmd/uibundle -version "$UI_VERSION" -output build/ui
  ```

  此完整 SHA 标识只用于本地/普通 branch、PR CI 的打包和恢复验证，不是发布版本，
  不创建 tag、版本文件，也不发布；`g` 前缀避免全数字 SHA 形成非法 SemVer。
  正式 CI 只从传入的精确 `release_tag` 去 `v` 取版本，并验证 tag 所指提交 SHA = source SHA = `HEAD`。
  本地也只有 checkout 干净且已核验的正式 `TAG` 指向候选 SHA / `HEAD` 时才可用 `UI_VERSION="${TAG#v}"`，
  不从最近旧 tag 推断待发版本；完整步骤见[开发入门](../docsite/dev/development.rst)。
- Git 不提交 `web/dist`、`web/public/doc`、`web/public/devdoc`、根 `dist/` /
  `build/`、`node_modules`、缓存或 ELF。`/devdoc` HTTP 门控要求管理员，
  但公开 bundle 可直接读取开发文档，文档不能存放秘密。

## 国际化与主题

URL 中的语言前缀是权威信号，`$lang.tsx` 同步 `i18n.changeLanguage()`。
翻译文件为 `src/i18n/locales/{zh,en}.json`；新增字符串同时维护两种语言。
当前声明的 namespace 是 `common home papers token pat admin login auth dashboard downloader`；
namespace 名称不代表存在同名路由。语言切换通过导航保留查询参数。

`next-themes` 将 light/dark/system 偏好保存在 `qatlas_theme`。
颜色位于 `src/index.css` 的 `:root` / `.dark`，经 `@theme inline` 提供给 Tailwind；
组件使用 `bg-background`、`text-foreground` 等语义 token。
