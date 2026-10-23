# aether-personal

企业级的开源 personal agent 项目：任何企业都可以 fork、部署，去服务他们的用户。欢迎其他开发者参与。交付范围为用户提供的完整三层架构，覆盖 Flutter 六个平台、Go 后端、PostgreSQL、Agent 协作、外部工具与隔离执行，以及 dev/prod 环境。

当前开发版本包含邮箱验证码登录、Main 长期对话、后台 Subagent、联网搜索与网页读取，以及 Poke 联系人式的简版 Flutter 聊天。用户界面只显示与 Main 的消息流，Subagent 和工具过程留在后台。技术实现由开发者判断，产品决策先与用户确认。

## 已确认方向

| 项目 | 已确认内容 |
| --- | --- |
| 项目名称 | `aether-personal` |
| Flutter 平台 | Android、iOS、macOS、Windows、Linux、Web |
| 后端 | Go |
| 部署方式 | 项目提供通用 Docker 部署；阿里云新加坡仅为维护者自己实例的部署位置 |
| Go 版本 | `1.27.2` |
| Go 模块路径 | `github.com/OmniNetOpensource/aether-personal/backend` |
| HTTP 层 | Go 标准库 `net/http` |
| 数据库访问层 | `pgx v5.11.0 + sqlc v1.31.1`：手写 SQL，生成带类型的 Go 调用代码 |
| 代码托管 | GitHub，仓库 `OmniNetOpensource/aether-personal`，公开 |
| 许可协议 | [Apache-2.0](LICENSE) |
| 数据库 | PostgreSQL 18 + pgvector（镜像 `pgvector/pgvector:pg18`）；本地 dev 使用 Docker Compose |
| 模型服务 | OpenRouter；Main 默认 `minimax/minimax-m3.1-flash`，Subagent 默认跟随 Main，可分别配置 |
| 密钥与费用 | OpenRouter / Resend Key 由部署的企业配置，费用由该企业承担 |
| 登录 | 邮箱验证码 |
| 验证码邮件 | Resend |
| 首次登录 | 新邮箱验证成功后自动创建用户 |
| Main Agent | 每用户固定一个逻辑 Main，按用户串行运行，状态持久化到 PostgreSQL |
| 主对话上下文 | 每用户一条长期主对话，使用同一份 `messageList` |
| 处理中收到新消息 | 当前模型请求或工具调用结束后，在下一次模型调用前加入新消息 |
| 环境 | 实现时同时考虑 dev 和 prod |

## 完整交付范围

- Flutter Main 聊天界面，HTTP、WebSocket 和断线恢复。
- Go API：认证、收发消息、WebSocket。
- 消息服务：消息路由、持久化、保存后直接触发接收方。
- PostgreSQL：agent 身份、消息历史、Run 状态和执行轨迹；表结构见[数据模型](docs/wiki/02-data-model.md)。
- Main Runtime：`messageList` 上下文，按用户串行处理。
- Main 可直接执行或委派任务，与 Subagent 共用执行工具（当前为 `search`、`fetch_url`）；消息和派发权限按角色区分。
- Main 在耗时任务中通过 `send_message` 汇报关键进展，完成后发送结果，禁止直接输出正文。历史检索 `search_message`、`read_message` 尚待实现。
- 按任务创建 Subagent，独立上下文、后台执行、限额并行。
- Subagent 与 Main 互相 `send_message`；Subagent 失败时由运行时代发通知。
- 按用户权限搜索和读取历史消息。
- 通过 OpenRouter 调用模型。
- 搜索、HTTP API、MCP 等外部服务调用。
- 在隔离执行环境中运行浏览器与代码任务。
- dev/prod 环境配置及对应的验证。

上述范围不代表各项已经实现，当前状态见架构 Wiki。技术实现由开发者判断，影响用户体验和产品范围的决定先与用户确认。

## 仓库目录

采用同一仓库组织前端、后端、环境配置和文档：

| 目录 | 用途 |
| --- | --- |
| `backend/` | Go 后端 |
| `frontend/` | Flutter 六个平台共用工程 |
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

Flutter 工程位于 `frontend/`，当前以 Flutter Web 本地联调；其余平台的工程已生成，尚未构建验收。

## 本地开发

需要 Go `1.27.2`、Docker 和 Flutter `3.47.7`。Flutter 不在 PATH 时，根目录 Makefile 会使用 `$HOME/develop/flutter/bin/flutter`；也可用 `FLUTTER=/path/to/flutter/bin/flutter` 覆盖。

