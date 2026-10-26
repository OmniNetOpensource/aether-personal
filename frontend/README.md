# 阿芒 · Aether Lynx 客户端

ReactLynx + TypeScript 实现邮箱登录和 Main 聊天，保留原 Poke 式 IM 界面。Linux 通过浏览器使用。

## 本地运行

需要 Node.js `^20.19.0 || >=22.12.0`，建议 Node 24。在仓库根目录运行：

```sh
make dev
```

打开 <http://127.0.0.1:8080>。Go 同源提供页面、HTTP API 与 WebSocket；dev 点击「开发测试登录」即可使用固定 mock 用户进入聊天，无需邮箱或验证码；prod 使用邮箱验证码。

单独构建前端：

```sh
cd frontend
npm ci
npm run build
```

修改后重新执行构建并刷新浏览器。`npm run dev` 先构建共用界面，再启动网页宿主开发服务器，代理 API 和 WebSocket 到 `127.0.0.1:8080`；修改共用界面后仍需重新构建。`npm run dev:lynx` 用于原生 Explorer 的 bundle 开发，不等同于完整 App 宿主。

`PUBLIC_API_BASE_URL=https://api.example.com npm run build` 可指定其他后端；该后端需配置 HTTP 跨源访问。前端不得包含模型、邮件或沙盒供应商密钥。

## 工程边界

聊天页顶部的“查看桌面”打开当前用户的沙盒实时画面。宽屏并排显示，窄屏覆盖聊天。当前只读；暂停的沙盒保持暂停，Agent 下次使用电脑后自动连接。关闭面板或隐藏标签页会断开观看。

Go 后端同时提供 `/sandbox` 和 `/sandbox/desktop`，部署代理需要支持后者的 WebSocket Upgrade。浏览器通过现有聊天令牌认证，OpenSandbox 密钥只留在后端。原生宿主还需实现 `AetherHost.openDesktop`，当前只验收 Web。

- `src/App.tsx`：登录、消息分组、历史翻页、输入、回执、思考气泡。
- `src/chat.ts`：HTTP、第一帧 WebSocket 认证、退避重连、position 补消息、重发去重。
- `src/Markdown.tsx`：Markdown token 渲染为 Lynx 元素，不执行 HTML，不加载远程图片。
- `host/index.ts`：浏览器存储、WebSocket、键盘、视口、来源链接。
- `host/desktop.ts`：noVNC 桌面面板、连接状态和关闭清理。
- `public/native-modules.js`：Lynx worker 到网页宿主的模块桥接。

Flutter 的旧登录值（包括 SharedPreferencesAsync 的无前缀 JSON 字符串和旧版 flutter.* 键）在读取时转换到当前键；退出时同时移除旧 token，防止自动重新登录。

输出 `build/web/` 为可部署的网页，`dist/main.lynx.bundle` 为原生界面 bundle。Android、iOS、macOS、Windows 尚需实现宿主的 `AetherHost` 能力（见 `src/platform.ts`）、HTTP Service、`LynxWebSocketModule` 和键盘/视口事件，并完成原生构建与设备验收。bundle 编译通过不代表已经产出可安装 App。

Noto Sans SC 字体及 OFL 许可随 Web 产物分发。PrimJS 不支持的 Unicode 属性正则由 Babel 在构建时转换；WebSocket 模块延迟至后台连接时加载，避免主线程执行后台专用 API。

## 验证

```sh
npm run check
npm run build
```

不新增测试文件。行为通过 In-App Browser 连接本地真实后端检查；使用独立本地账号，历史翻页与复杂 Markdown 使用明确标记的本地样例，不作为模型回复的证据。2026-10-23 已完成本地验收：

- `make frontend` 从锁文件重装依赖，TypeScript 检查与 Web、Lynx 原生 bundle 构建通过。
- 真实邮箱验证码登录、模型回复、Main 思考气泡、已读回执、刷新恢复通过。
- Flutter SharedPreferencesAsync 的 JSON 字符串登录值转换后可继续进入聊天。
- 历史分页从 67 条补到 87 条，加载更早消息后保持原阅读位置。
- 断网时显示失败，恢复网络后点击重发成功；数据库核对同一条消息仅一行。
- 中文冒号后的来源链接可打开真实来源页，Markdown 表格、代码块、引用和图片替代文字正常显示。
- 320、390、768、1440 像素宽度无气泡越界，多行草稿可增高并在清空后回到 44px；Enter 发送、Shift+Enter 换行、32,001 字消息拦截通过。

上述设备尺寸为 In-App Browser 的视口检查。真实手机软键盘、原生宿主和原生安装包尚未验收。
