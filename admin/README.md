# aether 管理看板

本仓库 `admin/` 目录中的调用观测、全局运行参数与服务凭证配置后台，2026-11-03 起改用 TanStack Start + React + TypeScript（文件路由与 SPA 模式，和聊天客户端同款），构建出静态产物后仍由本目录的 Node 服务 `server.mjs` 托管，并把管理 API 按固定白名单代理到 Go 后端。看板独立于聊天客户端部署，通过管理员密码登录。

## 本地启动

需要 Node.js 22.12 或更新版本，Go 后端已完成 model_calls 迁移并运行。

```sh
cd admin
cp .env.example .env
npm ci
npm run build
npm start
```

以上命令从仓库根目录执行；也可直接在根目录运行 `make admin`（自动完成安装、构建与启动），或通过 `./start.sh` 统一启动聊天前后端与看板。

仅开发看板页面时，先启动 8080 端口的后端，再在 `admin/` 执行 `npm run dev`，打开 http://127.0.0.1:5174 。Rsbuild 开发服务器把 `/api` 去掉前缀后代理到后端，本机热更新不经过 `server.mjs`；正式运行与部署仍以构建产物 + `server.mjs` 为准。

打开 http://127.0.0.1:5174 。仅输入管理员密码即可进入，无需邮箱或验证码。在仓库根目录执行 `python3 scripts/set-admin-password.py` 设置密码，完成迁移并重启 Go 后端。密码哈希只在后端 `.env` 的 `ADMIN_PASSWORD_HASH` 中配置，不放进看板前端。

可用 `python3 scripts/set-admin-password.py --generate` 生成初始密码，明文保存到后端项目的 `backend/bin/admin-password.txt`，该文件不会提交。修改密码并重启后端后旧看板会话失效。未配置密码时关闭管理入口。

看板令牌固定 12 小时有效，仅保存在当前标签页的 sessionStorage。退出立即撤销该会话。聊天继续使用邮箱验证码，两个入口的令牌互不通用。管理员密码是共享凭证，不能识别具体个人。后端全局每分钟最多接受 10 次登录尝试。
`npm start` 实际执行 `node --env-file-if-exists=.env server.mjs`，自动加载本目录的 `.env`。也可直接配置进程环境变量后执行 `node server.mjs`。

`API_UPSTREAM` 是服务端固定的 Go 后端源站（支持逗号分隔多个对等后端实例，单容器滚动重启连接失败时自动切换下一个实例）；不支持 URL 路径、凭证或任意目标代理。浏览器只访问同源 `/api`，不需要 CORS，也不包含 OpenRouter、数据库或供应商密钥。会话令牌保存在当前标签页的 sessionStorage，退出时撤销。

## 文件职责

| 文件 | 职责 |
| --- | --- |
| `web.config.ts` | TanStack Start 与 Rsbuild 插件、SPA 外壳预渲染、开发代理及构建输出目录 |
| `src/router.tsx`、`src/routeTree.gen.ts` | 路由器配置与自动生成的文件路由类型（不手改生成文件） |
| `src/routes/__root.tsx`、`src/routes/index.tsx` | HTML 外壳、元信息、品牌图标与看板首页路由 |
| `src/AdminApp.tsx` | 管理员登录、侧边栏外壳、筛选、自动刷新与页面切换编排 |
| `src/api.ts` | 管理接口类型、统一请求与全看板共用的格式化工具 |
| `src/Overview.tsx`、`src/Calls.tsx`、`src/Timeline.tsx` | 运行概览、模型调用（含详情弹窗）与运行过程三个观测页 |
| `src/RuntimeSettings.tsx`、`src/ServiceSettings.tsx` | 配置页的全局运行参数与服务凭证 |
| `src/ui.tsx`、`src/style.css` | 共用小组件（徽章、面板、复制 ID、折叠长文本）与全局样式 |
| `server.mjs` | 生产入口：托管 `build/web` 静态产物，按白名单流式／缓冲代理 `/api` 到 Go 后端，并统一安全响应头 |
| `public/app-icon.png` | 产品图标，构建时复制进静态产物 |

`npm run build` 先生成文件路由并构建，再做类型检查；唯一部署产物是 `build/web`，`build/server` 仅供构建时预渲染，不随服务部署。

## 部署

将本目录按 `infra/prod/Dockerfile.admin` 构建镜像单独部署：构建阶段在镜像内 `npm ci && npm run build`，运行阶段只带 `server.mjs` 与 `build/web`，运行 `node server.mjs`，由 HTTPS 反向代理转发到 `127.0.0.1:5174`。配置 `HOST`、`PORT`、`API_UPSTREAM` 环境变量；密码哈希 `ADMIN_PASSWORD_HASH` 只存在后端环境。不要把 `.env` 作为静态资源发布。看板代码与后端位于同一 Git 仓库，独立运行，不嵌入聊天客户端构建产物。

## 数据口径

- 调用是新日志记录的模型 HTTP 尝试，和包含工具、等待、重试恢复的执行步骤分开。崩溃窗口可能留下中断 / 结果未知。
- 费用为模型提供方返回的已知 USD 小计；缺少费用的调用单独计数。它不包含搜索等服务成本。
- Token 缺失显示未知，明确返回的 0 才是已知 0。平均耗时只计算观测到完成的请求。
- 工具分布是模型提出的工具调用名称，不能当作已经执行成功。
- 历史步骤未接入调用日志时不回填，不混入新调用统计。
- 提供「运行过程」与「配置」两页。运行过程选择用户后按时间展示 Main 的输入、推理、工具调用与结果，长内容可加载全文。模型调用每页最多 50 条，可加载下一页；分页浏览时暂停自动刷新，刷新或更改筛选条件后恢复。配置页仅手动刷新。时间在界面按浏览器时区显示，请求使用 UTC。
- 每条调用详情独立读取实际请求与响应 JSON，可格式化查看和选中文本。包含消息上下文与工具定义，不保存 HTTP 认证头。旧日志显示未记录。非 JSON 响应显示原始文本，超过模型客户端 8 MiB 接收上限时明确标记截断。

验证：`npm run check`（TypeScript 类型检查 + `server.mjs` 语法检查）。不包含测试文件。

## 全局运行参数

在「配置」页修改全局压缩阈值和闲置反思时长。保存时若出现版本冲突，刷新后重新编辑。个人模型选择见 [App 模型设置](../docs/wiki/05-main-runtime.md#app-模型设置)。

## 服务配置

OpenRouter 由部署环境的 `OPENROUTER_API_KEY` 配置。看板显示配置状态和额度，其余服务的凭证在「配置」页保存。填写新 Key 替换，留空保留，点击“清除 Key”后续请求停止使用该服务。Gemini 地址默认 `https://api.ikuncode.cc`，只需填写 Key；Resend 同时填写发件人。保存不请求供应商，因此“已保存 · 未验证”不代表额度、权限或可用性通过验证。

后端先执行全部迁移并配置 `SERVICE_ENCRYPTION_KEY`，即使没有业务 Key 也能进入看板。Key 只以密文进入数据库，浏览器仅收到末尾四位和更新时间。现有 `.env` 使用 `make -C backend import-services` 一次性迁移，之后从看板维护。主密钥需单独备份，配置语义见 [架构 Wiki](../docs/wiki/08-admin-observability.md#服务凭证管理2026-10-30)。
