#!/usr/bin/env bash
# 本文件是本地一键启动脚本：拉起后端、前端与管理看板；端口已被占用时先关闭旧进程，退出时再关掉本次启动的服务。
set -euo pipefail

project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
admin_dir="${ADMIN_DIR:-$project_dir/admin}"
admin_port="${ADMIN_PORT:-5174}"
service_pids=()

fail() {
  printf '\n错误：%s\n' "$*" >&2
  exit 1
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if ((${#service_pids[@]})); then
    printf '\n正在关闭本次启动的后端和看板……\n'
    kill "${service_pids[@]}" 2>/dev/null || true
    for pid in "${service_pids[@]}"; do
      wait "$pid" 2>/dev/null || true
    done
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for command in go node npm docker make curl lsof; do
  command -v "$command" >/dev/null || fail "未安装 $command。"
done
[[ -f "$project_dir/backend/.env" ]] || fail "请先从 backend/.env.example 创建 backend/.env 并填写密钥。"
[[ -f "$admin_dir/server.mjs" ]] || fail "找不到管理看板：$admin_dir；可用 ADMIN_DIR 指定目录。"
node -e 'const [major, minor] = process.versions.node.split(".").map(Number); process.exit(major > 22 || (major === 22 && minor >= 12) ? 0 : 1)' || fail "请使用 Node.js 22.12 或更新版本（建议 Node 24）。"
docker info >/dev/null 2>&1 || fail "Docker 尚未就绪，请先启动 Docker Desktop 或本机 Docker 服务。"
docker compose version >/dev/null 2>&1 || fail "需要 Docker Compose v2。"

backend_port="$(
  cd "$project_dir/backend"
  set -a
  . ./.env
  [[ "${APP_ENV:-}" == dev ]] || fail "此脚本仅用于 APP_ENV=dev。"
  printf '%s' "${PORT:-8080}"
)"
[[ "$backend_port" =~ ^[0-9]+$ && "$admin_port" =~ ^[0-9]+$ ]] \
  && ((10#$backend_port >= 1 && 10#$backend_port <= 65535 && 10#$admin_port >= 1 && 10#$admin_port <= 65535)) \
  && [[ "$backend_port" != "$admin_port" ]] \
  || fail "后端与看板端口必须是不同的有效端口。"

# listeners 列出正在监听该 TCP 端口的进程号；没有监听时为空。
listeners() {
  lsof -nP -iTCP:"$1" -sTCP:LISTEN -t 2>/dev/null || true
}

# stop_port 在启动前腾出开发端口。上次 start.sh 占着端口时结束那次脚本，由它关掉自己的子进程；
# 其他程序只结束监听进程，避免误关它的父进程。
stop_port() {
  local port=$1 pid ppid args target seen existing attempt
  local -a targets=()
  while IFS= read -r pid; do
    [[ -z "$pid" || "$pid" == "$$" ]] && continue
    ppid="$(ps -o ppid= -p "$pid" 2>/dev/null | tr -d '[:space:]' || true)"
    args=""
    if [[ -n "$ppid" && "$ppid" != "$$" ]]; then
      args="$(ps -o args= -p "$ppid" 2>/dev/null || true)"
    fi
    target="$pid"
    if [[ "$args" == *start.sh* ]]; then
      target="$ppid"
    fi
    seen=
    if ((${#targets[@]})); then
      for existing in "${targets[@]}"; do
        [[ "$existing" == "$target" ]] && seen=1
      done
    fi
    [[ -n "$seen" ]] || targets+=("$target")
  done < <(listeners "$port")
  ((${#targets[@]})) || return 0
  printf '端口 %s 已有服务，正在关闭后重新启动……\n' "$port"
  kill -TERM "${targets[@]}" 2>/dev/null || true
  for ((attempt = 0; attempt < 60; attempt++)); do
    [[ -z "$(listeners "$port")" ]] && return 0
    sleep 0.1
  done
  while IFS= read -r pid; do
    [[ -n "$pid" && "$pid" != "$$" ]] && kill -KILL "$pid" 2>/dev/null || true
  done < <(listeners "$port")
  sleep 0.2
  [[ -z "$(listeners "$port")" ]] || fail "无法关闭占用端口 ${port} 的服务。"
}

stop_port "$backend_port"
stop_port "$admin_port"

mkdir -p "$project_dir/backend/bin"
log_dir="$(mktemp -d "$project_dir/backend/bin/start-logs.XXXXXX")"
printf '日志目录：%s\n' "$log_dir"

wait_ready() {
  local url=$1 pid=$2 log_file=$3
  for ((attempt = 0; attempt < 60; attempt++)); do
    if [[ -n "$pid" ]] && ! kill -0 "$pid" 2>/dev/null; then
      tail -n 30 "$log_file" >&2
      fail "服务启动失败，详见 $log_file。"
    fi
    if curl --fail --silent --max-time 1 "$url" >/dev/null 2>&1; then
      return
    fi
    sleep 1
  done
  tail -n 30 "$log_file" >&2
  fail "等待服务就绪超时：$url；详见 $log_file。"
}

cd "$project_dir"
printf '\n[1/4] 安装依赖并构建聊天前端……\n'
make frontend
printf '\n[2/4] 启动数据库并执行迁移……\n'
make -C backend db-up
make -C backend migrate
printf '\n[3/4] 构建后端……\n'
(cd backend && go build -o "$log_dir/server" ./cmd/server)

printf '\n[4/4] 启动后端与管理看板……\n'
(
  cd "$project_dir/backend"
  set -a
  . ./.env
  export FRONTEND_DIR="$project_dir/frontend/build/web"
  exec "$log_dir/server"
) >"$log_dir/backend.log" 2>&1 &
backend_pid=$!
service_pids+=("$backend_pid")
(
  cd "$admin_dir"
  export HOST=127.0.0.1 PORT="$admin_port" API_UPSTREAM="http://127.0.0.1:$backend_port"
  exec node --env-file-if-exists=.env server.mjs
) >"$log_dir/admin.log" 2>&1 &
admin_pid=$!
service_pids+=("$admin_pid")

wait_ready "http://127.0.0.1:$backend_port/healthz" "$backend_pid" "$log_dir/backend.log"
wait_ready "http://127.0.0.1:$admin_port/" "$admin_pid" "$log_dir/admin.log"

printf '\n启动完成\n聊天：http://127.0.0.1:%s\n看板：http://127.0.0.1:%s\n' "$backend_port" "$admin_port"
printf 'dev 聊天默认自动进入 dev@example.com；看板使用独立管理员密码登录。\n'
printf '按 Ctrl+C 关闭后端与看板；数据库保留运行。\n\n'
tail -n 10 -f "$log_dir/backend.log" "$log_dir/admin.log" &
service_pids+=("$!")

while kill -0 "$backend_pid" 2>/dev/null && kill -0 "$admin_pid" 2>/dev/null; do
  sleep 1
done
fail "后端或看板已退出，请检查 $log_dir 中的日志。"
