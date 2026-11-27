# aether Web/PWA

Vite+ + TanStack Router + React 19 + TypeScript + Effect 4（`effect` + `@effect/atom-react`）实现邮箱登录、Main 聊天、只读记忆浏览与远程记忆 MCP OAuth 授权确认，以纯静态 SPA 部署。桌面直接使用浏览器，移动端安装同一份 Web 应用为 PWA。构建产物仅为静态 HTML/JS/CSS 与 PWA 资源，浏览器通过 Go WebSocket 恢复登录与聊天。

## 本地运行

需要 Node.js `22.18+` / `24.11+`（建议 Node 24）与 pnpm `10`。在仓库根目录执行：

```sh
make dev
```

打开 [本地聊天](http://127.0.0.1:8080)。Go 同源提供页面与 WebSocket，认证与历史消息都走这条连接。dev 页面默认自动进入固定开发用户 `dev@example.com`，无需点击登录；自动登录失败时可点击「开发测试登录」重试。prod 使用邮箱验证码。

仅开发前端时，先启动 `8080` 端口的后端，再在 `frontend/` 执行：

```sh
pnpm install --frozen-lockfile
pnpm run dev
```

打开命令输出的地址，默认是 `localhost:3000`。Vite+ 提供 React 热更新，并将 `/ws`、`/sandbox/desktop`、`/model-settings`、`/memory-files`、`/.well-known`、`/mcp`、`/oauth/*`、`/images` 与 `/files` 代理至本地后端：页面打开即建立 WebSocket，收到后端 `ready` 事件前显示 loading，登录、历史与收发都走这条连接。端口已占用时可使用 `pnpm run dev -- --port 3001`。

构建、静态检查与格式化：

```sh
pnpm run check
pnpm run format
pnpm run build
```

`pnpm run build` 会先执行 Vite+（Rolldown）静态打包与文件路由拆包，再由 `vp check --no-fmt` 执行 Oxlint 与 TypeScript 类型检查。唯一部署目录是 `frontend/build/web`，继续兼容后端 `FRONTEND_DIR` 和 Cloudflare Static Assets。页面固定访问同源 API，前端不包含供应商密钥。依赖固定在 `pnpm-lock.yaml`。

## 移动端安装

在线打开部署后的 HTTPS 网站，通过浏览器自带入口安装。聊天页面不显示「安装应用」按钮或安装说明弹窗。

- Android：使用 Chrome 菜单中的「安装应用」或「添加到主屏幕」。
- iPhone、iPad：在 Safari 点击分享，再选择「添加到主屏幕」。
- 电脑：使用浏览器地址栏或菜单中的安装入口。

PWA 清单设置 `display: standalone`，安装后以独立窗口打开。多尺寸图标（192px、512px、Apple 180px 与带安全边距的 maskable 512px）作为静态资源保存在 `public/icons/`，安装一般要求 HTTPS，localhost 可用于本地开发。

## 存储与更新

登录保存为 `aether_session` 和 `aether_email`；浏览器无法保存登录时显示错误。草稿按账号写入当前标签页的 `sessionStorage`，刷新页面后恢复；关闭标签页后不保证保留。发送失败保留输入框内容供直接重发，同一次消息重发沿用 `client_message_id`。

应用不注册 Service Worker，入口 `/` 与 `/index.html` 通过 `public/_headers` 配置 `Cache-Control: no-cache`，刷新页面或重新打开即加载最新构建版本。

## 文件职责

| 文件 | 职责 |
| --- | --- |
| `index.html`、`vite.config.ts` | SPA 入口 HTML 模板、Vite+ 构建与 TanStack Router 文件路由插件配置、本地开发代理及格式化/检查规则 |
| `src/main.tsx`、`src/routes/`、`src/routeTree.gen.ts` | 浏览器入口、`RegistryProvider`、`/` / `/memories` / `/oauth/authorize` 文件路由与自动生成的路由树 |
| `src/App.tsx` | 主聊天入口：挂载 scoped 聊天会话与浏览器前后台生命周期 Atom，切换 loading、登录页与聊天页 |
| `src/lib/errors.ts`、`src/lib/runtime.ts` | `Data.TaggedError` 领域错误体系、统一错误文案格式化与 `Atom.runtime` 服务层装配 |
| `src/lib/ChatController.ts` | 纯 Effect 聊天会话控制器（`Scope`/`FiberHandle`/`Deferred`/`SubscriptionRef`）：登录、历史、消息回执、思考动态、退避重连与幂等重发 |
| `src/lib/serverEvents.ts`、`src/lib/ServerEventRelay.ts` | `Schema` 定义的 WebSocket 事件结构与唯一连接中转 |
| `src/lib/api.ts`、`src/lib/modelSettings.ts`、`src/lib/memories.ts`、`src/lib/oauth.ts` | 基于 `effect/http` + `Schema` 的同源 HTTP 服务、个人模型设置、只读记忆与 OAuth 2.1 接口 |
| `src/components/ModalDialog.tsx` | 原生 `<dialog>` 模态框公共封装 |
| `src/components/chat/` | 聊天布局、TanStack Virtual 消息列表、折叠、引用、历史搜索、只读桌面与多模态输入框 |
| `src/components/memories/` | `/memories` 双栏只读记忆预览页（文件列表筛选 + Markdown 预览与复制） |
| `src/components/login/`、`src/components/markdown/` | 邮箱验证码登录、Markdown/KaTeX/Shiki 渲染与公共 LRU 缓存 |
| `src/lib/platform.ts` | 浏览器登录存储与安全链接校验 |
| `src/styles/style.css`、`src/styles/palette.css` | 响应式布局与随系统切换的森林绿深浅色变量 |
| `public/manifest.webmanifest`、`public/icons/` | PWA 清单与静态多尺寸图标 |
