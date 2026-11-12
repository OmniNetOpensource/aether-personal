# 阿芒 · aether-personal

以维护者自用为主的开源 personal agent 项目：首要目标是维护者自己长期使用，其他人也可以自行部署，或使用维护者向公众开放的实例。欢迎其他开发者参与。交付范围为用户提供的完整三层架构，覆盖纯 Web/PWA 客户端、Go 后端、PostgreSQL、Agent 与外部工具，以及 dev/prod 环境。

当前版本包含邮箱验证码登录、Main 长期对话、异步后台并行 Subagent（带零损耗直通转发）、页面与 Agent 历史检索、主题记忆、支持并行推进的持久化需求队列、定时任务与闲置后台反思、联网搜索与网页/图片读取、阿里云 FC + AgenticFS 持久化个人电脑（含只读实时桌面与双向文件流转），以及 Poke 式的全屏 Web 聊天界面与独立管理看板。用户界面只显示与阿芒的消息和交付文件，工具与子代理过程留在后台。技术实现由开发者判断，产品决策先与用户确认。

## 已确认方向

| 项目 | 已确认内容 |
| --- | --- |
| 项目名称 | `aether-personal` |
| 产品名称 | 中文「阿芒」，英文 Aether |
| 前端 | TanStack Start + React + TypeScript；SPA 静态部署，移动端使用 PWA |
| 后端 | Go |
| 部署方式 | 通用 Docker Compose；维护者 Web 使用 Cloudflare，阿里云新加坡单机运行双对等后端容器（`api-1`、`api-2`）、纯内存 Redis 8 与 PostgreSQL 18；通过 `make publish` 零停机滚动更新 |
| Go 版本 | `1.27.2` |
| Go 模块路径 | `github.com/OmniNetOpensource/aether-personal/backend` |
| HTTP 层 | Go 标准库 `net/http` |
| 数据库与信号层 | `pgx v5.11.0 + sqlc v1.31.1`（PostgreSQL 持久事实与栅栏代数）+ `redis/go-redis v9`（纯内存唤醒、临时租约、Pub/Sub 与图片缓存） |
| 代码托管 | GitHub，仓库 `OmniNetOpensource/aether-personal`，公开 |
| 许可协议 | [Apache-2.0](LICENSE) |
| 数据库 | PostgreSQL 18（镜像 `pgvector/pgvector:pg18`）与纯内存 Redis 8；本地 dev 使用 Docker Compose |
| 模型服务 | OpenRouter、ikun Gemini 与 ikun Claude；Main 默认 `deepseek/deepseek-v4.1-flash-1017`，后台 Subagent 与上下文压缩均可单独指定模型（留空跟随 Main） |
| 密钥与费用 | OpenRouter / Resend Key 由实际部署者配置，费用由部署者承担；维护者实例由维护者承担 |
| 登录 | 邮箱验证码 |
| 验证码邮件 | Resend |
| 首次登录 | 新邮箱验证成功后自动创建用户 |
| Main Agent | 每用户固定一个逻辑 Main，按用户串行运行，状态持久化到 PostgreSQL |
| 调度与停机 | 对等无状态后端容器经 Redis 竞争消费与续期租约、以 `users.main_lease_epoch` 校验写库防脑裂；停机信号一到即取消本地运行、释放租约并重入队由存活容器接手，见 [对等多实例调度与停机交接](docs/wiki/05-main-runtime.md#对等多实例调度与停机交接2026-11-11) |
| 需求队列与并行 Subagent | `tasks` 持久化需求，支持多项独立的调研与分析任务同时标为 `in_progress` 并交由独立上下文的后台 Subagent 异步并行执行，跑完后经 `subagent_event` 唤醒 Main，由 `forward_subagent` 零改写损耗直发聊天并结项（个人沙箱电脑任务仍由 Main 单线程串行执行）；前端通过输入框上方胶囊与右上角浮层实时展示任务进展 |
| 主对话上下文 | 每用户一条长期主对话，使用同一份 `messageList` |
| 主题记忆 | 不自动注入记忆，Main 需要时通过 `memory` 先读根索引、再读相关主题；明确的长期偏好与约定默认自动维护，详细主题通过单一 `memory` 工具按需读写 |
| Linux 沙盒工作环境 | 阿里云 FC Agent Sandbox + AgenticFS 持久 `/mnt/workspace`，支持只读实时桌面观看与 50 MB 聊天双向文件互传，见 [沙盒与文件流转](docs/wiki/07-sandbox.md) |
| 处理中收到新消息 | 当前模型请求或工具调用结束后，在下一次模型调用前加入新消息 |
| 环境 | 实现时同时考虑 dev 和 prod |

## 完整交付范围

- Web/PWA Main 聊天界面：主页打开即建 WebSocket，登录、历史、收发全走同一连接，支持断线恢复。
- Go API：聊天 WebSocket（认证、历史、收发与推送）、管理与健康检查 HTTP 接口。
- 消息服务：消息路由、持久化、保存后直接触发接收方。
- PostgreSQL：agent 身份、消息历史、Run 状态和执行轨迹；表结构见[数据模型](docs/wiki/02-data-model.md)。
- Main Runtime：`messageList` 上下文，按用户串行处理。
- Main 直接使用联网执行工具完成任务。
- Main 在耗时任务中用正文汇报进度，完成后用正文给结果；连续 5 轮只有工具调用时，运行时插入一条用户看不见的提醒。历史检索 `search_message`、`read_message` 已实现：Main 可搜该用户全部消息记录。
- 按用户权限搜索和读取历史消息。
- 使用 `memory` 文件工具维护根文档与详细主题；每个命令与回执同事务提交，不依赖 compact 摘要反复重写。
- 通过 OpenRouter 调用模型。
- 搜索、HTTP API、MCP 等外部服务调用。
- dev/prod 环境配置及对应的验证。

上述范围不代表各项已经实现，当前状态见架构 Wiki。技术实现由开发者判断，影响用户体验和产品范围的决定先与用户确认。

## 仓库目录

采用同一仓库组织前端、后端、环境配置和文档：

| 目录 | 用途 |
| --- | --- |
| `backend/` | Go 后端 |
| `frontend/` | React Web/PWA 工程 |
| `admin/` | TanStack Start 管理看板与 Node 同源代理，独立运行 |
| `infra/` | dev/prod 环境和部署配置 |
| `docs/` | 架构图、架构 Wiki 与决策总览 |

后端内部目录组织：

| 后端目录 | 用途 |
| --- | --- |
| `backend/cmd/server/` | 程序入口 |
| `backend/internal/config/` | 环境配置 |
| `backend/internal/httpapi/` | API |
| `backend/internal/database/` | sqlc 生成代码 |
| `backend/db/migrations/` | 表结构迁移 |
| `backend/db/queries/` | 手写 SQL |

sqlc 配置为 `backend/sqlc.yaml`。生成的代码会提交进仓库，只有修改 `backend/db/queries/` 中的 SQL 时才需要安装 [sqlc](https://docs.sqlc.dev/en/latest/overview/install.html) `v1.31.1`，然后在 `backend/` 下执行 `sqlc generate`。

前端工程位于 `frontend/`，TanStack Start 管理文件路由与 HTML 外壳，React 渲染标准 DOM。Web 与移动 PWA 共用 SPA 静态产物 `frontend/build/web`，由 Go 同源提供，或通过 Cloudflare 分发并代理 WebSocket。PWA 提供安装入口、离线应用外壳和更新提示；聊天需要联网。见 [前端说明](frontend/README.md)。

## 本地开发

需要 Go `1.27.2`、Docker，以及 Node.js `>=22.12.0`（建议 Node 24）。前端依赖固定在 `frontend/package-lock.json`，不再需要 Flutter SDK。

首次准备基础配置与管理员登录：

```sh
cp backend/.env.example backend/.env
# 用 openssl rand -base64 32 生成主密钥，填入 backend/.env 的 SERVICE_ENCRYPTION_KEY
python3 scripts/set-admin-password.py
make dev
```

打开 [本地聊天](http://127.0.0.1:8080)。`make dev` 构建 Web/PWA、启动数据库、执行迁移并运行 Go；Go 根据 `FRONTEND_DIR` 同源提供页面与 WebSocket。页面打开后先显示 loading，WebSocket 就绪后本地 dev 自动进入固定 mock 用户 `dev@example.com`，无需点击登录、输入邮箱或验证码。

另一个终端执行 `make admin`，进入 [管理看板](http://127.0.0.1:5174) 的“配置”页填写 OpenRouter、ikun Gemini、ikun Claude、Resend、Serper、Parallel、EveryInfra、阿里云 OSS 和阿里云 FC 凭证。修改从下一次请求生效，无需重启。服务未配置时允许进入看板，使用该服务时报未配置。已有 `.env` 的升级方式见 [服务凭证管理](docs/wiki/08-admin-observability.md#服务凭证管理2026-10-30)。

运行编译与静态类型检查（仓库不保留或运行任何本地测试类脚本）：

```sh
make check
```

仅运行后端时仍可使用：

```sh
cd backend
cp .env.example .env
make db-up     # 启动本地 PostgreSQL 与纯内存 Redis，等到健康后才返回
make migrate   # 执行数据库迁移（goose，已作为 Go 工具依赖固定版本）
make run       # 加载 .env 运行后端
curl localhost:8080/healthz
make db-down   # 停止本地容器
```

| 环境变量 | 说明 |
| --- | --- |
| `ADMIN_PASSWORD_HASH` | 管理看板密码哈希，用 `python3 scripts/set-admin-password.py` 设置；留空关闭管理入口。见 [08](docs/wiki/08-admin-observability.md) |
| `APP_ENV` | 必填，`dev` 或 `prod`；决定日志格式（dev 文本，prod JSON） |
| `DATABASE_URL` | 必填，PostgreSQL 连接串 |
| `REDIS_URL` | 服务进程必填，Redis 连接串（本地示例 `redis://localhost:6379/0`，生产 Compose 自动注入） |
| `DATABASE_MAX_CONNS` | 单容器默认 `20`，最小 `5`；同时运行 Main 的用户数默认不超过该值减 4（可用 `AGENT_CAPACITY` 调小） |
| `SERVICE_ENCRYPTION_KEY` | 必填，Base64 编码的 32 字节加密主密钥，用 `openssl rand -base64 32` 生成；与数据库分开保存并备份，不能随意重新生成 |
| `MAIN_MODEL` | 可选，Main 的初始默认模型；管理看板保存后使用数据库选择，默认 `deepseek/deepseek-v4.1-flash-1017`；ikun Gemini 使用 `ikun:gemini-...` ID |
| `FRONTEND_DIR` | Web/PWA 构建目录；本地示例为 `../frontend/build/web`，相对后端进程工作目录 |
| `PORT` | 可选，HTTP 监听端口，默认 `8080` |
| `POSTGRES_PORT`、`REDIS_PORT` | 可选，本地开发容器映射到本机的端口，默认 `5432` 与 `6379` |

本地 dev 打开聊天页面即自动使用 `dev@example.com` 登录，自动登录失败时可点击「开发测试登录」重试；prod 不开放此入口。只模拟用户身份，模型和工具仍真实运行。

登录、历史与收发都走主页建立的聊天 WebSocket，没有对应的 HTTP 接口：页面在收到后端 `ready` 事件前显示 loading，dev 随即在同一连接上自动完成开发登录，prod 在同一连接上发送邮箱与验证码命令（dev 不发邮件，验证码打印在后端日志的 `login code` 一行）。协议见 [消息与实时通信](docs/wiki/04-messaging.md#websocket)。

## 项目资料

生产部署配置位于 `infra/prod/` 和 `infra/cloudflare/`，首次安装与更新步骤见 [Web 生产部署](docs/wiki/09-deployment.md)。维护者聊天域名为 `aether.forkicks.fun`，公众开放注册，首版无限额度开放。

完成一次 SSH 与 Cloudflare 登录配置后，在干净的 `main` 工作区运行 `make publish`，即可构建、上传、备份、迁移并发布前后端。`make publish-check` 只检查接入与依赖，不修改生产。脚本保留服务器上的生产密钥，发布记录和异机数据库备份保存在被 Git 忽略的 `.local/publish/`。具体配置及失败恢复见 [一键更新现有部署](docs/wiki/09-deployment.md#一键更新现有部署)。

仓库还提供 GitHub Actions 自动发布工作流：配置 `production` 环境的部署凭证后，push 到 `main` 自动构建并更新前后端。数据库备份先用 age 加密再保存为 Actions 产物，解密私钥保留在维护者本机。配置与恢复步骤见 [Push 后自动发布](docs/wiki/09-deployment.md#push-后自动发布)。

- [架构 Wiki](docs/wiki/README.md)：按主题完整说明架构与设计，包括每项决定的理由和各部分的实现状态。
- [架构图](docs/architecture.mmd)：Mermaid 格式的三层架构图。
- [决策总览](docs/decisions.md)：已确认选择的一览表及待确认问题。

## 管理看板（开发中）

看板代码位于本仓库的 [`admin/`](admin/README.md)，在仓库根目录运行 `make admin`（自动安装依赖、构建并启动），打开 [管理看板](http://127.0.0.1:5174)。看板与聊天端同为 TanStack Start + React + TypeScript 的 SPA，需要 Node.js 22.12 或更新版本；构建产物由本目录的 Node 服务托管并代理管理 API。在本仓库根目录执行 `python3 scripts/set-admin-password.py` 设置管理员密码，后端执行 `make migrate` 并重启。看板只需输入密码，聊天继续使用邮箱验证码。也可执行 `./start.sh` 统一启动聊天前后端与看板。

看板合并为「运行过程」与「配置」两页：可按用户、Agent（Main 或任一后台 Subagent）、时间范围、模型与请求结果筛选查看输入、推理、工具调用与截图、压缩摘要和失败请求，并点开单次调用查看实际请求与响应 JSON；在「配置」页统一管理对话、Subagent 与压缩模型、压缩阈值、闲置后台反思时长及全部第三方服务凭证与剩余额度。部署与统计口径见 [管理看板说明](docs/wiki/08-admin-observability.md)。
