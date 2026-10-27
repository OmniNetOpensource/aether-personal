# 09 Web 生产部署

2026-10-26，用户选择向公众开放注册，并确认 Web 使用 Cloudflare，聊天域名为 `aether.forkicks.fun`。选择理由是将页面分发和公网入口交给 Cloudflare，保留现有 Go、PostgreSQL 与 Linux 沙盒架构。

## 服务位置

| 服务 | 位置 | 入口 |
| --- | --- | --- |
| Web/PWA 静态资源与 Worker 代理 | Cloudflare Workers Static Assets | `aether.forkicks.fun` |
| Go API、Agent Runtime | 阿里云新加坡的应用主机 | `origin-aether.forkicks.fun`，要求源站凭证 |
| PostgreSQL 18 + pgvector | 应用主机的独立生产数据卷 | 不发布宿主机端口 |
| 管理看板 | 应用主机的独立 Node 容器 | `admin-aether.forkicks.fun`，独立管理员密码 |
| 用户 Linux 环境 | 暂时停用，本次不部署 | 不配置云沙箱或桌面入口，见 [07](07-sandbox.md#当前状态暂时停用2026-10-27) |

源站和管理子域名由部署实现选定。当前文件提供部署基础，不表示服务器、DNS 或线上服务已经创建。

## 本地准备

从远端最新 `main` 的基础整理发布版本。不要把 `.env`、数据库或开发机的沙盒状态打包进去。推送公开 GitHub 仓库需要用户授权，也可以将已检查的源码归档直接传到服务器。

在仓库根目录构建 Web 并检查 Worker 打包：

```sh
make edge-dry-run
```

`frontend/build/web` 是静态资源目录。页面固定使用当前域名访问 API，不再提供跨域 API 构建参数。Worker 只代理指定的用户 API、`/healthz`、聊天 `/ws`，不代理开发登录与管理 API；沙盒及桌面入口当前关闭。静态资源由 ASSETS 提供，缺失文件返回 404，不将脚本请求回退成 HTML。

`infra/cloudflare/package-lock.json` 固定部署工具依赖。`infra/cloudflare/wrangler.jsonc` 配置域名和源站地址。其他企业部署时修改这些公开配置，不能使用维护者的域名。

PWA 的 `sw.js`、清单与图标包含在同一产物中，无需额外服务。站点需要 HTTPS。Service Worker 使用当前构建的静态资源清单，不缓存 API 或认证数据；缓存入口为 `/`。新版在页面中提示用户更新，点击后切换并恢复当前标签页草稿。iOS 在 Safari 分享菜单中安装，Android 使用浏览器安装入口。反向代理不能把缺失的 `sw.js` 或清单回退为 HTML，也不能对 `sw.js` 配置长期不可变缓存。

## 准备应用主机

应用主机需要 Linux、Docker Engine 和 Docker Compose v2。入站开放 80、443，SSH 仅允许运维来源。应用、看板和数据库容器均不发布独立端口，只有 Caddy 对外监听。

在 DNS 中将 `origin-aether.forkicks.fun` 和 `admin-aether.forkicks.fun` 的 A 记录指向应用主机。首版让这两个源站记录只解析 DNS，由 Caddy 申请证书。聊天域名由 Worker 的 Custom Domain 管理，不预先建立指向源站的同名记录。不要修改该域已有邮件记录。

在服务器源码目录创建生产配置：

```sh
cp infra/prod/.env.example infra/prod/.env
chmod 600 infra/prod/.env
```

在服务器上生成两份不同的随机十六进制值，分别写入 `POSTGRES_PASSWORD` 和 `ORIGIN_AUTH_SECRET`。例如使用 `openssl rand -hex 32`，不要把结果发进聊天或提交到仓库。数据库密码使用十六进制，避免连接串中的保留字符。

填写 OpenRouter、Resend 与联网服务密钥。`EMAIL_FROM` 必须属于 Resend 已验证的发信域名，示例中的地址不是验证结果。`APP_ENV=prod` 和内部数据库地址由 Compose 设置。开发数据库和生产数据库不共用数据卷。

为看板设置独立密码：

```sh
python3 scripts/set-admin-password.py --env-file infra/prod/.env
```

哈希在 `.env` 中保留单引号，防止 `$` 被展开。留空会关闭后端管理入口。生产不使用 `--generate` 输出的开发密码文件。

将 `RELEASE_TAG` 设置为本次发布的提交 SHA 或唯一版本号。先构建、启动数据库，再单独迁移，最后启动服务：

```sh
make prod-build
make prod-db-up
make prod-migrate
make prod-up
make prod-status
```

迁移不在后端启动时自动执行。`prod-up` 等待后端与看板健康。Caddy 的启动不表示证书签发成功，还需要通过公开 HTTPS 访问验证。

## 连接 Cloudflare

在本机 `infra/cloudflare` 下执行 `npx wrangler login`，使用有权管理 `forkicks.fun` 的账号。将服务器 `ORIGIN_AUTH_SECRET` 的相同值通过交互提示保存到 Worker Secret：

```sh
cd infra/cloudflare
npx wrangler secret put ORIGIN_AUTH_SECRET
```

Worker 覆盖浏览器传来的 `X-Aether-Origin-Key`，只向固定源站发送真实凭证。Caddy 验证凭证并在转发 Go 前移除此头。源站拒绝直接访问，`/admin` 在源站始终返回 404。管理看板通过内部网络调用 Go 管理接口。

Worker 不跟随源站重定向，避免将源站凭证带到其他目标。HTTP API 响应带 `Cache-Control: no-store`，部署账号中也不能配置覆盖这些 API 的强制缓存规则。WebSocket 不缓冲、不改写数据帧。

完成下面的公众上线条件后，在仓库根目录发布：

```sh
make edge-deploy
```

部署会更新 `aether.forkicks.fun` 的公开站点。`dry-run` 只检查打包，不上传或发布。首次 Secret 保存也可能创建 Worker，因此在目标账号和部署配置确认后执行。

## 公众上线条件

开放注册已经确认。以下能力仍须实现和验收，当前部署文件不提供这些能力：

- 验证码申请的 IP 与邮箱频率限制、总发信预算，以及服务端校验的机器人防护。
- 每用户模型用量与并发限制、全局费用上限。具体额度、重置周期和超额后的用户体验待产品确认。
- 真实 Resend 收信、真实模型回复、WebSocket 断线补消息和管理看板权限验收。

`/healthz` 只检查数据库，不能证明模型和邮件可用。2026-10-27 用户决定本次只启用联网执行工具，沙盒代码开关关闭，已有 `SANDBOX_*` 配置也不会启用工具；生产 Compose 已注释这些变量。沙盒隔离、备份与恢复、容量限制等验收留到恢复该功能时进行。

## 备份与更新

在修改现有数据库之前先备份：

```sh
make prod-backup
```

成功后文件位于被 Git 忽略的 `infra/prod/.local/backups/`。脚本使用 PostgreSQL 自定义格式，失败时删除未完成的文件，文件仅当前用户可读写。必须定期将备份复制到另一台主机或独立存储，服务器上的同一份磁盘不构成灾难恢复备份。

恢复时先停止写入，并使用独立空数据库验证。将备份文件传给 PostgreSQL 容器中的 `pg_restore --exit-on-error --no-owner --no-acl -U aether -d <空数据库>`，核对 goose 版本与关键表行数后再安排切换。不要直接在正在使用的生产库中验证恢复。

更新应用前保存当前 `RELEASE_TAG`，执行备份，然后为新版本重新执行构建、迁移和启动命令。回退应用时用旧版本源码和旧标签运行 `prod-up`，前提是数据库仍兼容旧代码。不要假设回退镜像能撤销迁移，不要自动对有生产数据的库执行 goose down。

Cloudflare 的静态资源和 Worker 一起版本化，使用 Cloudflare 的部署回退功能恢复旧 Web 版本。后端和 Web 发布必须保持接口兼容。

## 本地验证记录

2026-10-26，在独立 Compose 项目与数据库中完成以下验证，没有发布 Cloudflare，也没有使用生产供应商凭证：

- 前端 TypeScript、Lynx 与 Web 构建通过，构建过程仍有 Lynx ObjectRef 警告。
- Worker 通过语法检查与 Wrangler dry-run，部署工具依赖审计为零漏洞。`undici` 覆盖到兼容的 `7.30.1` 修复上游工具依赖中的漏洞。
- Go 与管理看板镜像构建成功，PostgreSQL 18 完成全部 12 个迁移，后端和看板健康检查通过。
- 实际经过本地 Workers Runtime、Caddy、生产模式 Go 和 PostgreSQL，验证静态资源、预置验证码验证登录、历史读取、退出及会话撤销。该预置验证码不证明 Resend 已送达邮件。
- 聊天与桌面 WebSocket 均完成 Upgrade，并收到 Go 对无效令牌返回的 1008 关闭码。没有用该检查冒充真实桌面画面验收。
- 源站无凭证返回 403，源站管理 API 返回 404，Worker 不提供开发登录和管理 API。检查中发现并修正了 Caddy 指令排序导致管理路径未被先行拦截的问题。
- 使用实际备份脚本导出数据库，在另一空数据库恢复，核对迁移版本 12 与用户行数 1。备份权限为 600，文件名在 macOS 与 Linux 使用兼容的随机后缀。

2026-10-27 前端改为 React Web/PWA，当前本地验收见 [`frontend/README.md`](../../frontend/README.md)。上述 Lynx 构建记录保留为历史，不代表当前 PWA 的真机或生产验收。

## 官方资料

- [Workers Static Assets](https://developers.cloudflare.com/workers/static-assets/)
- [静态资源和 Worker 路由配置](https://developers.cloudflare.com/workers/static-assets/binding/)
- [Workers WebSocket](https://developers.cloudflare.com/workers/runtime-apis/websockets/)
- [Caddy 反向代理](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)
