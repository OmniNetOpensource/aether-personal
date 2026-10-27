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
| 管理后台 | 2026-10-26 纳入本仓库 `admin/`，独立运行与部署；保留运行概览、模型调用、模型配置，调用详情查看实际请求与响应 JSON，开发中 | [08](wiki/08-admin-observability.md) |
| Main Agent | 逻辑概念，不对应常驻进程或容器 | [01](wiki/01-overview.md#main-是逻辑概念) |
| 任务 | 虚拟概念，实体是 Subagent，统一称 `subagent_id` | [01](wiki/01-overview.md#任务是虚拟概念实体是-subagent) |
| 仓库组织 | 同一仓库：`backend/`、`frontend/`、`admin/`、`infra/`、`docs/` | [01](wiki/01-overview.md#工程组织) |
| 前端框架与平台 | 2026-10-27 改为 React + React DOM + TypeScript 的纯 Web 应用，桌面与移动共用 | [01](wiki/01-overview.md#第一层web-与-pwa-前端) |
| 移动端交付 | 2026-10-27 改为 PWA；安装到主屏幕、离线应用外壳与显式更新，取代原生宿主；2026-10-28 删除页面安装按钮，使用浏览器自带安装入口 | [01](wiki/01-overview.md#第一层web-与-pwa-前端) |
| 客户端界面与验收 | React Web 保留现有 Poke 式 IM：全屏消息列表与输入框，无联系人栏、菜单或外层卡片；2026-10-28 删除整个顶部栏；先验收 Web | [01](wiki/01-overview.md#第一层web-与-pwa-前端) |
| 前端构建 | Node.js + npm 锁文件，Rsbuild 输出 Web/PWA 至 frontend/build/web；无需原生 SDK | [01](wiki/01-overview.md#第一层web-与-pwa-前端) |
| 客户端显示名称 | 中文名「阿芒」，英文名 Aether；npm 工程名 aether-personal | [01](wiki/01-overview.md#第一层web-与-pwa-前端) |
| 产品图标 | 2026-10-26 用户提供的深绿背景黄色毛绒角色原图；用于客户端登录、Web 图标和管理看板品牌标识 | [01](wiki/01-overview.md#第一层web-与-pwa-前端) |
| 基础配色 | 2026-10-26 随产品图标统一为森林绿、奶油白与柔黄；覆盖聊天客户端及管理看板 | [01](wiki/01-overview.md#第一层web-与-pwa-前端) |
| 本地 Web 接入 | Go 用 `FRONTEND_DIR` 提供构建产物，与 HTTP API、WebSocket 同源；根目录 `make dev` 启动 | [01](wiki/01-overview.md#本地开发) |
| 后端 | Go `1.27.2`，标准库 `net/http`，`log/slog` | [01](wiki/01-overview.md#技术栈) |
| Go 模块路径 | `github.com/OmniNetOpensource/aether-personal/backend` | [01](wiki/01-overview.md#技术栈) |
| 数据库访问层 | `pgx v5.11.0 + sqlc v1.31.1` | [01](wiki/01-overview.md#技术栈) |
| 配置来源 | 启动配置读取环境变量，运行时模型选择可由管理员保存到数据库；`APP_ENV` 必填，只允许 `dev` / `prod` | [01](wiki/01-overview.md#配置) |
| 健康检查 | `GET /healthz`，同时检查数据库连接；启动时不检查数据库 | [01](wiki/01-overview.md#后端骨架-已实现) |
| 监听端口 | `PORT`，默认 `8080` | [01](wiki/01-overview.md#配置) |
| 本地 dev 数据库 | Docker Compose，端口 `${POSTGRES_PORT:-5432}` | [01](wiki/01-overview.md#后端骨架-已实现) |
| 本地常用命令 | `backend/Makefile` | [01](wiki/01-overview.md#后端骨架-已实现) |
| 代码托管 | GitHub `OmniNetOpensource/aether-personal`，公开，Apache-2.0，提交使用 noreply 邮箱 | [01](wiki/01-overview.md#代码托管) |
| 开发分支 | 2026-10-27 用户要求默认直接在最新 `main` 开发，不自行创建独立开发分支；公开仓库推送仍需授权 | [01](wiki/01-overview.md#代码托管) |
| 部署方式 | 通用 Docker Compose；维护者实例使用 Cloudflare Workers Static Assets 托管 Web，Go 与数据库在阿里云新加坡，当前沙盒暂停接入，仅上线联网执行工具；生产部署待完成 | [09](wiki/09-deployment.md) |
| 维护者 Web 域名 | 2026-10-26 确认 `aether.forkicks.fun`，公众开放注册 | [09](wiki/09-deployment.md) |
| 模型与发信费用 | Key 由部署企业配置，费用由该企业承担 | [01](wiki/01-overview.md#模型服务与费用) |
| 项目资料 | `README.md`、`docs/architecture.mmd`、`docs/wiki/`（自包含的详细说明）、`docs/decisions.md`（本总览） | [Wiki](wiki/README.md#维护) |

### 数据

| 事项 | 选择 | 详见 |
| --- | --- | --- |
| 数据库 | PostgreSQL 18 + pgvector（`pgvector/pgvector:pg18`），主键 `uuidv7()` | [02](wiki/02-data-model.md#通用规则) |
| 消息列表 | 只有 Agent 拥有消息列表，通信消息与上下文共用 `messages` 表；不建 `agents`、`runs`、`events`、`compactions` 表 | [02](wiki/02-data-model.md) |
| 表 | `users`、`subagents`、`messages`、`steps`、`login_codes`、`sessions`，开发中新增 `user_sandboxes`、`sandbox_operations`、`model_calls`、`recap_state` | [02](wiki/02-data-model.md#表关系) |
| 固定取值字段 | `text` + `CHECK`，不用枚举 | [02](wiki/02-data-model.md#通用规则) |
| 排序与待读 | 全局序列 `sequence`，为空即待读，下次调用模型前补号 | [02](wiki/02-data-model.md#排序与待读规则) |
| 外键 | 一律 `ON DELETE CASCADE` | [02](wiki/02-data-model.md#外键) |
| 压缩 | 摘要作为 `is_summary` 消息追加，不删旧消息 | [02](wiki/02-data-model.md#压缩与上下文检索) |
| 自动压缩触发 | 回答后超过 6 小时或上下文达到 200,000 tokens；两个阈值在看板配置，下一次有任务、调用模型前检查并压缩；开发中，本地已验证并启用 | [02](wiki/02-data-model.md#自动压缩触发2026-10-26开发中) |
| 历史检索 | 关键词 + 向量混合搜索 | [02](wiki/02-data-model.md#压缩与上下文检索) |
| embedding | `google/gemini-embedding-2`，1536 维，写死在代码中，异步计算，余弦距离 HNSW | [02](wiki/02-data-model.md#压缩与上下文检索) |
| 迁移 | goose `v3.29.0`（`go tool`），单独命令执行 | [02](wiki/02-data-model.md#迁移) |
| sqlc | 单独安装；可空列用指针，UUID 用 `google/uuid`，`numeric` 用 `shopspring/decimal` | [02](wiki/02-data-model.md#代码生成sqlc) |

### 认证

| 事项 | 选择 | 详见 |
| --- | --- | --- |
| 本地开发登录 | 2026-10-28 确认 dev 默认自动使用固定 mock 用户 `dev@example.com`，无需点击登录、输入邮箱或验证码；prod 不开放 | [03](wiki/03-auth.md#本地开发登录) |
| 登录方式 | 邮箱验证码，新邮箱验证成功后自动创建用户 | [03](wiki/03-auth.md) |
| 首版注册范围 | 2026-10-26 确认向公众开放注册，不要求邀请码或人工审核；生产防滥用与用量控制待完成 | [03](wiki/03-auth.md#生产注册范围) |
| 看板登录 | 仅输入独立管理员密码，无邮箱和验证码；使用独立管理会话，聊天账号不能访问管理接口 | [08](wiki/08-admin-observability.md#权限与-api) |
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
| Main 思考提示 | 聊天底部三点小气泡，按真实处理状态显示，结束、失败或断线后收起；开发中 | [04](wiki/04-messaging.md#main-思考气泡-开发中) |
| 运行动态摘要 | 2026-10-26 确认增加额外 Agent 汇总 Main 与 Subagent 的行动；有工作期间每 20 秒或累计 4 次模型请求触发 recap，先到先触发；聊天底部以更小、更淡的无气泡文字更新摘要，开发中 | [05](wiki/05-main-runtime.md#行动摘要-agent开发中) |
| recap 历史保留 | 2026-10-26 要求 recap 随消息持久保留，继续使用小号淡色样式；需求已确认，时间线持久化尚未实现 | [04](wiki/04-messaging.md#recap-随聊天历史保留已确认需求尚未实现) |
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
| Schedule 职责 | 2026-10-27 确认 Agent 通过工具布置一次性和周期安排，到期后将任务上下文发给该用户的 Main，复用现有执行流程；开发中 | [10](wiki/10-schedules.md) |
| 回复用户 | 只通过 `send_message`；禁止直接输出正文，模型违约正文也不对外展示 | [05](wiki/05-main-runtime.md#main-如何回复用户) |
| 处理中收到新消息 | 当前请求或工具调用结束后、下一次模型调用前加入 | [05](wiki/05-main-runtime.md#运行循环-已实现) |
| 按用户串行 | `pg_try_advisory_lock`，释放后唤醒调度；周期扫描补偿漏通知 | [05](wiki/05-main-runtime.md#运行循环-已实现) |
| 数据库连接池上限 | 环境变量 `DATABASE_MAX_CONNS`，默认 `20`；不放进 `DATABASE_URL`，以免 goose 迁移失败 | [05](wiki/05-main-runtime.md#运行循环-已实现) |
| Main 架构 | 2026-10-23 用户确认全面采用 B：Main 与 Subagent 共用执行工具，Main 自行决定直接执行或派发；消息与派发权限保持角色区分，原 A/B 作为历史记录 | [06](wiki/06-main-role-experiment.md) |
| Main 沟通方式 | 只通过工具行动和沟通，耗时任务以 `send_message` 汇报关键进展，完成后发送结果；禁止直接输出正文 | [05](wiki/05-main-runtime.md#main-如何回复用户) |
| Main 模型 | 初始默认 `deepseek/deepseek-v4.1-flash-1017`；管理员可在看板切换并持久化，覆盖 `MAIN_MODEL` 默认值 | [05](wiki/05-main-runtime.md#模型) |
| Subagent 模型 | `SUBAGENT_MODEL` 提供初始默认；管理端可独立切换或跟随 Main，保存后重启仍保留 | [05](wiki/05-main-runtime.md#模型) |
| 系统提示词 | 迁入 Aether 的中文调研与表达提示词；适配历史能力，保留只用工具沟通和最终结果走 `send_message` 的契约 | [05](wiki/05-main-runtime.md#系统提示词-已实现) |
| 模型调用失败 | 不向用户显示，记日志与 `steps.error`；临时错误重试 3 次 | [05](wiki/05-main-runtime.md#模型调用失败) |
| Subagent | 与 Main 只通过消息联系；状态由运行时维护；收到消息即唤醒；暂无强制中断 | [05](wiki/05-main-runtime.md#subagent-已实现) |
| 近期实现顺序 | 已完成 Subagent 联网与主对话；当前将客户端迁移为纯 Web/PWA，保留界面与通信行为 | [05](wiki/05-main-runtime.md#实现顺序) |
| Subagent 联网工具 | 沿用 Aether：`search(query, platform)` 使用 Serper 和 EveryInfra；`fetch_url(url)` 使用 Parallel 和 EveryInfra | [05](wiki/05-main-runtime.md#search-与-fetch_url-已实现) |
| 派发接口 | `assign_subagents({tasks:["任务说明", ...]})`，每项创建独立 Subagent，立即返回对应 ID | [05](wiki/05-main-runtime.md#assign_subagents-已实现) |
| Subagent 并发 | 每用户最多 3 个同时执行，超出任务保留在数据库中排队 | [05](wiki/05-main-runtime.md#subagent-已实现) |
| Subagent 超时 | 每次唤醒最多连续执行 15 分钟，超时标为 `failed` 并通知 Main，收到新消息可再唤醒 | [05](wiki/05-main-runtime.md#subagent-已实现) |
| 当前执行能力 | 2026-10-27 暂时停用全部沙箱功能，只保留搜索与网页读取；隐藏桌面入口，忽略旧沙箱配置，保留代码以便后续恢复 | [07](wiki/07-sandbox.md#当前状态暂时停用2026-10-27) |
| 沙盒能力范围 | 完整 Linux 工作电脑，包含终端、代码、文件、软件安装与使用、浏览器、图形桌面和后台进程；Main 与 Subagent 共用执行工具，开发中，本地链路已接通，生产隔离与检查点待验收 | [07](wiki/07-sandbox.md#能力范围-开发中) |
| 沙盒持久化与暂停 | 工作环境长期保留，空闲时暂停，下次自动恢复继续使用；文件、已安装软件、用户配置和浏览器状态不随任务结束清空，开发中，本地链路已接通，生产隔离与检查点待验收 | [07](wiki/07-sandbox.md#持久化与暂停-开发中) |
| 沙盒归属与首批功能 | 每用户一个独立持久的 Linux 沙盒，Main 与该用户 Subagent 共用；首批优先浏览网页、处理文件和运行代码，开发中，本地链路已接通，生产隔离与检查点待验收 | [07](wiki/07-sandbox.md#每用户独立环境与首批功能-开发中) |
| 维护者云沙盒 | 2026-10-27 验证阿里云二代后决定暂时停用，当前不购买云沙箱；保留后续按需创建、应用判断空闲后暂停，恢复同一环境；暂停默认保留 7 天，长期数据仍需外部备份 | [07](wiki/07-sandbox.md#阿里云二代运行时2026-10-27) |
| 沙盒实时观看 | 2026-10-26 确认入口放在聊天页面，用户只看自己的桌面；Web 首版只读，观看不创建或恢复环境，本地已验收 | [07](wiki/07-sandbox.md#聊天页面实时桌面2026-10-26) |

## 待确认问题

以下仅是待讨论事项，不构成已采用的方案；清单也不是完整的决策枚举。

- 文档语言（中文、英文或双语）。
- 企业管理员的多角色与个人身份管理；当前看板使用共享管理员密码，技术与接口见 [08](wiki/08-admin-observability.md)。
- 按用户的用量与费用控制（额度、频率限制、统计）。
- Schedule 的固定间隔安排与静默监控完成协议，见 [10](wiki/10-schedules.md#漏跑暂停和删除)。
- 多实例部署下的后台向量计算。
- 企业统一登录（SSO）等登录方式扩展。
- 混合搜索的细节：中文关键词搜索实现、两路结果的合并方式、哪些消息参与搜索、`input_type` 的使用；完整消息读取规则。
- 边生成边显示的推送方式，以及各平台的 WebSocket 连接恢复行为。
- 其余 HTTP 工具与 MCP 的接入方式和权限。
- Android、iOS、macOS、Windows 原生宿主的发布和验收方式（Linux 已确认仅要求浏览器）。
- 维护者阿里云新加坡实例的实际资源与连接信息、生产部署验收；实现配置见 [09](wiki/09-deployment.md)。
- 项目各部分的验收标准及验证环境。
