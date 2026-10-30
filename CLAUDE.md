# aether-personal

以维护者自用为主的开源 personal agent：首要目标是维护者自己长期使用，其他人也可以自行部署，或使用维护者向公众开放的实例。架构图见 `docs/architecture.mmd`；架构与设计的完整说明（含每项决定的理由）见 `docs/wiki/`，已确认选择的一览表与待确认问题见 `docs/decisions.md`。动手前先读相关页面。

## 协作约束

- 默认直接在 `main` 分支开发，不自行创建独立开发分支；每次开始前先获取远端最新 `main` 并同步。用户另有明确要求时按用户要求执行。
- 仅产品决策询问用户，一次讨论一个问题。命名、依赖、表结构、接口形状等技术实现由代理判断，不逐项询问；这是用户在 2026-10-22 明确更新的协作方式。
- 用户没有明确说改，就不要改动代码或文档：先调查、核对是否符合预期并汇报结论与建议，等用户明确说改后再动手。这是用户在 2026-10-29 明确更新的协作方式。
- 用户确认的决定写入 `docs/wiki/` 对应主题页，写明选择与理由，再更新 `docs/decisions.md` 的一览表（必要时同步 README）。
- 解释时说清楚背后的知识点，不要只给结论。
- 推送到 GitHub（公开仓库）前需用户同意。

## 编码原则

- 生产代码的简洁性、可维护性与易于理解，永远排在第一位。
- 尽可能少的抽象。只有会被两个及以上地方使用时才抽出函数或组件。
- 防御只发生在系统边界：后端 API 的请求、用户输入、外部服务响应。内部模块之间建立在高度互信的契约之上，不做重复校验。
- 变量名直观，禁止抽象命名。
- 每个小模块前写注释：文件、类型、函数／方法、组件，以及函数内可独立理解的一段逻辑，开头都用一两句说明它负责什么、为什么存在，方便人类顺着注释逐个模块阅读。模块标题注释不逐行复述代码；模块内部的其他注释仍只写代码本身表达不了的约束。
- 每个手写源文件的第一行就是文件头注释：一两句说清这个文件负责什么，让人不用读代码就知道该不该打开它；修改文件时若其职责变了，同步更新文件头。范围只含手写源文件（Go 不含 sqlc 生成代码、TS/TSX/JS/CSS、手写 SQL 查询与脚本）；生成代码、历史迁移与 JSON 等不支持或不应手改的文件不加。Go 的文件头与 `package` 之间留空行，避免被当成包文档注释。
- 禁止写任何测试文件。

## 技术栈

- 后端：Go `1.27.2`、标准库 `net/http`、`log/slog`、pgx v5（`pgxpool`）。
- 数据库：PostgreSQL 18 + pgvector，主键 `uuidv7()`。
- 迁移：goose（`go tool goose`，版本固定在 `go.mod`）。
- 数据库访问：sqlc `v1.31.1`（单独安装），手写 SQL 在 `backend/db/queries/`，生成代码在 `backend/internal/database/` 并提交进仓库，不要手改。
- 模型：OpenRouter 与 ikun Gemini 原生协议；邮件：Resend（直接用 `net/http` 调用）。
- 前端：React + React DOM + TypeScript，纯 Web 应用，移动端使用 PWA；工程位于 `frontend/`，构建输出 `frontend/build/web`。

## 常用命令（在 `backend/` 下）

```sh
cp .env.example .env   # 首次
make db-up             # 启动本地 PostgreSQL（等到可连接才返回）
make migrate           # 执行迁移
make run               # 运行后端；dev 下登录验证码打印在日志中
make db-down
sqlc generate          # 修改 db/queries 后重新生成
go build ./... && go vet ./...
```

## 约定

- 基础设施启动配置只来自环境变量；管理员在看板选择的模型和服务凭证持久化到数据库，服务 Key 不回退环境变量。`SERVICE_ENCRYPTION_KEY` 由部署端提供并单独备份；`APP_ENV` 必须为 `dev` 或 `prod`，无默认值。缺少服务 Key 时允许进入看板补齐，调用该服务时报未配置。
- 迁移文件按 `00001_...sql` 顺序编号，包含 Up 与 Down；新增迁移后在本地验证执行、回滚、再执行。
- 表结构：固定取值的字段用 `text` + `CHECK`，不用 enum；外键一律 `ON DELETE CASCADE`。
- sqlc 生成类型：不能为空的列用普通类型，能为空的列加 `*`（`nil` 表示空）；UUID 用 `google/uuid`，时间用 `time.Time`，`numeric` 用 `shopspring/decimal`。
- 验证码和登录令牌在数据库中只存哈希值；第三方 API Key 必须还原后发给供应商，因此使用 AES-GCM 加密保存，完整 Key 不回显，主加密密钥与数据库分开保存。
- 提交信息使用 Conventional Commits（`feat(backend): ...`、`docs: ...`、`chore: ...`）。
- 提交时采用小规模、自包含的 commit：一次提交只包含一个能独立成立的改动。
