# aether-personal

企业级开源 personal agent：任何企业都可以 fork、部署，去服务他们的用户。架构见 `docs/architecture.mmd`，所有已确认的决定及理由见 `docs/decisions.md`，动手前先读相关章节。

## 协作约束

- 任何微决策（命名、依赖、表结构、接口形状、默认值等）都必须先询问用户，得到答复后再执行。一次只讨论一个问题。
- 用户确认的决定记入 `docs/decisions.md`（必要时同步 README），写明选择与理由。
- 解释时说清楚背后的知识点，不要只给结论。
- 推送到 GitHub（公开仓库）前需用户同意。

## 编码原则

- 生产代码的简洁性、可维护性与易于理解，永远排在第一位。
- 尽可能少的抽象。只有会被两个及以上地方使用时才抽出函数或组件。
- 防御只发生在系统边界：后端 API 的请求、用户输入、外部服务响应。内部模块之间建立在高度互信的契约之上，不做重复校验。
- 变量名直观，禁止抽象命名。
- 注释只写代码本身表达不了的约束。

## 技术栈

- 后端：Go `1.27.2`、标准库 `net/http`、`log/slog`、pgx v5（`pgxpool`）。
- 数据库：PostgreSQL 18 + pgvector，主键 `uuidv7()`。
- 迁移：goose（`go tool goose`，版本固定在 `go.mod`）。
- 数据库访问：sqlc `v1.31.1`（单独安装），手写 SQL 在 `backend/db/queries/`，生成代码在 `backend/internal/database/` 并提交进仓库，不要手改。
- 模型：OpenRouter；邮件：Resend（直接用 `net/http` 调用）。
- 前端：Flutter（尚未初始化）。

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

- 配置只来自环境变量；`APP_ENV` 必须为 `dev` 或 `prod`，无默认值。`prod` 必需的配置缺失时启动即报错退出。
- 迁移文件按 `00001_...sql` 顺序编号，包含 Up 与 Down；新增迁移后在本地验证执行、回滚、再执行。
- 表结构：固定取值的字段用 `text` + `CHECK`，不用 enum；外键一律 `ON DELETE CASCADE`。
- sqlc 生成类型：不能为空的列用普通类型，能为空的列加 `*`（`nil` 表示空）；UUID 用 `google/uuid`，时间用 `time.Time`，`numeric` 用 `shopspring/decimal`。
- 验证码、令牌等凭证在数据库中只存哈希值。
- 提交信息使用 Conventional Commits（`feat(backend): ...`、`docs: ...`、`chore: ...`）。
