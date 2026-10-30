# 03 认证 `已实现`

终端用户用邮箱验证码登录，新邮箱验证成功后自动创建用户。2026-10-28 起，登录全程走主页打开时建立的聊天 WebSocket：前端连接后先发 `hello` 携带已有令牌，没有令牌时在同一条连接上发送验证码与开发登录命令（见 [04 消息与实时通信](04-messaging.md#websocket)），聊天不再使用 HTTP 登录或历史接口。管理看板使用独立的密码登录和会话，见 [08](08-admin-observability.md#权限与-api)。

## 生产注册范围

2026-10-26，用户确认首版直接向公众开放注册。任何新邮箱验证成功后均可创建用户，不要求邀请码或人工审核。选择理由是让公众可以直接注册使用，访问范围不限制为维护者或受邀用户。

现有邮箱登录支持新邮箱验证后自动创建账号，但开放注册的生产部署尚未验收。2026-10-30 补充生产验证码的共享限流和 Turnstile 校验，实际生产域名、真实挑战和邮件送达仍需验收。

2026-10-30，用户确认“现在先无限额度开放”。首版不设置每用户每日输入次数、模型费用额度或全站模型费用上限，选择理由是先让公众无额度门槛使用。该决定不取消登录验证码的防刷保护，也不改变已有的运行时并发与超时约束。模型与联网供应商仍按实际调用计费；后续是否增加额度另行确认。

## 本地开发登录

2026-10-28，用户确认本地 dev 默认无需登录，直接使用 mock 邮箱。`APP_ENV=dev` 时后端在 `ready` 事件中标记 `dev_login`，前端随即在同一条 WebSocket 上自动发送 `dev_login` 命令，创建或复用 `dev@example.com` 并签发普通聊天会话；已有该邮箱的会话则直接复用，已有其他邮箱的会话则切换为开发用户。无需点击按钮、输入邮箱或验证码；自动登录失败时保留「开发测试登录」按钮供重试。选择理由是打开本地页面即可开始调试，同时复用真实会话、用户数据和聊天链路。所有开发测试登录共享这一用户的聊天记录。

`prod` 的 `ready` 事件中 `dev_login` 为 false，且后端拒绝 `dev_login` 命令；生产继续使用邮箱验证码。开发登录只模拟身份，模型请求和其他服务仍按当前配置真实调用。

## 流程

```mermaid
sequenceDiagram
    participant App
    participant API as Go 后端
    participant DB as PostgreSQL
    participant Resend

    App->>API: WebSocket 命令 request_code {email}
    API->>DB: 覆盖该邮箱的 login_codes 行（只存哈希）
    alt prod
        API->>Resend: 发送验证码邮件
    else dev
        API->>API: 验证码打印到日志
    end
    API-->>App: 事件 code_sent

    App->>API: WebSocket 命令 verify_code {email, code}
    API->>DB: 原子地占用一次尝试机会，再比对哈希
    API->>DB: 新邮箱则创建 users；删除 login_codes；新增 sessions
    API-->>App: 事件 authenticated {"token": "..."}

    App->>API: 重连时 hello 带令牌
    API->>DB: 按令牌哈希查 sessions，必要时延期
```

## WebSocket 命令与事件

登录不再提供 HTTP 接口，全部在主页的聊天 WebSocket 上以命令与事件完成，协议见 [04](04-messaging.md#websocket)。

| 命令 | 成功事件 | 失败事件 |
| --- | --- | --- |
| `hello`（可带令牌） | `ready {dev_login, authenticated}` | 令牌失效时回未登录 `ready`，前端清掉本地令牌 |
| `dev_login` | `authenticated {"token": "...", "email": "dev@example.com"}`，仅 dev | prod 回 `request_failed {code: "dev_login_unavailable"}` |
| `request_code` | `code_sent` | 60 秒内重复请求回 `request_failed {code: "too_frequent"}` |
| `verify_code` | `authenticated {"token": "..."}` | 验证码错误或过期统一回 `request_failed {code: "invalid_code"}` |
| `logout` | `logged_out`，只退出当前设备 | 未登录回 `request_failed {code: "unauthorized"}` |

- 邮箱去除首尾空格并转为小写后再使用。
- 未携带或携带无效、过期令牌的请求返回 401。
- 核对验证码时先原子地占用一次尝试机会再比对，并发请求也不会超过 5 次。

## 规则与理由

### 登录凭证

- 使用随机令牌 + 数据库记录（`sessions` 表），不使用 JWT：退出登录或管理员删除用户时删除记录，令牌立即失效；删除用户时经级联删除自动清除。JWT 签发后在过期前无法收回。
- 令牌在重连的 `hello` 中传递，不使用 Cookie，也不放在网址里。
- 允许多设备同时登录，每次登录新增一条 `sessions` 记录。
- `logout` 命令只退出当前设备（删除当前令牌的记录），连接本身保持未登录状态；“退出所有设备”暂不提供。
- 滑动过期：连续 30 天不活跃才失效。中间件在距上次延期超过一天时将过期时间推迟到 30 天后，避免每次请求都写库。过期令牌按未登录处理，返回 401；过期记录暂留表中，以后再加定期清理。

### 验证码

- 6 位数字，有效期 10 分钟；同一邮箱 60 秒内只能请求一次，防止轰炸邮箱和浪费发信额度。
- 同一验证码最多输错 5 次，之后作废，防止暴力猜测；请求新验证码时旧验证码立即作废，同一时间只有一个有效。
- 验证码与登录令牌在数据库中只存哈希值，不存原文，数据库泄露也拿不到可用的凭证。
- 正确验证码的尝试占用、用户与会话创建、验证码删除在同一事务提交，行锁保证同一验证码并发提交也只能成功一次。错误验证码仍提交尝试次数；数据库失败则回滚。
- 验证码只有一百万种可能，数据库泄露时其哈希可被穷举反推；因验证码 10 分钟过期、用过即删，影响有限，暂不引入服务端密钥。登录令牌为 32 字节随机数，不受此影响。

### 生产验证码防刷

生产 `request_code` 携带 Turnstile 的一次性令牌。公开 site key 随 `ready` 下发，秘密 Key 只在后端；后端调用 Cloudflare Siteverify，要求成功、hostname 与部署配置完全一致，且 action 为 `request_code`。缺少配置、验证服务不可用或验证失败均不发信，dev 不加载挑战。

IP 只取 Worker 覆盖写入的 `X-Aether-Client-IP`，不信任浏览器传来的同名头或任意 `X-Forwarded-For`。Caddy 的源站凭证和不公开的 API 容器共同构成信任边界。IPv4 归一化，IPv6 按 /64 计数。数据库计数只存地址或邮箱的 SHA-256 哈希，哈希不是匿名化；超过两天的旧窗口记录会清理。

PostgreSQL 共享以下固定窗口，重连或重启不重置。每个窗口自首次占用起算，不按自然日重置，边界附近可能出现相邻窗口的突发流量。

| 用途 | 默认限制 |
| --- | --- |
| 调用挑战验证 | 全站每分钟 100 次；每个 IP 每分钟 5 次、每小时 30 次 |
| 发验证码邮件 | 全站每分钟 30 次、每 24 小时 1000 次；每邮箱每 24 小时 20 次 |
| 同邮箱重复发码 | 沿用 60 秒冷却 |

这些限制只保护验证码邮件，不限制已登录用户的消息或模型使用。选择共享数据库而非进程内计数，是为了避免多实例、重启与重连绕过。挑战尝试失败仍计数；发信名额与邮箱冷却在一个事务中占用，拒绝时一起回滚。调用 Resend 前先提交，发信失败或结果未知也不退还名额，避免重试风暴。

达到限制返回 `too_frequent`；挑战失效返回 `verification_required`；生产配置缺失或服务不可用返回 `login_unavailable`。全站邮件保护触发时新登录可能暂时受限，已登录聊天继续运行。

### 发信

- 按 `APP_ENV` 区分：`dev` 不发邮件，把验证码打印到后端日志，本地无需 Resend Key；`prod` 通过 Resend 发信，缺少配置时发信请求失败，且绝不打印验证码。缺少服务配置不阻止启动，管理员可进入看板补齐。
- Resend Key 与发件人由管理看板“服务配置”页维护，环境变量不作为日常调用的回退。发件域名需由部署者先在 Resend 后台验证，见 [08 服务凭证管理](08-admin-observability.md#服务凭证管理2026-10-30)。
- 维护者自己的部署沿用 aether-start 已在 Resend 验证的域名，发件人为 `noreply@mail.forkicks.fun`。
- 直接用 `net/http` 调用 Resend 发信接口，不引入官方 SDK：只需“发一封邮件”这一个功能。
- 验证码邮件为中英双语，写在同一封邮件里，不做多语言切换；自行部署后可自行修改文案。样式沿用 aether-start 的深色验证码邮件。

## 表结构

迁移 `00002_create_auth_tables.sql`：

- `login_codes`：`email` 主键（一个邮箱一行，请求新验证码时覆盖，旧的自然作废）、`code_hash`、`attempts`、`expires_at`、`created_at`（判断 60 秒重发间隔）。不关联 `users`，因为新邮箱验证成功前用户还不存在；验证成功后删除该行。
- `sessions`：`id`、`user_id`（级联删除，带索引）、`token_hash`（唯一）、`expires_at`、`created_at`。滑动延期不需要额外的列：剩余不足 29 天即说明距上次延期超过一天。

## 涉及代码

- `backend/internal/httpapi/auth.go`：验证码与会话的核心逻辑。
- `backend/internal/httpapi/websocket.go`：在聊天连接上处理 `hello` 与登录命令；服务器事件定义与写出中转见 `backend/internal/httpapi/events.go`。
- `backend/internal/httpapi/login_email.go`：调用 Resend 发送验证码邮件。
- `backend/db/queries/auth.sql`：验证码与会话的查询。

## 尚未包含

- 其他登录方式扩展（如 SSO）。`待确认`
- 管理员的个人身份与多角色权限。当前共享密码看板见 [08](08-admin-observability.md#权限与-api)。
- 过期会话的定期清理。`待确认`
