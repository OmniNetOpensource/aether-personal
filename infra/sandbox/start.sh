#!/usr/bin/env bash
# 本脚本在长期工作区挂载后启动用户桌面；持久锁阻止两个计算实例同时写同一个浏览器配置。
set -euo pipefail

cd /mnt/workspace
test "$(id -u)" = 1000
mountpoint -q /mnt/workspace
: "${AIO_API_KEY:?AIO_API_KEY is required}"
export AIO_DEFAULT_USER=user
export AIO_SANDBOX_HOME=/mnt/workspace

# 已启动的同一个实例直接复用；此处检查电脑和浏览器，而不只检查守护进程存活。
ready() {
    curl --max-time 2 -fsS -H "x-api-key: $AIO_API_KEY" \
        'http://127.0.0.1:18091/v1/capabilities?refresh=true' |
        python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; sys.exit(0 if d["exec"]["bash"] and d["files"]["write"] and all(d[k]["status"]=="ready" for k in ("browser","computer","code_interpreter")) and d["computer"]["screenshot"] and d["computer"]["actions"] else 1)'
}

if [[ "${1:-}" != --serve ]]; then
    if ready >/dev/null 2>&1; then exit 0; fi
    # 外部命令等待就绪后返回；桌面进程由独立会话托管，不挂在 SDK 请求的生命周期上。
    nohup setsid "$0" --serve </dev/null >/tmp/aether-desktop.log 2>&1 &
    for ((attempt=0; attempt<60; attempt++)); do
        if ready >/dev/null 2>&1; then exit 0; fi
        sleep 1
    done
    echo 'Desktop did not become ready; inspect /tmp/aether-desktop.log inside the sandbox.' >&2
    exit 1
fi

# 锁由整个进程组持有；实例销毁后 NFS 释放锁，下一台电脑才能使用相同的浏览器档案。
exec 9>.aether-desktop.lock
if ! flock -n 9; then
    echo 'Workspace desktop is already running or held by another instance.' >&2
    exit 1
fi
umask 077
mkdir -p .config .cache .local/share browser-profile Downloads
export DISPLAY=:1
export XDG_CONFIG_HOME=/mnt/workspace/.config
export XDG_CACHE_HOME=/mnt/workspace/.cache
export XDG_DATA_HOME=/mnt/workspace/.local/share
export AIO_COMPUTER_USE_LISTEN=127.0.0.1:18100
export AIO_COMPUTER_USE_URL=http://127.0.0.1:18100
export BROWSER_REMOTE_DEBUGGING_HOST=127.0.0.1
export BROWSER_REMOTE_DEBUGGING_PORT=9222
export AIO_HOST=0.0.0.0
export AIO_PORT=18091
export AIO_RUNTIME_DIR=/tmp/aether-aio
export AIO_SHELL_BACKEND=native
export AIO_CODE_BACKEND=native
export AIO_HOST_PYTHON_BIN=/usr/bin/python3

# D-Bus 和 X11 使用实例内的临时目录，用户文件和 Chrome 配置使用持久挂载。
# 先登记清理，再逐个启动子进程，启动途中失败也释放已启动的服务与工作区锁。
pids=()
trap 'if ((${#pids[@]})); then kill "${pids[@]}" 2>/dev/null || true; fi' EXIT
eval "$(dbus-launch --sh-syntax)"
pids+=("$DBUS_SESSION_BUS_PID")
Xvfb :1 -ac -screen 0 1024x768x24 -nolisten tcp &
xvfb_pid=$!
pids+=("$xvfb_pid")
for ((attempt=0; attempt<50; attempt++)); do
    if xdpyinfo -display :1 >/dev/null 2>&1; then break; fi
    sleep 0.1
done
xdpyinfo -display :1 >/dev/null
xfwm4 --replace &
window_manager_pid=$!
pids+=("$window_manager_pid")

# 锁确保旧实例已退出；Chrome 的主机标识文件属于计算实例，销毁后需重新生成。
rm -f browser-profile/SingletonLock browser-profile/SingletonCookie browser-profile/SingletonSocket
google-chrome --no-sandbox --disable-dev-shm-usage --no-first-run --no-default-browser-check \
    --force-renderer-accessibility --password-store=basic --remote-debugging-address=127.0.0.1 \
    --remote-debugging-port=9222 --user-data-dir=/mnt/workspace/browser-profile \
    --window-size=1024,768 about:blank &
chrome_pid=$!
pids+=("$chrome_pid")
computer-use &
computer_pid=$!
pids+=("$computer_pid")
aiod start &
aio_pid=$!
pids+=("$aio_pid")

# 任一服务退出就停止整组，错误保留在实例日志；下一次调用得到明确的启动失败。
wait -n "$aio_pid" "$computer_pid" "$chrome_pid" "$xvfb_pid" "$window_manager_pid"
