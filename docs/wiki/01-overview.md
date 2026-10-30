# 01 总体架构

完整架构图见 [`docs/architecture.mmd`](../architecture.mmd)（Mermaid 格式，GitHub 上可直接预览）。本页说明项目定位、各层组件的职责与实现状态，以及工程组织与部署方面的选择和理由。

## 项目定位

用户原话（2026-10-30 更新）：

> 改成主要给自己用，其他人也可以用。

因此：

- 首要目标是维护者自己长期使用；功能取舍、优先级与体验以维护者自己的实际使用为准，不再以企业部署后服务众多终端用户为首要设计目标。
- 项目仍然开源：其他人可以自行部署使用，也可以注册使用维护者向公众开放的实例；每位用户各自拥有自己的 Main 与 Subagent。
- 部署、配置和本地开发流程应让其他自行部署者能照文档上手，不能依赖维护者本机的私有资源；但不为企业级集成（如统一登录、多角色权限）预留设计，以后确有需要再单独讨论。
- 模型与发信的 Key 由实际部署者配置，费用由部署者承担；维护者实例由维护者承担。
- 支持删除用户，暂时只能由管理员删除，用户不能自行删除。
- 管理后台代码位于同一仓库的 `admin/`，作为独立服务部署，与 `frontend/` 聊天客户端分开。

此前的定位（2026-10-15）为企业级开源项目——“任何企业都可以 fork、部署，去服务他们的用户”，已由上述定位取代；更早的定位为“让每个人都能部署自己的 personal agent”。交付范围是整张架构图的全部内容，实现时同时考虑 dev 和 prod 环境。

## 三层结构

```mermaid
flowchart LR
    APP["第一层：React Web App<br/>移动端 PWA"]
    subgraph BACKEND["第二层：Go 后端（多台对等、无状态）"]
        API["API<br/>HTTP + WebSocket"]
        RUNTIME["Agent 运行时<br/>Main · Subagent"]
    end
    DB[("PostgreSQL 18 + pgvector<br/>共享存储")]
    subgraph EXTERNAL["第三层：外部服务"]
        OPENROUTER["OpenRouter<br/>模型 · embedding"]
        RESEND["Resend<br/>验证码邮件"]
        TOOLS["搜索 · HTTP/MCP<br/>浏览器 · 代码隔离环境"]
    end

    APP <-->|"WebSocket 登录、历史、<br/>收发与推送"| API
    API <--> DB
    RUNTIME <--> DB
    API -->|"唤醒"| RUNTIME
    RUNTIME <--> OPENROUTER
    API --> RESEND
    RUNTIME <--> TOOLS
```

### 第一层：Web 与 PWA 前端

2026-10-27，用户确认将前端彻底改为纯 Web 应用，移动端使用 PWA。这取代 2026-10-23 的 Lynx 选择及同日较早的 Android 原生宿主优先方案。桌面和移动端共用 React、React DOM、TypeScript 与 CSS，直接使用浏览器 API，不再生成 Lynx bundle 或维护原生容器。

选择理由：当前功能以邮箱登录、长期聊天与实时消息为主，标准 DOM 已能覆盖。React 直接渲染浏览器元素后，存储、WebSocket、中文输入法、选区、无障碍与键盘适配不再需要跨宿主桥接。PWA 使用同一份 Web 产物，移动端通过添加到主屏幕获得独立窗口。

| 组件 | 职责 | 状态 |
| --- | --- | --- |
| React 界面 | 邮箱登录、Main 消息、Markdown、回执、失败重发与真实思考动态 | 本地实现 |
| TypeScript 连接管理 | 主页打开即连 WebSocket、收到 `ready` 前显示 loading、`hello` 携带令牌、重连按 position 补消息、历史翻页与登录命令走同一连接、重发沿用 client_message_id；服务器事件在 `serverEvents.ts` 定义、由 `ServerEventRelay.ts` 统一中转 | 本地实现 |
| 浏览器存储 | localStorage 保存登录，兼容早期 Flutter 与 Lynx 的键；sessionStorage 按账号保存当前标签页草稿 | 本地实现 |
| PWA | 安装清单、多尺寸图标、离线应用外壳、显式版本更新 | 本地实现，移动真机待验收 |

