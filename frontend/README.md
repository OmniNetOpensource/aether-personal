# 阿芒 · Aether Web/PWA

React、React DOM 与 TypeScript 实现邮箱登录和 Main 聊天。桌面直接使用浏览器，移动端安装同一份 Web 应用为 PWA。界面直接使用标准 DOM，构建不再包含 Lynx、原生宿主或跨宿主桥接。

## 本地运行

需要 Node.js `^20.19.0 || >=22.12.0`，建议 Node 24。在仓库根目录执行：

```sh
make dev
```

打开 [本地聊天](http://127.0.0.1:8080)。Go 同源提供页面与 WebSocket，认证与历史消息都走这条连接。dev 页面默认自动进入固定开发用户 `dev@example.com`，无需点击登录；自动登录失败时可点击「开发测试登录」重试。prod 使用邮箱验证码。

仅开发前端时，先启动 `8080` 端口的后端，再在 `frontend/` 执行：

```sh
npm ci
npm run dev
```

打开命令输出的 `localhost:3000` 地址。Rsbuild 支持 React 热更新，只有 `/ws` 代理至本地后端：页面打开即建立 WebSocket，收到后端 `ready` 事件前显示 loading，登录、历史与收发都走这条连接。端口已占用时可使用 `npm run dev -- --port 3001`。开发服务器不注册 Service Worker；离线与更新验收使用正式构建。

构建与类型检查：

```sh
npm run check
npm run build
```

唯一部署产物是 `frontend/build/web`，继续兼容后端 `FRONTEND_DIR` 和 Cloudflare Static Assets。页面固定访问同源 API，前端不包含供应商密钥。依赖固定在 `package-lock.json`。

## 移动端安装

在线打开部署后的 HTTPS 网站，通过浏览器自带入口安装。聊天页面不再显示「安装应用」按钮或安装说明弹窗。

- Android：使用 Chrome 菜单中的「安装应用」或「添加到主屏幕」。
- iPhone、iPad：在 Safari 点击分享，再选择「添加到主屏幕」。
- 电脑：使用浏览器地址栏或菜单中的安装入口。

PWA 清单设置 `display: standalone`，安装后以独立窗口打开。构建脚本使用现有产品图标生成 192px、512px、Apple 180px 与 maskable 512px 图标。maskable 图标保留完整构图，并增加安全边距。安装一般要求 HTTPS，localhost 可用于本地开发。

## 离线与更新

Service Worker 只缓存构建时列出的公开静态资源，不缓存认证响应、聊天历史或 WebSocket。首次在线打开并完成缓存后，可以断网打开应用并编辑草稿；聊天与 Main 推理仍需要网络。离线新开的页面没有历史副本，恢复网络后通过既有 position 机制同步。

登录沿用 `aether_session` 和 `aether_email`，兼容 Flutter 的旧键与 JSON 编码；浏览器无法保存登录时显示错误。草稿按账号写入当前标签页的 sessionStorage，刷新与手动更新后恢复；关闭标签页后不保证保留。发送失败保留重发按钮，同一次消息重发沿用 `client_message_id`。

新版下载完成后出现「更新应用」，用户点击后才切换并刷新。构建内容和 Service Worker 生成脚本共同决定缓存版本。缓存入口使用 `/`，避免 Go 将 `/index.html` 重定向到 `/` 后，缓存的重定向响应无法用于导航。系统推送通知、后台持续连接和离线消息队列未实现。

## 文件职责

| 文件 | 职责 |
| --- | --- |
| `src/App.tsx` | 页面入口与前后台生命周期：loading、登录页与聊天页切换 |
| `src/ChatController.ts` | 界面状态：登录、历史、消息回执、思考动态、重连与幂等重发，只处理已校验的服务器事件 |
| `src/serverEvents.ts` | 后端发来的全部事件类型定义与校验结构，与后端 `events.go` 一一对应 |
| `src/ServerEventRelay.ts` | 前端唯一接触聊天 WebSocket 的文件：连接、发 `hello` 与命令、校验并中转服务器事件 |
| `src/ChatPage.tsx`、`src/ChatMessageList.tsx`、`src/MessageComposer.tsx` | 聊天布局、消息列表滚动与输入发送 |
| `src/LoginPage.tsx` | 邮箱验证码与开发测试登录表单 |
| `src/Markdown.tsx` | Markdown token 渲染为 React DOM，不执行 HTML，不加载远程图片 |
| `src/platform.ts` | 浏览器登录存储与来源链接校验 |
| `src/PwaControls.tsx` | Service Worker 注册、离线启动状态与显式更新 |
| `src/style.css`、`src/palette.css` | 响应式布局与现有绿、黄、奶油白配色 |
| `public/manifest.webmanifest` | PWA 名称、启动地址、窗口模式与图标清单 |
| `scripts/build-pwa.mjs` | 生成图标、按构建内容生成静态资源缓存与 Service Worker |

迁移前的本地 Lynx 源码和未提交 Android 工程保存在被 Git 忽略的 `.local/legacy-lynx-20261027/`，不参与构建。

## 本地验收

2026-10-27，已完成：

- TypeScript 检查、React 正式构建、Cloudflare Worker 语法检查与 Wrangler dry-run。
- In-App Browser 恢复既有登录与真实消息历史，开发服务器一键登录进入开发用户。
- PWA 清单由浏览器解析，无清单错误；Service Worker 控制页面，缓存中只有静态资源。
- 断网重新打开应用外壳、恢复草稿；断网发送显示失败，恢复网络后手动重发获得已读回执，收到真实 Main 回复「迁移验收通过」。
- 新版等待期间显示更新按钮，点击更新后页面加载新版，草稿保留。
- 320px、390px 移动视口无页面横向溢出，Shift+Enter 换行、Enter 发送；上翻阅读后编辑多行草稿不改变阅读位置。真实历史中的表格、代码块和来源链接使用标准 DOM 渲染。

手机尺寸检查属于 In-App Browser 视口验收，不能代替真实 iOS、Android 的主屏幕安装、系统键盘和后台生命周期验收。生产部署尚未完成。

官方参考：[PWA 安装](https://web.dev/learn/pwa/installation)、[Service Worker](https://web.dev/learn/pwa/service-workers)。
