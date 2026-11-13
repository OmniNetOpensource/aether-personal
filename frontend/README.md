# aether Web/PWA

TanStack Start、React 与 TypeScript 实现邮箱登录和 Main 聊天，使用文件路由与 SPA 模式。桌面直接使用浏览器，移动端安装同一份 Web 应用为 PWA。Start 在构建时预渲染 HTML 外壳，浏览器通过 Go WebSocket 恢复登录与聊天。

## 本地运行

需要 Node.js `>=22.12.0`，建议 Node 24。在仓库根目录执行：

```sh
make dev
```

打开 [本地聊天](http://127.0.0.1:8080)。Go 同源提供页面与 WebSocket，认证与历史消息都走这条连接。dev 页面默认自动进入固定开发用户 `dev@example.com`，无需点击登录；自动登录失败时可点击「开发测试登录」重试。prod 使用邮箱验证码。

仅开发前端时，先启动 `8080` 端口的后端，再在 `frontend/` 执行：

```sh
npm ci
npm run dev
```

打开命令输出的地址，默认是 `localhost:3000`。TanStack Start 通过 Rsbuild 提供 React 热更新和文件路由生成，`/ws` 与图片上传 `/images` 代理至本地后端：页面打开即建立 WebSocket，收到后端 `ready` 事件前显示 loading，登录、历史与收发都走这条连接。端口已占用时可使用 `npm run dev -- --port 3001`。开发服务器不注册 Service Worker；离线与更新验收使用正式构建。

构建与类型检查：

```sh
npm run check
npm run build
```

`npm run build` 会先生成文件路由和构建，再检查类型并生成 PWA 资源。`src/routeTree.gen.ts` 由 Start 自动维护并提交，不手改。唯一部署目录是 `frontend/build/web`，继续兼容后端 `FRONTEND_DIR` 和 Cloudflare Static Assets。`frontend/build/server` 仅供构建时预渲染使用，不部署，不需要常驻 Node 服务。页面固定访问同源 API，前端不包含供应商密钥。依赖固定在 `package-lock.json`。

## 移动端安装

在线打开部署后的 HTTPS 网站，通过浏览器自带入口安装。聊天页面不再显示「安装应用」按钮或安装说明弹窗。

- Android：使用 Chrome 菜单中的「安装应用」或「添加到主屏幕」。
- iPhone、iPad：在 Safari 点击分享，再选择「添加到主屏幕」。
- 电脑：使用浏览器地址栏或菜单中的安装入口。

PWA 清单设置 `display: standalone`，安装后以独立窗口打开。构建脚本使用现有产品图标生成 192px、512px、Apple 180px 与 maskable 512px 图标。maskable 图标保留完整构图，并增加安全边距。安装一般要求 HTTPS，localhost 可用于本地开发。

## 离线与更新

Service Worker 只缓存构建时列出的公开静态资源，不缓存认证响应、聊天历史或 WebSocket。首次在线打开并完成缓存后，可以断网加载应用外壳；聊天与 Main 推理仍需要网络。当前有一个迁移前已存在的限制：离线冷启动时，聊天控制器未收到后端 `ready`，页面仍显示 loading，暂时无法进入草稿编辑。恢复网络后通过既有 position 机制同步，离线新开的页面没有历史副本。

登录沿用 `aether_session` 和 `aether_email`，兼容 Flutter 的旧键与 JSON 编码；浏览器无法保存登录时显示错误。草稿按账号写入当前标签页的 sessionStorage，刷新与手动更新后恢复；关闭标签页后不保证保留。发送失败保留重发按钮，同一次消息重发沿用 `client_message_id`。

联网导航优先读取当前页面，网络请求失败时使用缓存的应用外壳。前台页面每分钟检查一次版本，打开页面、回到前台或重新联网时立即检查。新版资源下载完成后，Service Worker 自动激活并刷新它控制的应用页面。构建内容和 Service Worker 生成脚本共同决定缓存版本，离线入口为 `/`。

## 文件职责

| 文件 | 职责 |
| --- | --- |
| `web.config.ts` | TanStack Start 与 Rsbuild 插件、SPA 外壳预渲染、开发代理及客户端和服务端输出目录 |
| `src/router.tsx`、`src/routeTree.gen.ts` | 路由器配置与自动生成的文件路由类型 |
| `src/routes/__root.tsx`、`src/routes/index.tsx` | HTML 外壳、元信息、全局样式、PWA 清单与聊天首页 |
| `src/App.tsx` | 页面入口与前后台生命周期：loading、登录页与聊天页切换 |
| `src/ChatController.ts` | 界面状态：登录、历史、消息回执、思考动态、重连与幂等重发，只处理已校验的服务器事件 |
| `src/serverEvents.ts` | 后端发来的全部事件类型定义与校验结构，与后端 `events.go` 一一对应 |
| `src/ServerEventRelay.ts` | 前端唯一接触聊天 WebSocket 的文件：连接、发 `hello` 与命令、校验并中转服务器事件 |
| `src/ChatPage.tsx`、`src/ChatMessageList.tsx`、`src/MessageComposer.tsx` | 聊天布局、消息列表虚拟滚动与输入发送 |
| `src/RichMessageInput.tsx`、`src/messageAttachments.ts` | Tiptap 多模态混排、内容类型与 OSS 上传校验 |
| `src/QuoteSelection.tsx`、`src/ImagePreview.tsx` | 助手选区引用、原文回看与原图预览 |
| `src/LoginPage.tsx` | 邮箱验证码与开发测试登录表单 |
| `src/Markdown.tsx` | 同款 remark / GFM / KaTeX 管线，清洗后渲染 React DOM，展示 HTTP(S) 图片 |
| `src/MarkdownCodeBlock.tsx`、`src/shiki.ts`、`src/markdown.css` | 代码高亮与复制、按需语言包、Markdown 排版与移动端滚动 |
| `src/platform.ts` | 浏览器登录存储与来源链接校验 |
| `src/components/PwaControls.tsx` | Service Worker 注册、自动检查更新与离线启动状态 |
| `src/style.css`、`src/palette.css` | 响应式布局与现有绿、黄、奶油白配色 |
| `public/manifest.webmanifest` | PWA 名称、启动地址、窗口模式与图标清单 |
| `scripts/build-pwa.mjs` | 生成图标、按构建内容生成静态资源缓存与 Service Worker |

迁移前的本地 Lynx 源码和未提交 Android 工程保存在被 Git 忽略的 `.local/legacy-lynx-20261027/`，不参与构建。

## 本地验收

2026-11-02，TanStack Start 迁移的本地验收：

- Start SPA 外壳预渲染、TypeScript 检查、Cloudflare Worker 语法检查与 Wrangler dry-run 通过。
- In-App Browser 从本地 Go 服务加载正式静态产物，恢复开发用户登录与历史消息，未发现 hydration 错误。刷新后草稿恢复。
- Service Worker 激活，缓存清单仅含公开静态资源，不含 WebSocket、认证响应或构建用服务端产物；断网可以加载外壳，离线冷启动的 loading 限制见上文。
- 390px 宽度下无页面横向溢出。真机安装和生产部署未在本次迁移中验收。

以下为旧入口下的历史验收记录，不作为当前 Start 入口的完整验收证据。

2026-10-27，已完成：

- TypeScript 检查、React 正式构建、Cloudflare Worker 语法检查与 Wrangler dry-run。
- In-App Browser 恢复既有登录与真实消息历史，开发服务器一键登录进入开发用户。
- PWA 清单由浏览器解析，无清单错误；Service Worker 控制页面，缓存中只有静态资源。
- 断网重新打开应用外壳、恢复草稿；断网发送显示失败，恢复网络后手动重发获得已读回执，收到真实 Main 回复「迁移验收通过」。
- 新版等待期间显示更新按钮，点击更新后页面加载新版，草稿保留。
- 320px、390px 移动视口无页面横向溢出，Enter 换行、Ctrl+Enter 发送；上翻阅读后编辑多行草稿不改变阅读位置。真实历史中的表格、代码块和来源链接使用标准 DOM 渲染。

手机尺寸检查属于 In-App Browser 视口验收，不能代替真实 iOS、Android 的主屏幕安装、系统键盘和后台生命周期验收。生产部署尚未完成。

官方参考：[PWA 安装](https://web.dev/learn/pwa/installation)、[Service Worker](https://web.dev/learn/pwa/service-workers)。