保留 Poke 式 IM 布局：全屏消息列表、底部胶囊输入框，无联系人栏、头像、菜单、外层卡片和任务面板。Subagent 和工具过程留在后台，Main 的实际消息进入聊天。连续消息按发送方和两分钟间隔分组，三十分钟间隔或跨日显示时间；仅最新用户消息显示正常回执，失败消息保留重发入口。上翻阅读不自动回底，加载旧消息保留阅读位置。

2026-10-28，用户要求输入框外围透明，单行时文字与图标垂直居中，多行时加号和发送按钮固定在输入框底部，文字向上扩展，并在左侧增加加号。输入框以透明外围的浮层覆盖消息区，滚动内容为输入框实际高度预留底部空间；多行输入与已选文件改变高度时同步更新。用户确认本次加号仅提供本地文件选择入口，支持显示文件名和移除选择；不上传文件、不随聊天发送文件。

中文名为「阿芒」，英文名为 Aether。保留用户指定的深绿背景黄色毛绒角色图标，原图位于 `frontend/public/app-icon.png`；管理看板独立部署的副本位于 `admin/public/app-icon.png`。界面沿用森林绿 `#204b32`、柔黄 `#f6cf83` 与奶油白 `#fffcf4`，前端色值集中在 `frontend/src/palette.css`。使用系统字体，避免离线应用外壳依赖大型字体下载。2026-10-28，用户要求整体字号略微放大：正文与输入框由 16px 调整为 18px，其余文字同步增加 2px，并相应增加行高，改善阅读舒适度。

Main 正文由 marked 解析，再渲染成 React DOM，保留表格、代码块、列表、粗体、引用与来源链接。HTML 作为文本显示，远程图片显示替代文字；链接只允许无账号密码的 HTTP(S)，新窗口使用 `noopener noreferrer`。用户消息显示原文。Enter 发送、Shift+Enter 换行，中文输入法确认候选字不触发发送。通过 Visual Viewport 与安全区域适配移动键盘。

2026-10-28，用户要求删除整个顶部栏，让聊天内容直接占满页面顶部；同时删除页面的「安装应用」按钮和安装说明弹窗，安装继续使用浏览器自带入口。Service Worker 注册与显式更新继续保留，更新提示仅在有待更新版本时出现。

PWA 的清单位于 `frontend/public/manifest.webmanifest`。`frontend/scripts/build-pwa.mjs` 从现有图标生成普通图标、Apple 图标与带安全边距的 maskable 图标，随后按构建内容生成 Service Worker。缓存只包含本次构建的公开静态资源，不缓存认证响应、消息 API 或 WebSocket。入口缓存 `/`，避免 Go 对 `/index.html` 的重定向影响离线导航。

用户曾在线打开并完成缓存后，可在断网时打开应用、起草消息；聊天历史仍从服务器读取，重新打开后的离线界面不提供历史消息副本。发送消息、Main 推理和历史同步需要网络。新版 Service Worker 下载后等待用户点击「更新应用」，切换前不会自动刷新；当前标签页的草稿在刷新后恢复。Android 可使用浏览器安装入口，iPhone 与 iPad 在 Safari 的分享菜单中选择「添加到主屏幕」。PWA 安装依赖 HTTPS，本地 localhost 为开发例外。系统推送通知、后台持续连接尚未实现。

代码位于 `frontend/src/`，构建工具沿用 Rsbuild，依赖固定在 `frontend/package-lock.json`。`npm run build` 只生成 `frontend/build/web`，Go 同源服务和 Cloudflare Static Assets 继续读取该目录，聊天 WebSocket 契约不变。具体验收记录和命令见 [`frontend/README.md`](../../frontend/README.md)。旧框架的历史验收不作为当前 Web/PWA 的验收证据。

