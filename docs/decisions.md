# 决策总览

所有已确认选择的一览表与待确认问题。每项选择的详细说明、理由与用户原话在 [架构 Wiki](wiki/README.md) 的对应主题页中。

记录始于 2026-10-14（Asia/Shanghai）。

## 协作约束

- 技术实现由开发者判断；仅产品决策询问用户，一次讨论一个问题。用户在 2026-10-22 明确用此规则替代此前“任何微决策先询问”的要求，原话为“不要问我这种技术问题，问我产品决策问题就行”。
- 交付范围是整张架构图的全部内容。
- 实现逻辑时同时考虑 dev 和 prod 环境。
- 变量名称必须直观，禁止使用抽象的变量名。
- 禁止写任何测试文件。用户在 2026-10-23 明确要求记下此规则。
- 提交时采用小规模、自包含的 commit。用户在 2026-10-23 明确要求记下此规则。

## 已确认选择

### 项目与工程

| 事项 | 选择 | 详见 |
| --- | --- | --- |
| 项目定位 | 企业级开源 personal agent，企业 fork、部署后服务其终端用户 | [01](wiki/01-overview.md#项目定位) |
| 项目名称 | `aether-personal` | [01](wiki/01-overview.md#代码托管) |
| 用户删除 | 仅企业管理员可删除，终端用户不能自行删除 | [01](wiki/01-overview.md#项目定位) |
| 管理后台 | 单独的前端仓库、单独部署 | [01](wiki/01-overview.md#项目定位) |
| Main Agent | 逻辑概念，不对应常驻进程或容器 | [01](wiki/01-overview.md#main-是逻辑概念) |
| 任务 | 虚拟概念，实体是 Subagent，统一称 `subagent_id` | [01](wiki/01-overview.md#任务是虚拟概念实体是-subagent) |
| 仓库组织 | 同一仓库：`backend/`、`frontend/`、`infra/`、`docs/` | [01](wiki/01-overview.md#工程组织) |
| Flutter 目标平台 | Android + iOS + macOS + Windows + Linux + Web | [01](wiki/01-overview.md#第一层flutter-前端) |
| 近期 Flutter 界面与验收 | 2026-10-23 改为 Poke 式 IM，按用户追加要求全屏只保留消息列表和输入框；白底灰色气泡，无联系人栏、菜单或外层卡片；先验收 Flutter Web，保留六平台目标 | [01](wiki/01-overview.md#第一层flutter-前端) |
| 本地 Flutter SDK | 官方 stable 渠道，当前 Flutter `3.47.7`、Dart `3.13.5`，安装到 `$HOME/develop/flutter`，不修改全局 shell 配置 | [01](wiki/01-overview.md#第一层flutter-前端) |
| Flutter 工程标识 | `aether_personal`，组织 `io.github.omninetopensource`，显示名称 Aether | [01](wiki/01-overview.md#第一层flutter-前端) |
| 本地 Web 接入 | Go 用 `FRONTEND_DIR` 提供构建产物，与 HTTP API、WebSocket 同源；根目录 `make dev` 启动 | [01](wiki/01-overview.md#本地开发) |
| 后端 | Go `1.27.2`，标准库 `net/http`，`log/slog` | [01](wiki/01-overview.md#技术栈) |
| Go 模块路径 | `github.com/OmniNetOpensource/aether-personal/backend` | [01](wiki/01-overview.md#技术栈) |
| 数据库访问层 | `pgx v5.11.0 + sqlc v1.31.1` | [01](wiki/01-overview.md#技术栈) |
| 配置来源 | 只读环境变量；`APP_ENV` 必填，只允许 `dev` / `prod` | [01](wiki/01-overview.md#配置) |
| 健康检查 | `GET /healthz`，同时检查数据库连接；启动时不检查数据库 | [01](wiki/01-overview.md#后端骨架-已实现) |
| 监听端口 | `PORT`，默认 `8080` | [01](wiki/01-overview.md#配置) |
| 本地 dev 数据库 | Docker Compose，端口 `${POSTGRES_PORT:-5432}` | [01](wiki/01-overview.md#后端骨架-已实现) |
| 本地常用命令 | `backend/Makefile` | [01](wiki/01-overview.md#后端骨架-已实现) |
| 代码托管 | GitHub `OmniNetOpensource/aether-personal`，公开，Apache-2.0，提交使用 noreply 邮箱 | [01](wiki/01-overview.md#代码托管) |
| 部署方式 | 通用 Docker 部署；阿里云新加坡仅为维护者实例位置 | [01](wiki/01-overview.md#部署) |
| 模型与发信费用 | Key 由部署企业配置，费用由该企业承担 | [01](wiki/01-overview.md#模型服务与费用) |
| 项目资料 | `README.md`、`docs/architecture.mmd`、`docs/wiki/`（自包含的详细说明）、`docs/decisions.md`（本总览） | [Wiki](wiki/README.md#维护) |

### 数据

| 事项 | 选择 | 详见 |
| --- | --- | --- |
| 数据库 | PostgreSQL 18 + pgvector（`pgvector/pgvector:pg18`），主键 `uuidv7()` | [02](wiki/02-data-model.md#通用规则) |
| 消息列表 | 只有 Agent 拥有消息列表，通信消息与上下文共用 `messages` 表；不建 `agents`、`runs`、`events`、`compactions` 表 | [02](wiki/02-data-model.md) |
| 表 | `users`、`subagents`、`messages`、`steps`、`login_codes`、`sessions` | [02](wiki/02-data-model.md#表关系) |
| 固定取值字段 | `text` + `CHECK`，不用枚举 | [02](wiki/02-data-model.md#通用规则) |
| 排序与待读 | 全局序列 `sequence`，为空即待读，下次调用模型前补号 | [02](wiki/02-data-model.md#排序与待读规则) |
| 外键 | 一律 `ON DELETE CASCADE` | [02](wiki/02-data-model.md#外键) |
| 压缩 | 摘要作为 `is_summary` 消息追加，不删旧消息 | [02](wiki/02-data-model.md#压缩与上下文检索-仅设计) |
| 历史检索 | 关键词 + 向量混合搜索 | [02](wiki/02-data-model.md#压缩与上下文检索-仅设计) |
| embedding | `google/gemini-embedding-2`，1536 维，写死在代码中，异步计算，余弦距离 HNSW | [02](wiki/02-data-model.md#压缩与上下文检索-仅设计) |
| 迁移 | goose `v3.29.0`（`go tool`），单独命令执行 | [02](wiki/02-data-model.md#迁移) |
| sqlc | 单独安装；可空列用指针，UUID 用 `google/uuid`，`numeric` 用 `shopspring/decimal` | [02](wiki/02-data-model.md#代码生成sqlc) |

### 认证

| 事项 | 选择 | 详见 |
| --- | --- | --- |
| 登录方式 | 邮箱验证码，新邮箱验证成功后自动创建用户 | [03](wiki/03-auth.md) |
| 登录凭证 | 随机令牌 + `sessions` 表，不用 JWT；`Authorization: Bearer` | [03](wiki/03-auth.md#登录凭证) |
| 过期 | 滑动过期，30 天不活跃失效；允许多设备 | [03](wiki/03-auth.md#登录凭证) |
| 验证码 | 6 位、10 分钟、60 秒重发间隔、最多 5 次 | [03](wiki/03-auth.md#验证码) |
| 凭证存储 | 只存哈希 | [03](wiki/03-auth.md#验证码) |
| 邮件服务 | Resend，`net/http` 直接调用；dev 打印日志，prod 必须配置 | [03](wiki/03-auth.md#发信) |

### 消息与实时通信

| 事项 | 选择 | 详见 |
| --- | --- | --- |
| 整体结构 | 实例无状态、状态在 PostgreSQL；`LISTEN/NOTIFY` 互相唤醒，不引入 Redis | [04](wiki/04-messaging.md#整体结构) |
| 实时通道 | WebSocket（`coder/websocket`），第一条消息验证登录；登录与历史走 HTTP | [04](wiki/04-messaging.md#websocket) |
| 消息状态 | `sending` / `failed`（仅 App）、`delivered` / `read`（由 `sequence` 算出） | [04](wiki/04-messaging.md#消息状态) |
| 用户可见消息 | 不加列，在 Go 代码中解析 `send_message` | [04](wiki/04-messaging.md#用户能看到的消息) |
| 消息 ID | 数据库生成；`client_message_id` 只用于识别重发 | [04](wiki/04-messaging.md#消息-id-与重发) |
| 写入顺序 | 同一用户的插入先取 `pg_advisory_xact_lock` | [04](wiki/04-messaging.md#写入顺序) |
| 不合规则的消息 | 直接断开 WebSocket，不回错误消息 | [04](wiki/04-messaging.md#websocket) |
| 重连位置 | 推送的消息带 `position`（所在行 ID），重连时 `auth` 带 `last_position`；不用 `sequence` | [04](wiki/04-messaging.md#websocket) |

### Main 运行时

| 事项 | 选择 | 详见 |
| --- | --- | --- |
| 实现顺序 | 最小对话 → 边生成边显示 → 更多工具 → 压缩与混合搜索 → Subagent | [05](wiki/05-main-runtime.md#实现顺序) |
| 主对话 | 每用户一条长期主对话，同一份 `messageList` | [05](wiki/05-main-runtime.md) |
| 回复用户 | 只通过 `send_message`；禁止直接输出正文，模型违约正文也不对外展示 | [05](wiki/05-main-runtime.md#main-如何回复用户) |
| 处理中收到新消息 | 当前请求或工具调用结束后、下一次模型调用前加入 | [05](wiki/05-main-runtime.md#运行循环-已实现) |
| 按用户串行 | `pg_try_advisory_lock`，释放后唤醒调度；周期扫描补偿漏通知 | [05](wiki/05-main-runtime.md#运行循环-已实现) |
| 数据库连接池上限 | 环境变量 `DATABASE_MAX_CONNS`，默认 `20`；不放进 `DATABASE_URL`，以免 goose 迁移失败 | [05](wiki/05-main-runtime.md#运行循环-已实现) |
| Main 架构 | 2026-10-23 用户确认全面采用 B：Main 与 Subagent 共用执行工具，Main 自行决定直接执行或派发；消息与派发权限保持角色区分，原 A/B 作为历史记录 | [06](wiki/06-main-role-experiment.md) |
| Main 沟通方式 | 只通过工具行动和沟通，耗时任务以 `send_message` 汇报关键进展，完成后发送结果；禁止直接输出正文 | [05](wiki/05-main-runtime.md#main-如何回复用户) |
| Main 模型 | `minimax/minimax-m3.1-flash`，`MAIN_MODEL` 可改 | [05](wiki/05-main-runtime.md#模型) |
| Subagent 模型 | `SUBAGENT_MODEL` 独立配置，留空时使用 `MAIN_MODEL` 的实际值 | [05](wiki/05-main-runtime.md#模型) |
| 系统提示词 | 迁入 Aether 的中文调研与表达提示词；适配历史能力，保留只用工具沟通和最终结果走 `send_message` 的契约 | [05](wiki/05-main-runtime.md#系统提示词-已实现) |
| 模型调用失败 | 不向用户显示，记日志与 `steps.error`；临时错误重试 3 次 | [05](wiki/05-main-runtime.md#模型调用失败) |
| Subagent | 与 Main 只通过消息联系；状态由运行时维护；收到消息即唤醒；暂无强制中断 | [05](wiki/05-main-runtime.md#subagent-已实现) |
| 近期实现顺序 | Subagent 的联网搜索与网页读取 → WhatsApp 风格简版 Flutter → 本地联调；先补齐 Subagent 运行与通信链路 | [05](wiki/05-main-runtime.md#实现顺序) |
| Subagent 联网工具 | 沿用 Aether：`search(query, platform)` 使用 Serper 和 EveryInfra；`fetch_url(url)` 使用 Parallel 和 EveryInfra | [05](wiki/05-main-runtime.md#search-与-fetch_url-已实现) |
| 派发接口 | `assign_subagents({tasks:["任务说明", ...]})`，每项创建独立 Subagent，立即返回对应 ID | [05](wiki/05-main-runtime.md#assign_subagents-已实现) |
| Subagent 并发 | 每用户最多 3 个同时执行，超出任务保留在数据库中排队 | [05](wiki/05-main-runtime.md#subagent-已实现) |
| Subagent 超时 | 每次唤醒最多连续执行 15 分钟，超时标为 `failed` 并通知 Main，收到新消息可再唤醒 | [05](wiki/05-main-runtime.md#subagent-已实现) |

## 待确认问题

以下仅是待讨论事项，不构成已采用的方案；清单也不是完整的决策枚举。

- 文档语言（中文、英文或双语）。
- 企业管理员：身份与权限如何表示、本仓库后端为管理后台提供哪些接口、管理后台仓库的技术选型。
- 按用户的用量与费用控制（额度、频率限制、统计）。
- 多实例部署下的后台向量计算。
- 企业统一登录（SSO）等登录方式扩展。
- 压缩的触发时机与摘要写法。
- 混合搜索的细节：中文关键词搜索实现、两路结果的合并方式、哪些消息参与搜索、`input_type` 的使用；完整消息读取规则。
- 边生成边显示的推送方式，以及各平台的 WebSocket 连接恢复行为。
- 其余 HTTP 工具与 MCP 的接入方式和权限。
- 浏览器和代码隔离环境的实现、资源限制与生命周期。
- 六个平台的界面、任务状态展示、发布和验证方式。
- 通用 Docker 部署的具体形式；维护者阿里云新加坡实例的部署资源、数据库部署、域名、TLS 和 dev/prod 隔离方式。
- 项目各部分的验收标准及验证环境。
