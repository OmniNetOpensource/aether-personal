# aether-personal

以维护者自用为主的开源 personal agent：首要目标是维护者自己长期使用，其他人也可以自行部署，或使用维护者向公众开放的实例。架构图见 `docs/architecture.mmd`；架构与设计的完整说明（含每项决定的理由）见 `docs/wiki/`，已确认选择的一览表与待确认问题见 `docs/decisions.md`。动手前先读相关页面。

## 协作约束

- 默认直接在 `main` 分支开发，不自行创建独立开发分支；每次开始前先获取远端最新 `main` 并同步。用户另有明确要求时按用户要求执行。
- 产品决策询问用户，一次讨论一个问题。命名、依赖、表结构、接口形状等技术实现由代理判断，不逐项询问；代码修改仍须遵守下一条的事前确认要求。
- 修改代码之前，必须先完成调查，向用户说明调查结论、拟修改的内容与影响范围，并取得用户对该修改方案的明确确认后再动手。用户提出修改需求不等于已确认具体方案。已确认范围内的实现细节无需逐项询问，超出该范围时须重新确认。用户未明确要求修改文档时，也不要自行修改文档。这是用户在 2026-11-06 明确更新的协作方式。
- 用户确认的决定写入 `docs/wiki/` 对应主题页，写明选择与理由，再更新 `docs/decisions.md` 的一览表（必要时同步 README）。
- 维护者正通过本项目学习 Go 与后端知识。解释时说清楚背后的知识点，不要只给结论；涉及 Go 语言特性、标准库、并发、HTTP、数据库等概念时，结合本项目的具体代码讲清其作用与取舍，避免默认维护者已熟悉相关概念，也不要脱离当前任务展开无关教学。
- 推送到 GitHub（公开仓库）前需用户同意。

## 编码原则

- 生产代码的简洁性、可维护性与易于理解，永远排在第一位。
- 尽可能少的抽象。只有会被两个及以上地方使用时才抽出函数或组件。
- 不做防御性编码：不写重复校验、不可达分支的兜底和中间层的降级或自行恢复；每一层遇到错误直接向上抛（Go 里即 `return`，必要时只用 `%w` 补本层上下文），不在中间层吞错，由最外层统一记录并决定如何呈现。
- 变量名直观，禁止抽象命名。
- 每个小模块前写注释：文件、类型、函数／方法、组件，以及函数内可独立理解的一段逻辑，开头都用一两句说明它负责什么、为什么存在，方便人类顺着注释逐个模块阅读。模块标题注释不逐行复述代码；模块内部的其他注释仍只写代码本身表达不了的约束。
- 每个手写源文件的第一行就是文件头注释：一两句说清这个文件负责什么，让人不用读代码就知道该不该打开它；修改文件时若其职责变了，同步更新文件头。范围只含手写源文件（Go 不含 sqlc 生成代码、TS/TSX/JS/CSS、手写 SQL 查询与脚本）；生成代码、历史迁移与 JSON 等不支持或不应手改的文件不加。Go 的文件头与 `package` 之间留空行，避免被当成包文档注释。
- 不做任何本地测试类的事：不写测试文件，不运行测试、端到端验证或临时验证脚本，也不为验证去起临时数据库或后端进程。编译与静态检查（`go build`、`go vet`、前端类型检查）不算测试，照常运行。

## Agent loop 三阶段

核心是 `backend/internal/agent/` 的 agent loop，每一轮依次走三个阶段，说明见 `docs/wiki/05-main-runtime.md` 的「Agent loop 三阶段」：

- 请求前 `before_request.go`：整理状态并产出请求——清理上次进程退出的残留、补跑缺回执的工具、开步骤、读设置、必要时压缩、组装请求。
- 请求 `request.go`：发出请求并收回完整回复——调用日志、思考中被新输入取消、首段输出占位。
- 请求后 `after_request.go`：处理本轮结局——保存回复并执行工具、被新输入取代、临时故障重试、失败收尾；前两个阶段的错误也交给它。
- 循环主体 `runner.go` 只判断还有没有活（含收尾任务提醒），按顺序调用三个阶段，不放业务逻辑。

修改 agent loop 前，先说明改动属于哪个阶段；业务改动只落在三个阶段内。阶段之间只通过 `modelRequest` 与 `chatResponse` 交接，不互相调用阶段入口（共用的函数除外，如压缩复用 `sendModelRequest`、请求前补跑工具复用 `executeToolCalls`）。修改循环主体本身须单独取得用户确认。

## 技术栈

