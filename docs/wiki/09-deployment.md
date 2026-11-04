# 09 Web 生产部署

2026-10-26，用户选择向公众开放注册，并确认 Web 使用 Cloudflare，聊天域名为 `aether.forkicks.fun`。选择理由是将页面分发和公网入口交给 Cloudflare，保留现有 Go 与 PostgreSQL 架构。

## 服务位置

| 服务 | 位置 | 入口 |
| --- | --- | --- |
| Web/PWA 静态资源与 Worker 代理 | Cloudflare Workers Static Assets | `aether.forkicks.fun` |
| Go API、Agent Runtime | 阿里云新加坡的应用主机 | `origin-aether.forkicks.fun`，要求源站凭证 |
| PostgreSQL 18 | 应用主机的独立生产数据卷 | 不发布宿主机端口 |
| 管理看板 | 应用主机的独立 Node 容器 | `admin-aether.forkicks.fun`，独立管理员密码 |

源站和管理子域名由部署实现选定。2026-10-30 已在维护者个人阿里云账号的新加坡轻量服务器部署，并连接 Cloudflare；实际验收进度见下方记录。这些域名不作为其他 fork 的默认生产入口。

## 本地准备

从远端最新 `main` 的基础整理发布版本。不要把 `.env` 或数据库打包进去。推送公开 GitHub 仓库需要用户授权，也可以将已检查的源码归档直接传到服务器。

在仓库根目录构建 Web 并检查 Worker 打包：

```sh
make edge-dry-run
```

`frontend/build/web` 是 TanStack Start SPA 模式生成的静态资源目录，包含预渲染的 `index.html`。`frontend/build/server` 只供构建时预渲染使用，不上传到静态托管，也不作为生产 Node 服务启动。页面固定使用当前域名访问 API，不再提供跨域 API 构建参数。Worker 只代理指定的用户 API、`/healthz`、聊天 `/ws`，不代理开发登录与管理 API。静态资源由 ASSETS 提供，缺失文件返回 404，不将脚本请求回退成 HTML。

`infra/cloudflare/package-lock.json` 固定部署工具依赖。`infra/cloudflare/wrangler.jsonc` 配置域名和源站地址。其他人自行部署时修改这些公开配置，不能使用维护者的域名。

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

另用 `openssl rand -base64 32` 生成 `SERVICE_ENCRYPTION_KEY`，保存到生产 `.env` 并单独备份。模型、邮件和联网服务的 Key 在管理看板“服务配置”页填写，Resend 发件人必须属于该账户已验证的域名。`APP_ENV=prod` 和内部数据库地址由 Compose 设置。开发数据库和生产数据库不共用数据卷与主加密密钥。恢复数据库时必须同时恢复原主密钥。

