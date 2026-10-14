# 项目决策记录

记录日期：2026-10-14（Asia/Shanghai）。

## 项目定位

用户原话：

> 这是一个开源项目，目标是让每个人都能部署自己的 personal agent。未来也可能其他人开发。

因此，部署、配置和本地开发流程都要让陌生的部署者和贡献者能够上手，不能依赖维护者本机的环境或私有资源。

## 协作约束

- 任何微决策都必须先询问用户，得到答复后再执行。
- 交付范围是整张架构图的全部内容。
- 实现逻辑时同时考虑 dev 和 prod 环境。
- 变量名称必须直观，禁止使用抽象的变量名。

## 已确认选择

| 事项 | 用户确认 |
| --- | --- |
| 项目名称 | `aether-personal` |
| Flutter 目标平台 | Android + iOS + macOS + Windows + Linux + Web |
| 后端 | Go |
| 部署方式 | 项目提供通用 Docker 部署；阿里云新加坡仅为维护者自己实例的部署位置 |
| Go 版本 | `1.27.2` |
| Go 模块路径 | `github.com/OmniNetOpensource/aether-personal/backend` |
| HTTP 层 | Go 标准库 `net/http` |
| 数据库访问层 | `pgx v5.11.0 + sqlc v1.31.1`：手写 SQL，生成带类型的 Go 调用代码 |
| 代码托管 | GitHub |
| GitHub 仓库所有者 | `OmniNetOpensource`（个人账号） |
| 许可协议 | Apache-2.0 |
| GitHub 仓库名称 | `aether-personal` |
| GitHub 仓库可见性 | 公开 |
| 维护者提交邮箱 | 使用 GitHub noreply 邮箱，避免在公开仓库中暴露真实邮箱 |
| `.gitignore` | 忽略系统文件、编辑器目录、`.env` 类文件和 `/backend/bin/` |
| 仓库组织 | 同一仓库：`backend/`、`frontend/`、`infra/`、`docs/` |
| 数据库 | PostgreSQL 18 |
| 本地 dev 数据库 | Docker Compose |
| 后端配置来源 | 只读环境变量；dev 使用未提交的 `.env` 由启动命令加载，仓库提供 `.env.example` |
| 运行环境标识 | 必填环境变量 `APP_ENV`，只允许 `dev` / `prod`，其他值启动报错 |
| 健康检查 | `GET /healthz`，同时检查数据库连接 |
| 停止信号处理 | 收到停止信号直接退出 |
| 本地常用命令 | `backend/Makefile` |
| 监听端口 | 环境变量 `PORT`，默认 `8080` |
| 本地数据库端口 | compose 中为 `${POSTGRES_PORT:-5432}`，可在 `.env` 覆盖 |
| 日志 | `log/slog`；dev 输出文本，prod 输出 JSON |
| 模型服务 | OpenRouter |
| 具体模型 | 稍后确定 |
| OpenRouter / Resend Key | 每个部署者在自己的环境变量中配置 |
| 模型与发信费用 | 由各部署者自行承担 |
| 主对话上下文 | 每用户一条长期主对话，使用同一份 `messageList` |
| Main 处理中收到新消息 | 当前模型请求或工具调用结束后，在下一次模型调用前加入新消息 |
| 登录方式 | 邮箱验证码 |
| 邮件服务 | Resend |
| 新用户注册 | 新邮箱验证码验证成功后自动创建用户 |
| 项目资料保存 | `README.md`、`docs/architecture.mmd`、`docs/decisions.md` |

## Main Agent 的语义澄清

用户原话：

> 自动创建用户。main agent只是一个逻辑概念。

因此，“每用户固定一个 Main Agent”表示用户与 Main Agent 的逻辑关系。不能据此预设每用户拥有常驻进程、独立容器或独立服务。

注册时创建哪些数据、Main 身份如何表示、Runtime 如何启动与恢复，均待用户确认。原始架构图不因这项澄清自动改动图中的节点或连线。架构图仅按用户确认，将第二层标题由“Go 后端 · 阿里云新加坡”改为“Go 后端”，以匹配通用部署定位。

## 工程组织与组件选择

- `backend/`：Go 后端。
- `frontend/`：Flutter 六个平台共用工程。
- `infra/`：dev/prod 环境和部署配置。
- `docs/`：设计文档与决策记录。

Go 版本为 `1.27.2`；较低版本的 Go 会按 `go.mod` 自动下载对应工具链。

HTTP 层已选择 Go 标准库 `net/http`。

数据库访问层已选择 `pgx + sqlc`：手写 SQL，使用 sqlc 生成带类型的 Go 调用代码，由 pgx 执行数据库操作。已固定 pgx `v5.11.0`、sqlc `v1.31.1`。

后端内部目录已确认并创建：

