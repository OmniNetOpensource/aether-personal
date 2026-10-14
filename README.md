# aether-personal

开源的个人 Agent 项目，目标是让每个人都能部署自己的 personal agent，并欢迎其他开发者参与。交付范围为用户提供的完整三层架构，覆盖 Flutter 六个平台、Go 后端、PostgreSQL、Agent 协作、外部工具与隔离执行，以及 dev/prod 环境。

当前已完成后端最小骨架：环境变量配置、`GET /healthz` 和本地 dev 数据库；业务功能仍待逐项确认和实现。任何微决策都须先询问用户，得到答复后再执行。

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
| 数据库 | PostgreSQL 18；本地 dev 使用 Docker Compose |
| 模型服务 | OpenRouter；具体模型待定 |
| 密钥与费用 | OpenRouter / Resend Key 由每个部署者自行配置，费用自理 |
| 登录 | 邮箱验证码 |
| 验证码邮件 | Resend |
| 首次登录 | 新邮箱验证成功后自动创建用户 |
| Main Agent | 每用户固定一个 Main Agent，属于逻辑概念；具体运行与持久化方式待确认 |
| 主对话上下文 | 每用户一条长期主对话，使用同一份 `messageList` |
| 处理中收到新消息 | 当前模型请求或工具调用结束后，在下一次模型调用前加入新消息 |
| 环境 | 实现时同时考虑 dev 和 prod |

## 完整交付范围

- Flutter 聊天与任务界面，HTTP、SSE 和断线恢复。
- Go API：认证、收发消息、SSE。
- 消息服务：消息路由、持久化、保存后直接触发接收方。
- PostgreSQL：agent 身份、消息历史、Run 状态和执行轨迹；具体数据结构待确认。
- Main Runtime：`messageList` 上下文，按用户串行处理。
- Main 的四个工具：`assign_subagents`、`send_message`、`search_message`、`read_message`。
- 按任务创建 Subagent，独立上下文、后台执行、限额并行。
- Subagent 补充消息，以及携带 `subagent_id` 的进度、完成、失败回报。
- 按用户权限搜索和读取历史消息。
- 通过 OpenRouter 调用模型。
- 搜索、HTTP API、MCP 等外部服务调用。
- 在隔离执行环境中运行浏览器与代码任务。
- dev/prod 环境配置及对应的验证。

上述范围不代表各项已经实现。技术版本、依赖、接口、数据结构、运行规则和部署细节仍需逐项确认。

## 仓库目录

采用同一仓库组织前端、后端、环境配置和文档：

| 目录 | 用途 |
| --- | --- |
| `backend/` | Go 后端 |
| `frontend/` | Flutter 六个平台共用工程 |
| `infra/` | dev/prod 环境和部署配置 |
| `docs/` | 设计文档与决策记录 |

后端内部目录组织：

| 后端目录 | 用途 |
| --- | --- |
| `backend/cmd/server/` | 程序入口 |
| `backend/internal/config/` | 环境配置 |
| `backend/internal/httpapi/` | API |
| `backend/internal/database/` | sqlc 生成代码 |
| `backend/db/migrations/` | 表结构迁移 |
| `backend/db/queries/` | 手写 SQL |

`backend/internal/database/`、`backend/db/migrations/`、`backend/db/queries/` 当前为空，Git 不单独记录空目录。数据库表、SQL 查询和 sqlc 生成配置尚未实现；Flutter 工程尚未初始化。

## 本地开发

需要 Go `1.27.2`（较低版本的 Go 会自动下载对应工具链）和 Docker。

```sh
cd backend
cp .env.example .env
make db-up     # 启动本地 PostgreSQL，等到数据库可连接后才返回
make run       # 加载 .env 运行后端
curl localhost:8080/healthz
make db-down   # 停止本地 PostgreSQL
```

| 环境变量 | 说明 |
| --- | --- |
| `APP_ENV` | 必填，`dev` 或 `prod`；决定日志格式（dev 文本，prod JSON） |
| `DATABASE_URL` | 必填，PostgreSQL 连接串 |
| `PORT` | 可选，HTTP 监听端口，默认 `8080` |
| `POSTGRES_PORT` | 可选，本地数据库映射到本机的端口，默认 `5432` |

## 项目资料

- [架构图](docs/architecture.mmd)：Mermaid 格式的三层架构图。
- [决策记录](docs/decisions.md)：已确认选择、语义澄清及待确认问题。