### 第二层：Go 后端

| 组件 | 职责 | 状态 |
| --- | --- | --- |
| 健康检查 | `GET /healthz`，同时检查数据库连接 | `已实现` |
| 认证 | 邮箱验证码登录、退出，全部走聊天 WebSocket 命令，见 [03 认证](03-auth.md) | `已实现` |
| 消息 API | WebSocket 登录、历史翻页、收发与推送；服务器事件定义与中转在 `events.go`，见 [04 消息与实时通信](04-messaging.md) | `已实现` |
| Main 运行时 | 按用户串行运行 Main，调用模型与工具，见 [05 Main 运行时](05-main-runtime.md) | `已实现` |
| `send_message` | 仅 Agent 间：Main 发给 Subagent，Subagent 发给 Main；Main 回复用户走正文 | `已实现` |
| `read_message` / `search_message` | 按 ID 读取、按关键词与语义搜索历史消息 | `仅设计` |
| `assign_subagents` | 派出 Subagent，异步启动并立即返回 `subagent_id` | `已实现` |
| `schedule` | Main 与 Subagent 安排一次性或周期任务，到期将保存的上下文交给 Main，见 [10](10-schedules.md) | `开发中` |
| Subagent 运行时 | 独立上下文、后台执行、每用户 3 个并行、持久排队、15 分钟超时 | `已实现` |
| 压缩 | 超过回答间隔或上下文阈值后，在下一次模型请求前生成摘要；阈值由看板配置 | `开发中` |
| 向量计算 | 后台为消息补算 embedding | `仅设计` |
| 管理员只读观测接口 | 用户、Main / Subagent、模型调用与历史步骤，见 [08](08-admin-observability.md) | `开发中` |
| 管理员用户操作接口 | 删除用户等管理操作 | `待确认` |

### 第三层：外部服务