在 Cloudflare 创建仅允许聊天域名的 Turnstile widget，把公开 site key、秘密 Key、聊天域名分别填入 `TURNSTILE_SITE_KEY`、`TURNSTILE_SECRET_KEY`、`TURNSTILE_HOSTNAME`。hostname 只填域名，不含协议和路径，例如 `aether.forkicks.fun`。缺少这些配置不阻止管理看板启动，但生产不发送登录验证码。不要将官方测试 Key 用于公开部署。验证与限流行为见 [03 生产验证码防刷](03-auth.md#生产验证码防刷)。

旧部署升级时，先保留原服务变量、补充主密钥，构建新镜像并执行迁移，再运行一次 `make prod-import-services`。该命令只导入不存在的服务记录，不覆盖已保存或已清除的 Key。导入完成后从生产 `.env` 删除旧服务变量，日常 API 容器不再接收它们。新部署无需导入，先登录看板即可填写服务配置。

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

开放注册已经确认。公众上线需要完成以下验收；维护者实例的实测结果见下方记录：

- 验证码申请的 IP、邮箱和全站共享频率限制及发信预算，以及真实域名上的 Turnstile 服务端校验。代码已补充，不能用本地或测试 Key 验证代替生产验收。
- 真实 Resend 收信、真实模型回复、WebSocket 断线补消息和管理看板权限验收。

2026-10-30，用户确认首版先无限额度向公众开放，不设置每日输入次数、每用户模型费用额度或全站模型费用上限。费用控制不再作为本次上线前置条件；验证码防滥用与真实端到端验收保留为上线条件。该决定的理由与范围见 [03 生产注册范围](03-auth.md#生产注册范围)。

`/healthz` 只检查数据库，不能证明模型和邮件可用。当前执行工具只有搜索与网页读取。

### 2026-10-30 维护者生产部署记录

部署使用冻结版本 `20261030-release`：基于当次已同步的 `main`，仅包含本次审查过的登录防护与部署改动，未混入其他会话正在开发的改动，也未推送公开 GitHub 仓库。

- Cloudflare Worker 版本 `e1404deb-f3f3-479e-902c-6822059bd672` 已接收全部流量，公开健康检查返回 200。
- 阿里云新加坡主机已启动 PostgreSQL、Go API、管理看板和 Caddy；数据库已迁移至 19。只有网关发布 80/443，数据库、API 与看板服务端口均未发布至宿主机。
- 两个源站域名已配置 DNS-only A 记录，Caddy 已取得受信任证书。源站无凭证返回 403，源站和 Worker 的管理路径返回 404；管理看板未登录返回 401，登录、退出和令牌撤销已验证。
- 真实 Turnstile 验证通过，Resend 登录验证码已送达收件箱，并通过网页版邮箱完成真实验证码登录；六项服务凭证均能使用生产主密钥解密。生产开发登录被拒绝，空 Turnstile 令牌不能发码。
- 按用户指定，生产主模型已保存为 `ikun:gemini-4-argon`，Subagent 跟随 Main。真实聊天回复、`search` 搜索、`fetch_url` 网页读取及模型工具续接均已验证，供应商请求返回 HTTP 200。ikun 未返回实际费用，调用日志保留未知值。
- 浏览器收到消息送达回执后模拟断网，确认 WebSocket 关闭；恢复网络后自动携带令牌和 `last_position` 重连，收到已认证 `ready` 与遗漏消息，页面补回断网期间的回复。一次自然断线时未送达的消息经页面重发后成功送达，最终只保留一条用户消息。
- 生产数据库已备份，并在独立空库恢复，核对迁移版本 19 和六条服务配置。备份另存到维护者本机，主密钥单独保存；定期异机备份尚未自动化。
- 临时部署 SSH 公钥已撤销，并确认新连接被拒绝；本机临时私钥也已删除。

详细构建、校验和、权限、网络与验收证据保存在本机被 Git 忽略的 `.local/deploy-20261030/`，其中含受保护的配置和备份，不可公开上传整个目录。

## 备份与更新

### 一键更新现有部署

2026-11-03，用户要求将维护者实例的发布流程整理为一键脚本。入口为 `make publish`。本机的 Python 标准库脚本负责冻结源码、构建、SSH 传输和 Cloudflare 发布，主机上的 Bash 脚本负责备份、迁移与 Compose 更新。选择现有 SSH 与 Wrangler 是为了复用已经部署的主机和 Worker，无需新增 CI、镜像仓库或公开推送流程。

首次使用时，准备可无人值守登录生产主机的 SSH alias。该账号需要 `sudo -n` 执行 Docker 和安装命令。通过可信渠道核对主机公钥，保留 SSH 主机校验。密钥、连接代理与端口写在本机 SSH 配置中，脚本不创建服务器权限或修改防火墙。

在仓库根目录创建本地配置：

```sh
mkdir -p .local
cp infra/prod/publish.example.json .local/publish.json
chmod 600 .local/publish.json
```

`ssh_host` 填 SSH alias 或 `user@host`，`ssh_config` 填本机 SSH 配置路径。示例使用被 Git 忽略的 `.local/publish.ssh`；使用默认 `~/.ssh/config` 时删除 `ssh_config` 字段。`public_url` 填与 Wrangler 配置一致的 HTTPS 聊天站点。其他 fork 需要同时修改自己的域名和 Wrangler 配置。

本机需要 Python 3、Go、Docker、Node.js、npm、Git、OpenSSH、tar、gzip 和 curl。先在 `infra/cloudflare/` 使用 `npx wrangler login` 完成已有 Cloudflare 账号的登录。如果本机联网需要代理，在运行命令的环境中设置 `HTTPS_PROXY`、`HTTP_PROXY`；SSH 的代理单独在 SSH 配置中指定。

配置完成后检查接入：

```sh
make publish-check
```

预检会读取主机的 Compose、Docker 和当前提交，并查询 Cloudflare 部署记录。首次服务器安装仍按本页前面的步骤完成；一键脚本要求已有 `aether-personal-prod` Compose 项目及生产 `.env`。当前应用主机镜像构建目标为 `linux/amd64`。

提交准备发布的改动，在 `main` 分支运行：

```sh
make publish
```

脚本拒绝未提交改动，获取并快进合并 `origin/main`，允许发布尚未推送的本地提交。随后从固定提交建立独立源码快照，执行 Go 构建与 vet、前端构建与类型检查、Worker 检查，并在看板镜像构建中执行构建、类型和服务脚本检查，构建带完整提交标签的镜像。运行期间工作区的后续修改不会进入该快照。

所有上传文件均有 SHA-256 摘要。主机先加载镜像和验证配置，停止 API 与管理看板写入后导出完整数据库备份，再执行 goose up 并等待新服务健康。生产 `.env` 只从当前服务器复制，Compose 项目名和数据卷沿用原值；模型、邮件与 OSS 服务配置继续使用生产数据库中的现有记录。

主机安装成功后，脚本通过 SSH 下载并校验数据库备份，再发布该快照的静态资源与 Worker。最终检查 Worker 的 100% 流量版本、网页与 Service Worker 及清单的字节一致性、`/healthz` 与管理路径隔离。成功提示不代表真实邮件、模型回复或移动设备已验收，这些检查仍按需求单独进行。已有 PWA 标签页通过“更新应用”切换版本。

发布编号包含提交前缀、UTC 时间与随机后缀。产物、日志、数据库备份和 `release.json` 保存在 `.local/publish/<发布编号>/`，目录仅当前用户可读写。数据库备份包含用户数据，不要公开上传。脚本只复制数据库备份到本机，生产主加密密钥仍需单独保管。

只生成本地发布包、不连接生产时运行：

```sh
python3 scripts/publish.py --prepare-only
```

发布包生成后若失败，先查看日志并修正原因，然后复用同一份产物继续。构建阶段尚未生成完整发布包时，修正后重新运行 `make publish`。

```sh
python3 scripts/publish.py --resume <发布编号>
```

构建或上传失败不影响原服务。停服务后、迁移开始前失败，安装器尝试重启原版本。迁移一旦开始，部分迁移可能已经提交，安装器保留原备份和阶段记录，不自动运行 goose down、恢复数据库或启动旧镜像。相同发布编号重试会继续 goose up；已经健康安装的后端不会再次停机。新版本健康而备份下载或 Cloudflare 发布失败时，命令报告未完成，保留新后端供同一发布编号继续。

本机文件锁覆盖主机与 Worker 发布，主机文件锁保护数据库迁移与服务切换。维护者从一台工作站执行发布；不要从不同工作站同时更新同一个 Worker。原有 `prod-*` 与 `edge-*` 命令继续用于首次安装和手工恢复。

在修改现有数据库之前先备份：

```sh
make prod-backup
```

成功后文件位于被 Git 忽略的 `infra/prod/.local/backups/`。脚本使用 PostgreSQL 自定义格式，失败时删除未完成的文件，文件仅当前用户可读写。必须定期将备份复制到另一台主机或独立存储，服务器上的同一份磁盘不构成灾难恢复备份。

恢复时先停止写入，并使用独立空数据库验证。将备份文件传给 PostgreSQL 容器中的 `pg_restore --exit-on-error --no-owner --no-acl -U aether -d <空数据库>`，核对 goose 版本与关键表行数后再安排切换。不要直接在正在使用的生产库中验证恢复。

更新应用前保存当前 `RELEASE_TAG`，执行备份，然后为新版本重新执行构建、迁移和启动命令。回退应用时用旧版本源码和旧标签运行 `prod-up`，前提是数据库仍兼容旧代码。不要假设回退镜像能撤销迁移，不要自动对有生产数据的库执行 goose down。

Cloudflare 的静态资源和 Worker 一起版本化，使用 Cloudflare 的部署回退功能恢复旧 Web 版本。后端和 Web 发布必须保持接口兼容。

### Push 后自动发布

2026-11-03，用户确认采用 GitHub Actions：push 到 `main` 后自动更新维护者生产实例。工作流位于 `.github/workflows/deploy.yml`，仍复用 `scripts/publish.py` 和 `scripts/install-release.sh`。选择 GitHub 托管 runner 是为了让发布不再依赖维护者电脑在线，同时保留已有主机、Docker Compose 和 Cloudflare Worker。

工作流固定 checkout 触发事件的提交，通过 `python3 scripts/publish.py --ci` 构建和发布。Node 固定为 `24.22.1`，与现有 Docker 构建镜像一致，避免工具链随 runner 更新而变化。CI 模式不 fetch 或合并新的 `main`，并在开始前跳过已被后续提交取代的任务。工作流只接受 `main` 的 push 或手动运行，使用同一个 concurrency group 串行执行，`cancel-in-progress: false` 避免新 push 中断正在执行的迁移。排队中的中间版本可能被后续任务替代，目标是最终发布最新的 `main`，不保证每次 push 都单独上线。

Actions 使用独立 Buildx builder，将后端与看板的中间构建层分别导出到本地 OCI 缓存，再由 Actions Cache 按提交保存；新 runner 优先恢复最近一次提交的缓存。每次导出后替换原缓存目录，避免历史层持续累积。缓存只包含公开源码的构建层，不包含生产配置或数据库备份。本机发布默认沿用本机 Docker 缓存；设置绝对路径环境变量 `PUBLISH_BUILD_CACHE` 时也可导入和导出同样的缓存。

后端镜像将只支持 PostgreSQL 的 goose 提前到应用源码之前编译，应用代码变化不会使迁移工具的构建层失效；三个 Go 可执行文件去掉调试符号，减少镜像传输量。看板的 `npm run build` 已包含检查，发布脚本不再在宿主机重复安装和检查。Actions 安装 pigz 并行压缩镜像，本机缺少 pigz 时回退 gzip，两者仍生成相同格式的发布包。外部命令输出会记录耗时与退出码，便于区分构建、传输和主机安装耗时。

发布顺序为检查与构建、上传、停写备份、迁移与启动后端、下载并校验备份、发布 Cloudflare、核对公网文件和健康状态。生产 `.env` 与服务配置仍留在原主机，不复制到 GitHub。启用 CI 后，不要同时在本机运行 `make publish`，以免两个入口交错更新 Worker。

#### 配置 GitHub production 环境

在仓库 Settings → Environments 创建 `production`，将允许部署的分支限制为 `main`，不设置人工审批，保证 push 后自动运行。为该环境配置以下 Secrets：

| Secret | 内容 |
| --- | --- |
| `PROD_SSH_HOST` | 已有生产主机的地址 |
| `PROD_SSH_USER` | 已有可无人值守 sudo 的部署用户 |
| `PROD_SSH_PRIVATE_KEY` | 专供 Actions 使用的 SSH 私钥；对应公钥在服务器使用 `restrict` 授权 |
| `PROD_SSH_KNOWN_HOSTS` | 经可信渠道核对的主机公钥记录；不在发布时自动信任 keyscan |
| `CLOUDFLARE_API_TOKEN` | 限定维护者账号与目标 Zone 的部署 Token，具备 Workers Scripts Edit、Account Settings Read、Zone Read、Workers Routes Edit 权限 |

为同一环境配置以下 Variables：

| Variable | 内容 |
| --- | --- |
| `CLOUDFLARE_ACCOUNT_ID` | Worker 所属账号 ID |
| `PROD_BACKUP_RECIPIENT` | age 备份加密公钥，格式为 `age1...` |

这些凭证在发布步骤运行前检查。SSH 严格核对主机公钥，Cloudflare 使用专用 API Token，不复制本机 Wrangler 的 OAuth 登录或刷新令牌。其他 fork 需修改工作流中的域名，以及自身的 Wrangler 路由和生产地址。

#### 保存与恢复 Actions 备份

GitHub 托管 runner 是临时机器，不能作为备份保存位置。每次运行将已下载并通过 SHA-256 核对的 `database.dump` 用 age 公钥加密，再将密文与不含 SSH 连接信息的 `release.json` 上传为 `production-<run_id>-<run_attempt>` 产物，保留 90 天。服务器仍保留原备份；这不是定期备份任务。

公开仓库的 Actions 产物可被其他人下载，所以只上传 `database.dump.age` 和经过筛选的发布摘要，禁止上传 `.local/`、发布目录、完整日志或明文数据库。解密私钥不放入 GitHub Secrets，维护者本机保存在 `.local/github-actions/backup.agekey`，必须单独保管和备份。生产 `SERVICE_ENCRYPTION_KEY` 仍需要另外备份。

从 Actions 页面下载并解压对应产物后，在保存私钥的本机解密：

```sh
umask 077
age --decrypt --identity .local/github-actions/backup.agekey \
  --output database.dump database.dump.age
```

随后按本页的独立空库恢复流程验证，不直接覆盖运行中的生产数据库。产物在保留期内删除或到期后不能用作长期归档，应另行保管需要长期保留的备份。

发布失败时查看 Actions 日志中的发布编号和主机阶段记录。开始迁移后不自动回滚数据库。已有健康后端而 Worker 未完成时，先处理失败步骤并使用原主机发布记录恢复，不根据 Actions 红色状态推断后端仍为旧版本。

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

### 聊天图片 OSS（2026-11-02）

维护者账号已开通 OSS 按量付费服务，专用 Bucket 为 `aether-personal-images-20261102`，地域 `cn-hangzhou`，标准存储、本地冗余、私有访问，阻止公共访问已开启，使用 OSS 托管 AES256 加密。阿里云的服务开通订单金额为零，后续存储、请求和流量按实际使用计费。

专用 RAM 用户 `aether-personal-images` 不开放控制台登录，只绑定 `AetherPersonalImageObjects` 自定义策略。策略仅允许此 Bucket 的 `aether-chat/*` 对象的 GetObject 与 PutObject，原文保存在 `infra/oss/ram-policy.json`。凭证已加密导入本地项目服务配置，临时明文导出文件已删除。本次未更新生产配置或执行部署。
