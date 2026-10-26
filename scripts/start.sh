#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
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

for command in go node npm docker make curl; do
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
sandbox_url="$(cd "$project_dir/backend"; . ./.env; printf '%s' "${SANDBOX_URL:-}")"

node --input-type=module - "$backend_port" "$admin_port" <<'JS'
import net from 'node:net';
const ports = process.argv.slice(2).map(Number);
if (ports.some(port => !Number.isInteger(port) || port < 1 || port > 65535) || ports[0] === ports[1]) {
  console.error('错误：后端与看板端口必须是不同的有效端口。');
  process.exit(1);
}
for (const port of ports) {
  await new Promise((resolve, reject) => {
    const socket = net.createConnection({ host: '127.0.0.1', port });
    socket.setTimeout(1000, () => socket.destroy(new Error('Port check timed out')));
    socket.once('connect', () => {
      socket.destroy();
      reject(Object.assign(new Error('Port in use'), { code: 'EADDRINUSE' }));
    });
    socket.once('error', error => {
      socket.destroy();
      if (error.code === 'ECONNREFUSED') resolve();
      else reject(error);
    });
  }).catch(error => {
    console.error(`错误：无法使用端口 ${port}（${error.code}），请先关闭占用它的服务。`);
    process.exit(1);
  });
}
JS

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
if [[ "$sandbox_url" == http://127.0.0.1:* || "$sandbox_url" == http://localhost:* ]]; then
  [[ -f infra/sandbox/.env ]] || fail "本机沙盒已启用，但缺少 infra/sandbox/.env；配置方式见 infra/sandbox/README.md。"
  sandbox_port="$(. ./infra/sandbox/.env; printf '%s' "${SANDBOX_PORT:-18090}")"
  [[ "${sandbox_url%/}" == "http://127.0.0.1:$sandbox_port" || "${sandbox_url%/}" == "http://localhost:$sandbox_port" ]] || fail "SANDBOX_URL 与 infra/sandbox/.env 中的端口不一致。"
  sandbox_image="$(cd backend; . ./.env; printf '%s' "${SANDBOX_IMAGE:-}")"
  if [[ "$sandbox_image" == aether-personal-desktop:dev ]] && ! docker image inspect "$sandbox_image" >/dev/null 2>&1; then
    make sandbox-build
  fi
  printf '\n启动配置中的本机 Linux 沙盒管理服务……\n'
  make sandbox-up >"$log_dir/sandbox.log" 2>&1 || {
    tail -n 30 "$log_dir/sandbox.log" >&2
    fail "本机沙盒启动失败，详见 $log_dir/sandbox.log。"
  }
  wait_ready "${sandbox_url%/}/health" "" "$log_dir/sandbox.log"
fi
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
printf 'dev 聊天点击「开发测试登录」即可进入；看板使用独立管理员密码登录。\n'
printf '按 Ctrl+C 关闭后端与看板；数据库和沙盒服务保留运行。\n\n'
tail -n 10 -f "$log_dir/backend.log" "$log_dir/admin.log" &
service_pids+=("$!")

while kill -0 "$backend_pid" 2>/dev/null && kill -0 "$admin_pid" 2>/dev/null; do
  sleep 1
done
fail "后端或看板已退出，请检查 $log_dir 中的日志。"