| 服务 | 用途 | 状态 |
| --- | --- | --- |
| OpenRouter 对话接口 | Main 使用 `deepseek/deepseek-v4.1-flash-1017`（`MAIN_MODEL` 可改） | `已实现` |
| OpenRouter embedding 接口 | `google/gemini-embedding-2`，1536 维 | `仅设计` |
| Resend | 发送验证码邮件（仅 `prod`） | `已实现` |
| `search`、`fetch_url` | Serper、Parallel、EveryInfra，见 [05](05-main-runtime.md#search-与-fetch_url-已实现) | `已实现` |
| 其余 HTTP API、MCP | 后续外部执行工具 | `待确认` |
| Linux 沙盒工作环境 | 2026-10-29 起先不考虑，已有实现已删除，见 [07](07-sandbox.md) | 不在当前范围 |

## 语义澄清

### Main 是逻辑概念

用户原话：

> 自动创建用户。main agent只是一个逻辑概念。

因此，“每用户固定一个 Main Agent”表示用户与 Main 的逻辑关系，不能据此预设每用户拥有常驻进程、独立容器或独立服务。Main 由 `user_id` 确定，有消息要处理时才在某台后端上运行（见 [05 Main 运行时](05-main-runtime.md)）。

### 任务是虚拟概念，实体是 Subagent

用户原话：

> 在我的设想里，没有任务这个实体概念（只是说有这个虚拟概念）。

因此，“任务”只是 Main 交给 Subagent 的说明，不单独建模；实体是 Subagent 本身。数据库表命名为 `subagents`，其 ID 在数据库、代码、工具接口和架构图中统一称为 `subagent_id`，不使用 `task_id`。

### 架构图的修改记录

原始架构图只按用户确认修改过以下几处：第二层标题由“Go 后端 · 阿里云新加坡”改为“Go 后端”，以匹配通用部署定位；`task_id` 改为 `subagent_id`；Subagent 的“进度 / 完成 / 失败回报”改为与 Main 互相 `send_message`、失败时由运行时代发通知；前端通信由 HTTP/SSE 改为 HTTP + WebSocket。2026-10-23 按用户确认采用 B：Main 也连接共用执行工具。

## 原始架构给出的约束

- 消息保存后直接触发接收方。
- Main 使用 `messageList` 上下文，按用户串行处理。
- Main 保留消息与派发工具，并与 Subagent 共用执行工具；当前已实现 `search` 和 `fetch_url`。历史搜索和读取仍属于设计范围。
- `assign_subagents` 记录 Subagent 后异步启动，立即返回 `subagent_id`。
- Subagent 按任务创建，使用独立上下文，后台执行，限额并行。
- Subagent 与 Main 互相 `send_message`；Subagent 失败时由运行时代发通知。
- 历史消息搜索和读取受用户权限及消息归属限制。
- 保存消息、Run 状态及执行轨迹。
- 浏览器和代码任务在隔离执行环境中执行。
- Web/PWA 打开主页即通过 WebSocket 与后端交互，登录、历史与收发共用一条连接，支持断线恢复。

## 关键设计

**后端无状态，状态都在 PostgreSQL。** 任何一台后端都能处理任何用户的请求；机器之间用 PostgreSQL 的 `LISTEN/NOTIFY` 互相唤醒，用咨询锁（advisory lock）保证同一用户的 Main 同一时间只在一台机器上运行。不引入 Redis。内存中只有 WebSocket 连接与进行中的模型调用，丢失后都能恢复。详见 [04 消息与实时通信](04-messaging.md#整体结构)。

**一切通信都是消息。** 用户、Main、Subagent 之间只通过消息联系，每个 Agent 的消息列表就是它的模型上下文（见 [02 数据模型](02-data-model.md)）。2026-10-30 起，Main 生成的正文就是它对用户说的话（整条生成结束后一次性推送）；`send_message` 只用于 Main 与 Subagent 之间。

## 工程组织

同一仓库组织前端、后端、环境配置和文档：

| 目录 | 用途 | 状态 |
| --- | --- | --- |
| `backend/` | Go 后端 | 有代码 |
| `frontend/` | React Web/PWA、TypeScript 连接管理与静态构建 | 本地实现，移动真机待验收 |
| `admin/` | 管理看板页面与 Node 同源代理，独立运行和部署 | 有代码 |
| `infra/` | dev/prod 环境与部署配置 | 生产 Docker 和 Cloudflare 配置见 [09](09-deployment.md) |
| `docs/` | 架构图、本 wiki、决策总览 | 有内容 |

后端内部：

| 目录 | 用途 |
| --- | --- |
| `backend/cmd/server/` | 程序入口：读取配置、建立数据库连接池、启动 HTTP 服务；启动时唤醒仍有待读消息的用户 |
| `backend/internal/config/` | 启动时一次性读取环境变量 |
| `backend/internal/httpapi/` | HTTP 路由、WebSocket 聊天（登录、历史与收发）、服务器事件定义 |
| `backend/internal/agent/` | Main 运行时：运行循环、调用 OpenRouter、系统提示词与工具定义 |
| `backend/internal/database/` | sqlc 生成的数据库访问代码（不要手改） |
| `backend/db/migrations/` | goose 迁移文件 |
| `backend/db/queries/` | 手写 SQL，sqlc 据此生成代码 |

## 技术栈

| 部分 | 选择 | 理由 |
| --- | --- | --- |
| 后端语言 | Go `1.27.2` | 较低版本的 Go 会按 `go.mod` 自动下载对应工具链 |
| Go 模块路径 | `github.com/OmniNetOpensource/aether-personal/backend` | 与 GitHub 仓库一致 |
| HTTP 层 | 标准库 `net/http` | 不引入 Web 框架 |
| 日志 | `log/slog`；dev 输出文本，prod 输出 JSON | 标准库 |
| 数据库访问 | pgx `v5.11.0` + sqlc `v1.31.1` | 手写 SQL，sqlc 生成带类型的 Go 调用代码，由 pgx 执行 |
| 数据库 | PostgreSQL 18 + pgvector（镜像 `pgvector/pgvector:pg18`），主键 `uuidv7()` | 向量检索与业务数据放在同一个数据库 |
| 迁移 | goose `v3.29.0`（`go tool goose`） | 见 [02 数据模型](02-data-model.md#迁移) |
| WebSocket | `coder/websocket` | 见 [04 消息与实时通信](04-messaging.md#websocket) |
| 模型 | OpenRouter | 一个 Key 接入多家模型 |
| 邮件 | Resend，用 `net/http` 直接调用 | 见 [03 认证](03-auth.md) |
| 前端 | React + React DOM + TypeScript，Rsbuild 构建 | 纯 Web 应用，移动端安装 PWA |

## 后端骨架 `已实现`

- `internal/config` 启动时一次性读取环境变量，不满足即报错退出。
- `GET /healthz` 每次请求 ping 数据库，正常返回 `200 ok`，失败返回 `503`。
- 后端启动时不检查数据库连通性，避免数据库晚于后端就绪时出现崩溃重启循环；数据库状态由 `/healthz` 反映。
- 等待数据库就绪由编排层负责：dev compose 使用 `pg_isready` 健康检查，`make db-up` 带 `--wait`，数据库可连接后才返回；将来的 Docker 部署中，后端使用 `depends_on: condition: service_healthy` 等待数据库。
- 收到停止信号后取消运行时请求，HTTP 服务最多等待 5 秒关闭；未完成的执行步骤保留在数据库，重启后恢复。
- dev compose 固定项目名 `aether-personal-dev`，避免与其他目录名为 `dev` 的 compose 项目冲突。
- `.env` 位于 `backend/`，由 Makefile 加载并传给 compose；仓库提供 `backend/.env.example`。
- 已验证：数据库正常时 `/healthz` 返回 200，停止数据库后返回 503，缺少 `APP_ENV` 时启动报错退出。

## 配置

基础设施启动配置来自环境变量，dev 使用未提交的 `.env`。运行时模型选择和六组服务配置由管理员在看板保存到数据库，见 [08](08-admin-observability.md)。`APP_ENV` 必填且只允许 `dev` / `prod`。`SERVICE_ENCRYPTION_KEY` 必填；缺少供应商 Key 不阻止启动，管理员可先登录看板补齐，调用未配置的服务时报错。服务凭证不回退环境变量，旧值仅由显式导入命令迁移一次。

| 环境变量 | 说明 |
| --- | --- |
| `APP_ENV` | 必填，`dev` 或 `prod`；决定日志格式、是否真实发信 |
| `DATABASE_URL` | 必填，PostgreSQL 连接串 |
| `PORT` | 可选，默认 `8080` |
| `SERVICE_ENCRYPTION_KEY` | 必填，Base64 编码的 32 字节主密钥；凭证密文保存在数据库，主密钥由部署端单独保存和备份 |
| `MAIN_MODEL` | 可选，默认 `deepseek/deepseek-v4.1-flash-1017` |
| `SUBAGENT_MODEL` | 可选，留空使用 `MAIN_MODEL` |
| `DATABASE_MAX_CONNS` | 默认 20，最小 5；预留 4 条连接处理普通请求 |
| `FRONTEND_DIR` | 同源提供 Web/PWA 的构建目录 |
| `POSTGRES_PORT` | 可选，本地 dev 数据库映射到本机的端口，默认 `5432` |

## 代码托管

2026-10-27，用户要求默认直接在 `main` 开发，不再自行创建独立开发分支。开始前先获取远端最新 `main` 并同步已有工作；推送公开仓库仍需用户授权。

- GitHub 个人账号 `OmniNetOpensource` 下的公开仓库 `aether-personal`，许可协议 Apache-2.0。
- 维护者提交使用 GitHub noreply 邮箱，避免在公开仓库中暴露真实邮箱；发布前已从文档和提交历史中移除维护者本机路径。
- `.gitignore` 忽略系统文件、编辑器目录、`.env` 类文件和 `/backend/bin/`。
- 代码托管位置与应用运行位置是独立事项。

## 部署

- 项目提供通用 Docker Compose 部署；阿里云新加坡仅为维护者自己实例的部署位置。
- 2026-10-26 确认 Web 使用 Cloudflare，聊天域名为 `aether.forkicks.fun`。采用 Workers Static Assets 托管页面与同源代理，Go 与 PostgreSQL 运行在服务器，避免重写现有 Agent Runtime。配置、步骤与未完成的公众上线条件见 [09](09-deployment.md)。
- 迁移作为单独命令执行，不在后端启动时自动执行（理由见 [02 数据模型](02-data-model.md#迁移)）。
- 目前只部署一台后端，但代码按多台的要求编写。

## 模型服务与费用

项目以维护者自用为主、同时开源供其他人自行部署，因此模型、邮件和联网工具的 Key 由实际部署者配置，费用由部署者承担；维护者实例由维护者承担。Main 与 Subagent 的模型见 [05 Main 运行时](05-main-runtime.md#模型)。

## 本地开发

根目录 `make dev` 构建 Web/PWA、启动数据库、迁移并运行后端。Go 按 `FRONTEND_DIR` 提供页面，与 API 和 WebSocket 同源，避免独立开发端口的跨源问题。`make check` 检查 Go 和 TypeScript；`make check-integration` 在独立 schema 运行真实 PostgreSQL 集成测试。完整步骤见根目录 README。

用户在 2026-10-22 明确技术实现由开发者决定，仅产品决策询问用户。该要求替代此前逐项询问技术微决策的工作方式，已确认产品选择继续记录在 Wiki 与决策总览。

用户在 2026-10-29 进一步明确：没说改就不要改。代理可以先调查现状、核对是否符合预期并汇报结论与建议，但未经用户明确说改，不动代码与文档。理由是改动的时机与范围由用户决定，调查结论先经用户确认，避免代理把讨论当成已授权的执行。

## 代码注释

2026-10-28，用户要求写代码时给每一个小模块前写上注释，方便人类阅读。

选择理由：读代码的人先看注释就知道这一小块负责什么、为什么存在，再看实现确认细节，不必逐行推断作者的意图。这里的小模块包括文件、类型、函数／方法、组件，以及函数内可独立理解的一段逻辑；每处用一两句说明职责即可，不逐行复述代码，模块内部的其他注释仍只写代码本身表达不了的约束。

同一天，用户进一步要求每个文件开头都写文件头注释，并且更新文件时同步更新它，这样一眼扫过文件列表或刚打开文件就知道该不该继续读。确认的范围是手写源文件：后端与前端的手写代码、手写 SQL 查询（`backend/db/queries/`）、管理看板与 Worker 脚本、运维脚本。排除三类：sqlc 生成代码（重新生成会被覆盖，且约定不手改）、已落地的历史迁移（不应改动既有迁移内容）、JSON 等不支持注释的配置文件。Go 文件的文件头与 `package` 之间留一空行：紧贴 `package` 的注释会被 Go 工具当成整个包的文档注释，同一包每个文件都写就会互相冲突；留空行后它只是普通的文件头注释。带 shebang 的脚本把文件头放在 shebang 的下一行，因为第一行必须是解释器声明。

## 写代码前的预期核对

2026-10-28，用户曾要求在写代码之前先核对预期：把所有可能失败的点列出来，由用户确定该失败状态是否走得到。确认不可达的不写防御处理，确认可达的才设计失败处理。当时的理由是，不可达分支会让主流程长期背着用不到的状态和复核。

2026-10-30，用户要求删除这套协作方式。完成一项需求时，不再把可能的报错和失败状态列出来找用户确认。失败处理由开发者判断：防御仍只发生在系统边界，即后端 API 的请求、用户输入和外部服务响应；内部模块继续按互信契约编写。已经按当时核对确定的产品行为保持不变，例如上下文压缩只把模型调用失败当作需要处理的失败，见 [05 Main 运行时](05-main-runtime.md)。