- 后端：Go `1.27.2`、标准库 `net/http`、`log/slog`、pgx v5（`pgxpool`）、`redis/go-redis` v9。
- 数据库与信号协调：PostgreSQL 18 + pgvector（保存全部持久状态与租约栅栏代数，主键 `uuidv7()`）；Redis 8（纯内存无持久化，传递跨实例唤醒队列、用户临时租约、思考打断与聊天推送广播，并以 `oss://` 为键共享图片旁路缓存）。
- 迁移：goose（`go tool goose`，版本固定在 `go.mod`）。
- 数据库访问：sqlc `v1.31.1`（单独安装），手写 SQL 在 `backend/db/queries/`，生成代码在 `backend/internal/database/` 并提交进仓库，不要手改。
- 模型：OpenRouter、ikun Gemini 原生协议与 ikun Claude 原生 Messages 协议（支持独立上下文压缩模型）；邮件：Resend（直接用 `net/http` 调用）；存储与沙箱：阿里云私有 OSS（配 Redis 共享图片缓存）、阿里云 FC Agent Sandbox + AgenticFS 持久工作区。
- 前端：React + React DOM + TypeScript，纯 Web 应用，移动端使用 PWA；工程位于 `frontend/`，构建输出 `frontend/build/web`。

## 常用命令（在 `backend/` 下）

```sh
cp .env.example .env   # 首次
make db-up             # 启动本地 PostgreSQL 与纯内存 Redis（等到可连接才返回）
make migrate           # 执行迁移
make run               # 运行后端；dev 下登录验证码打印在日志中
make db-down
sqlc generate          # 修改 db/queries 后重新生成
go build ./... && go vet ./...
```

## 排查生产

接到线上排查任务时，从仓库根目录调用 `python3 infra/prod/inspect-prod.py`。CLI 读取 `.local/publish.json`，复用发布用的公网地址和 SSH 配置。接入方式见[生产部署](docs/wiki/09-deployment.md#一键更新现有部署)。

- 用 `python3 infra/prod/inspect-prod.py health` 查公网 `/healthz` 的 HTTP 状态和正文。这个端点检查 PostgreSQL 和 Redis 的连通性。
- 用 `python3 infra/prod/inspect-prod.py status` 查生产容器状态、健康检查结果和镜像版本。输出为 JSON Lines。
- 用 `python3 infra/prod/inspect-prod.py logs --since 30m --tail 200` 查两个 Go API 容器的近期日志。用 `--service api-1` 或 `--service api-2` 选单个实例。按绝对时间查询时，给 `--since` 和 `--until` 传带时区的 RFC 3339 时间。
- 用 `python3 infra/prod/inspect-prod.py sql` 查持久状态。通过 stdin 传入一条返回结果集的 SQL 查询。PostgreSQL 在只读事务中执行，`psql` 输出带表头的 CSV，时间字段按 UTC 显示。按问题选择列和时间窗口，使用 `LIMIT` 控制结果量。

例如，按以下方式查最近的模型调用。

```sh
python3 infra/prod/inspect-prod.py sql <<'SQL'
SELECT id, user_id, step_id, requested_model, outcome, http_status,
       started_at, finished_at, duration_ms, output_tokens
FROM model_calls
WHERE started_at >= now() - interval '1 hour'
ORDER BY started_at DESC, id DESC
LIMIT 30;
SQL
```

在[生产管理看板](https://admin-aether.forkicks.fun)按用户和时间定位调用，用 `model_calls.id` 核对调用详情，再查看请求、响应和关联工具轨迹。到容器日志里找同一次故障时，用该记录的 `user_id` 和调用时间附近的窗口匹配。

## 约定

- 基础设施启动配置只来自环境变量；管理员在看板选择的模型和服务凭证持久化到数据库，服务 Key 不回退环境变量。`SERVICE_ENCRYPTION_KEY` 由部署端提供并单独备份；`APP_ENV` 必须为 `dev` 或 `prod`，无默认值。缺少服务 Key 时允许进入看板补齐，调用该服务时报未配置。
- 迁移文件按 `00001_...sql` 顺序编号，包含 Up 与 Down；新增迁移后不在本地执行、回滚或再执行来验证。
- 表结构：固定取值的字段用 `text` + `CHECK`，不用 enum；外键一律 `ON DELETE CASCADE`。
- sqlc 生成类型：不能为空的列用普通类型，能为空的列加 `*`（`nil` 表示空）；UUID 用 `google/uuid`，时间用 `time.Time`，`numeric` 用 `shopspring/decimal`。
- 验证码和登录令牌在数据库中只存哈希值；第三方 API Key 必须还原后发给供应商，因此使用 AES-GCM 加密保存，完整 Key 不回显，主加密密钥与数据库分开保存。
- 提交信息使用 Conventional Commits（`feat(backend): ...`、`docs: ...`、`chore: ...`）。
- 提交时采用小规模、自包含的 commit：一次提交只包含一个能独立成立的改动。
