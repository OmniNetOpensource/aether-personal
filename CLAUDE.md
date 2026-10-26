# aether-personal

企业级开源 personal agent：任何企业都可以 fork、部署，去服务他们的用户。架构图见 `docs/architecture.mmd`；架构与设计的完整说明（含每项决定的理由）见 `docs/wiki/`，已确认选择的一览表与待确认问题见 `docs/decisions.md`。动手前先读相关页面。

## 协作约束

- 仅产品决策询问用户，一次讨论一个问题。命名、依赖、表结构、接口形状等技术实现由代理判断，不逐项询问；这是用户在 2026-10-22 明确更新的协作方式。
- 用户确认的决定写入 `docs/wiki/` 对应主题页，写明选择与理由，再更新 `docs/decisions.md` 的一览表（必要时同步 README）。
- 解释时说清楚背后的知识点，不要只给结论。
- 推送到 GitHub（公开仓库）前需用户同意。

## 编码原则

- 生产代码的简洁性、可维护性与易于理解，永远排在第一位。
- 尽可能少的抽象。只有会被两个及以上地方使用时才抽出函数或组件。
- 防御只发生在系统边界：后端 API 的请求、用户输入、外部服务响应。内部模块之间建立在高度互信的契约之上，不做重复校验。
- 变量名直观，禁止抽象命名。
- 注释只写代码本身表达不了的约束。
- 禁止写任何测试文件。

## 技术栈

- 后端：Go `1.27.2`、标准库 `net/http`、`log/slog`、pgx v5（`pgxpool`）。
- 数据库：PostgreSQL 18 + pgvector，主键 `uuidv7()`。
- 迁移：goose（`go tool goose`，版本固定在 `go.mod`）。
- 数据库访问：sqlc `v1.31.1`（单独安装），手写 SQL 在 `backend/db/queries/`，生成代码在 `backend/internal/database/` 并提交进仓库，不要手改。
- 模型：OpenRouter；邮件：Resend（直接用 `net/http` 调用）。
- 前端：Lynx + ReactLynx + TypeScript，工程位于 `frontend/`，本地先验收 Web；Linux 使用浏览器。

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

- 启动配置只来自环境变量；管理员在看板选择的运行时模型配置持久化到数据库，并覆盖模型环境变量默认值；`APP_ENV` 必须为 `dev` 或 `prod`，无默认值。`prod` 必需的配置缺失时启动即报错退出。
- 迁移文件按 `00001_...sql` 顺序编号，包含 Up 与 Down；新增迁移后在本地验证执行、回滚、再执行。
- 表结构：固定取值的字段用 `text` + `CHECK`，不用 enum；外键一律 `ON DELETE CASCADE`。
- sqlc 生成类型：不能为空的列用普通类型，能为空的列加 `*`（`nil` 表示空）；UUID 用 `google/uuid`，时间用 `time.Time`，`numeric` 用 `shopspring/decimal`。
- 验证码、令牌等凭证在数据库中只存哈希值。
- 提交信息使用 Conventional Commits（`feat(backend): ...`、`docs: ...`、`chore: ...`）。
- 提交时采用小规模、自包含的 commit：一次提交只包含一个能独立成立的改动。
