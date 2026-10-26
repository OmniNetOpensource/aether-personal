# 本地 Linux 桌面沙盒

这套配置用于验证 OpenSandbox 的命令、文件、浏览器和桌面能力。管理服务固定为 `release-1.1.0` 的镜像摘要，并包含下面说明的兼容修复；桌面基础镜像固定摘要。新增镜像包含 XFCE、Chromium、Playwright、xdotool、截图工具和中文字体。

已接入 Main 与 Subagent 的命令、文件和桌面工具。后端按用户保存稳定映射，首次使用时分配，空闲后自动暂停，下次工具调用恢复。下列 CLI 用于开发者手动检查，独立于用户环境映射。产物下载 API 与生产隔离、可靠检查点尚未实现。

## 启动环境

在仓库根目录创建本地密钥配置。已有 `.env` 时跳过这一步：

```sh
python3 - <<'PY'
from pathlib import Path
import secrets
path = Path('infra/sandbox/.env')
with path.open('x') as config:
    config.write('SANDBOX_PORT=18090\nSANDBOX_API_KEY=' + secrets.token_hex(32) + '\n')
path.chmod(0o600)
PY
make sandbox-up
make sandbox-build
make sandbox-create
make sandbox-status
```

首次构建需要下载桌面和浏览器依赖。`create` 不设置自动过期时间，返回的沙盒 ID 保存在被 Git 忽略的 `infra/sandbox/.local/sandbox.json`。重复创建会报错，避免覆盖现有环境。

管理 API 监听 `127.0.0.1:18090`，要求 `OPEN-SANDBOX-API-KEY`。沙盒代理端口也只发布到宿主机回环地址。Docker socket 只交给可信的管理服务，不挂载到桌面沙盒中。该开发配置不提供生产所需的用户隔离、内网访问控制与虚拟机边界。

## 接入 Agent

先完成上面的服务启动和镜像构建，再在 `backend/.env` 中配置：

```dotenv
SANDBOX_URL=http://127.0.0.1:18090
SANDBOX_API_KEY=与infra/sandbox/.env相同的本地密钥
SANDBOX_IMAGE=aether-personal-desktop:dev
SANDBOX_IDLE_SECONDS=300
```

在 `backend/` 执行 `make migrate` 后重新启动后端。迁移必须指向该环境的数据库；验收应使用独立数据库，避免 PostgreSQL `search_path` 回退到 public 表或迁移版本表。不要输出或提交实际密钥。配置 URL 后，Main 和 Subagent 自动获得沙盒工具，不需要修改前端。没有配置 URL 则不注册工具。

后端首次工具调用创建用户环境。不要把手动 CLI 的 `sandbox.json` ID 填进用户映射，二者用途不同。浏览器支持沙盒内 Playwright/CDP 与桌面操控；默认用户可自行安装 Python/npm 依赖；系统软件通过修改镜像安装，当前运行时禁止 sudo 提权。

## 使用桌面

在当前终端加载配置，再执行以下命令：

```sh
set -a
. ./infra/sandbox/.env
set +a
python3 infra/sandbox/sandbox.py exec 'uname -a; id'
python3 infra/sandbox/sandbox.py exec 'mkdir -p ~/workspace; echo saved > ~/workspace/notes.txt'
python3 infra/sandbox/sandbox.py screenshot --output infra/sandbox/.local/desktop.png
python3 infra/sandbox/sandbox.py download /home/desktop/workspace/notes.txt --output infra/sandbox/.local/notes.txt
```

后台命令使用 `exec --background '命令'`，终端会立即打印 `command_id`。使用 `command-status ID` 和 `logs ID` 查看状态与输出。网络失败后先检查已有执行，命令行工具不会自动重试有副作用的操作。

桌面使用非 root 用户，可在用户目录安装软件与 Python/npm 依赖。当前 no-new-privileges 禁止 sudo 提权，系统软件通过 Dockerfile 配置；沙盒内完整系统管理权限仍待实现。

## 暂停与恢复

```sh
make sandbox-pause
make sandbox-status
make sandbox-resume
make sandbox-status
```

`pause` 和 `resume` 返回请求已接受，实际完成状态由 `status` 确认。命令行工具允许手动暂停运行中的任务。后端自动暂停则要求没有未完成的 Agent 工作、后台命令或结果不明的操作，并经过配置的空闲窗口。

Docker 模式暂停使用 cgroup 冻结，进程与内存留在当前宿主机。它不释放全部内存，也不能证明宿主机重启后能恢复进程。生产要求的磁盘与内存检查点必须使用支持该能力的运行时验证，见 [沙盒设计](../../docs/wiki/07-sandbox.md)。

## 运行边界

单次命令默认 300 秒、上限 3600 秒；命令在后台运行，需要查询状态与实际退出码。接口或进程中断不会自动终止远端命令。结果不明的修改操作不自动重放，会阻止空闲暂停，需先检查原环境和外部执行状态。

当前镜像和 bridge 网络用于本机开发。独立容器、用户 ID 检查与数据库映射已经存在，但尚不能证明生产租户隔离，尤其不能阻止沙盒访问同网络服务。生产必须配置虚拟机边界、租户网络策略、持久存储与备份，且不能把 Docker socket 暴露给沙盒。


## 旧环境与镜像更新

本地 Docker 镜像标签更新后，旧容器仍可能正常运行，但其原镜像 ID 不再能被 Docker inspect 查询。OpenSandbox `release-1.1.0` 的环境查询依赖该镜像查询，会使旧环境的生命周期 API 返回 500。

`Dockerfile.server` 在固定版本上应用两行范围的兼容修复：返回容器创建配置中记录的镜像 URI，避免为了展示环境信息再次查询原镜像。构建时检查原源码片段恰好出现一次，升级供应商版本时必须重新审查。该修复保留原环境 ID、运行进程和文件，不重建用户环境。`make sandbox-up` 会构建这个管理服务镜像。