- `backend/cmd/server/`：程序入口。
- `backend/internal/config/`：环境配置。
- `backend/internal/httpapi/`：API。
- `backend/internal/database/`：sqlc 生成代码。
- `backend/db/migrations/`：表结构迁移。
- `backend/db/queries/`：手写 SQL。

`database/`、`migrations/`、`queries/` 目录当前为空，Git 不单独记录空目录；具体接口、数据字段和运行规则继续分别确认。

## 后端最小骨架

- `internal/config` 启动时一次性读取环境变量：`APP_ENV` 与 `DATABASE_URL` 必填，`PORT` 默认 `8080`，不满足即报错退出。
- `GET /healthz` 每次请求 ping 数据库，正常返回 `200 ok`，失败返回 `503`。
- 后端启动时不检查数据库连通性，避免数据库晚于后端就绪时出现崩溃重启循环；数据库状态由 `/healthz` 反映。
- 等待数据库就绪由编排层负责：dev compose 使用 `pg_isready` 健康检查，`make db-up` 带 `--wait`，数据库可连接后才返回；将来的 Docker 部署中，后端使用 `depends_on: condition: service_healthy` 等待数据库。
- dev compose 固定项目名 `aether-personal-dev`，避免与其他目录名为 `dev` 的 compose 项目冲突。
- `.env` 位于 `backend/`，由 Makefile 加载并传给 compose；仓库提供 `backend/.env.example`。
- 已验证：数据库正常时 `/healthz` 返回 200，停止数据库后返回 503，缺少 `APP_ENV` 时启动报错退出。

当前尚无数据库表；sqlc 生成配置、SQL 查询及实际代码生成待确认。

## GitHub 托管

代码托管在 GitHub 个人账号 `OmniNetOpensource` 下的公开仓库 `aether-personal`，Go 模块路径相应为 `github.com/OmniNetOpensource/aether-personal/backend`。

代码仓库托管位置与应用运行位置是独立事项：维护者自己的实例部署在阿里云新加坡。

发布前已从文档和提交历史中移除维护者本机路径。

## 主对话与新消息处理

每个用户只有一条长期主对话，使用同一份 `messageList`。

Main 处理期间收到用户新消息时，当前模型请求或工具调用继续执行；待该请求或调用结束，在下一次模型调用前将新消息加入上下文。具体消息排序、工具调用记录的完整性以及并发入库规则仍待确认。

## 模型服务与费用

项目开源、供任何人自行部署，因此 OpenRouter 与 Resend 的 Key 由每个部署者在自己的环境变量中配置，费用自理。Main 与 Subagent 的具体模型稍后确定。

## 原始架构中已给出的约束

- 消息保存后直接触发接收方。
- Main 使用 `messageList` 上下文，按用户串行处理。
- Main 提供 `assign_subagents`、`send_message`、`search_message`、`read_message` 四个工具。
- `assign_subagents` 记录任务后异步启动 Subagent，立即返回 `task_id`。
- Subagent 按任务创建，使用独立上下文，后台执行，限额并行。
- Subagent 接收补充消息，并以 `task_id` 关联进度、完成和失败回报。
- 历史消息搜索和读取受用户权限及消息归属限制。
- 保存消息、Run 状态及执行轨迹。
- 浏览器和代码任务在隔离执行环境中执行。
- Flutter 通过 HTTP/SSE 与后端交互，支持断线恢复。

## 待确认问题

以下仅是待讨论事项，不构成已采用的方案；清单也不是完整的决策枚举。

- Flutter 包标识、各平台应用标识和前端代码组织；后端目录内的具体实现组织。
- sqlc 生成配置；Flutter 版本；其余后端和前端依赖选择。
- 文档语言（中文、英文或双语）。
- Main 与 Subagent 的具体模型及模型配置入口。
- 用户数据与 Main 逻辑身份的表示方法。
- 验证码格式、有效期、重发间隔、尝试次数和 dev/prod 发信行为。
- 登录会话的有效期、存储方式、多设备行为和退出规则。
- Resend 发信域名及发件人。
- 消息、任务、Run 和执行轨迹的数据结构及关联关系。
- 新消息加入 Main 上下文时的消息排序、工具调用记录完整性和并发入库规则。
- Subagent 并发上限、补充消息处理、取消、超时、失败和恢复规则。
- 上下文增长、历史搜索和完整消息读取规则。
- 消息持久化与触发执行之间的故障恢复规则。
- SSE 事件类型、事件保存、断线续传和各平台的连接恢复行为。
- 搜索服务、HTTP 工具、MCP 接入方式及执行权限。
- 浏览器和代码隔离环境的实现、资源限制与生命周期。
- 六个平台的界面、任务状态展示、发布和验证方式。
- 通用 Docker 部署的具体形式；维护者阿里云新加坡实例的部署资源、数据库部署、域名、TLS 和 dev/prod 隔离方式。
- 项目各部分的验收标准及验证环境。