首次准备配置并填入服务密钥：

```sh
cp backend/.env.example backend/.env
# 编辑 backend/.env，填写 OPENROUTER_API_KEY 及联网工具密钥
make dev
```

打开 [本地聊天](http://127.0.0.1:8080)。`make dev` 构建 Flutter Web、启动数据库、执行迁移并运行 Go；Go 根据 `FRONTEND_DIR` 同源提供页面、HTTP API 和 WebSocket。本地 dev 验证码只打印在运行后端的终端中。

检查代码和真实数据库运行时：

```sh
make check
make check-integration
```

数据库集成测试创建和清理独立随机 schema，外部模型及工具接口使用本地 HTTP 响应。它验证队列、并发、重启和失败行为，不证明真实供应商可用。真实联网依赖各服务的有效密钥。

`make check-live` 显式调用真实搜索和网页服务，会消耗相应供应商额度；未配置密钥的供应商显示为跳过，不能视为验证成功。

当前全面采用 B：Main 与 Subagent 共用执行工具。`make check-research` 使用正式提示词和正常 Runner 重放真实研究任务，输出完整消息和执行证据供 review；该命令会调用真实服务并消耗额度。可通过 `RESEARCH_OUTPUT=/绝对路径` 指定结果目录。原 A/B 数据作为历史记录保留，见 [Main 职责与验证](docs/wiki/06-main-role-experiment.md)。

仅运行后端时仍可使用：

```sh
cd backend
cp .env.example .env
make db-up     # 启动本地 PostgreSQL，等到数据库可连接后才返回
make migrate   # 执行数据库迁移（goose，已作为 Go 工具依赖固定版本）
make run       # 加载 .env 运行后端
curl localhost:8080/healthz
make db-down   # 停止本地 PostgreSQL
```

| 环境变量 | 说明 |
| --- | --- |
| `APP_ENV` | 必填，`dev` 或 `prod`；决定日志格式（dev 文本，prod JSON） |
| `DATABASE_URL` | 必填，PostgreSQL 连接串 |
| `DATABASE_MAX_CONNS` | 默认 `20`，最小 `5`；运行时为普通请求预留 4 条连接，每个运行中的 Main 或 Subagent 占一条；另有一条独立 LISTEN 连接 |
| `OPENROUTER_API_KEY` | 必填，OpenRouter 的 API Key |
| `MAIN_MODEL` | 可选，Main 使用的模型，默认 `minimax/minimax-m3.1-flash` |
| `SUBAGENT_MODEL` | 可选，Subagent 使用的模型，留空时使用 `MAIN_MODEL` 的实际值 |
| `SERP_API_KEY` | Serper Google 搜索密钥 |
| `PARALLEL_API_KEY` | 普通网页及 Reddit 正文读取密钥 |
| `EVERYINFRA_API_KEY` | 微信公众号、小红书搜索及正文读取密钥 |
| `FRONTEND_DIR` | Flutter Web 构建目录；本地示例为 `../frontend/build/web`，相对后端进程工作目录 |
| `PORT` | 可选，HTTP 监听端口，默认 `8080` |
| `POSTGRES_PORT` | 可选，本地数据库映射到本机的端口，默认 `5432` |
| `RESEND_API_KEY` | `prod` 必填，Resend 的 API Key |
| `EMAIL_FROM` | `prod` 必填，验证码邮件发件人，如 `Aether <noreply@company.com>`；域名需先在 Resend 后台验证 |

本地登录（dev 不发邮件，验证码打印在后端日志的 `login code` 一行）：

```sh
curl -X POST localhost:8080/auth/code -d '{"email":"me@example.com"}'
curl -X POST localhost:8080/auth/verify -d '{"email":"me@example.com","code":"<日志中的验证码>"}'
# 返回 {"token":"..."}，之后的请求带上 Authorization: Bearer <token>
curl -X POST localhost:8080/auth/logout -H "Authorization: Bearer <token>"
```

## 项目资料

- [架构 Wiki](docs/wiki/README.md)：按主题完整说明架构与设计，包括每项决定的理由和各部分的实现状态。
- [架构图](docs/architecture.mmd)：Mermaid 格式的三层架构图。
- [决策总览](docs/decisions.md)：已确认选择的一览表及待确认问题。
