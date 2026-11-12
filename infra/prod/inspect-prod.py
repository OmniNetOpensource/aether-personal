# 本文件通过现有发布连接查询生产健康、容器日志和 PostgreSQL，供仓库 Agent 排查线上问题。

import argparse
import io
import json
from pathlib import Path
import re
import shlex
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[2]
PROJECT_FILTER = "label=com.docker.compose.project=aether-personal-prod"
API_SERVICES = ("api-1", "api-2")


# 查询沿用发布配置中的 SSH 目标和主机公钥校验。
def ssh_command(config):
    host = config["ssh_host"]
    if not re.fullmatch(r"[a-zA-Z0-9_][a-zA-Z0-9_.@-]*", host):
        raise ValueError("ssh_host 必须为 SSH alias 或 user@host")
    command = [
        "ssh", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
        "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15",
        "-o", "ServerAliveCountMax=3",
    ]
    if "ssh_config" in config:
        command.extend(["-F", str(ROOT / Path(config["ssh_config"]).expanduser())])
    return [*command, host]


# OpenSSH 由远端 shell 解释命令，逐项转义 Docker 参数并通过 stdin 单独传 SQL。
def docker(ssh, *arguments, input_text=None, capture=False, merge_output=False):
    result = subprocess.run(
        [*ssh, shlex.join(["sudo", "-n", "docker", *arguments])],
        input=input_text,
        stdin=subprocess.DEVNULL if input_text is None else None,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.STDOUT if merge_output else None,
        text=True,
        check=True,
    )
    return result.stdout


# 按 Compose 标签查询容器 ID，发布更换容器时仍能通过服务名查询。
def find_container(ssh, service):
    containers = docker(
        ssh, "ps", "-aq", "--filter", PROJECT_FILTER,
        "--filter", f"label=com.docker.compose.service={service}", capture=True,
    ).splitlines()
    if len(containers) != 1:
        raise ValueError(f"生产服务 {service} 匹配到 {len(containers)} 个容器")
    return containers[0]


# Agent 可按命令选择证据，stdout 保留结果，失败时返回非零退出码。
def main():
    parser = argparse.ArgumentParser(description="查询生产健康、容器日志和 PostgreSQL。")
    parser.add_argument("--config", type=Path, default=ROOT / ".local/publish.json")
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("health", help="请求公网 /healthz")
    commands.add_parser("status", help="以 JSON Lines 输出生产容器状态和镜像")
    logs = commands.add_parser("logs", help="读取 Go API 容器日志")
    logs.add_argument("--service", choices=API_SERVICES, help="默认查询两个 API 实例")
    logs.add_argument("--since", default="30m", help="起始时间，接受 Docker 时间格式")
    logs.add_argument("--until", help="结束时间，接受 Docker 时间格式")
    logs.add_argument("--tail", type=int, default=200, help="每个实例最多读取的行数")
    commands.add_parser("sql", help="从 stdin 读取一条查询，以 CSV 输出")
    args = parser.parse_args()
    config = json.loads(args.config.read_text(encoding="utf-8"))

    # 公网健康检查使用发布目标的 URL，输出响应正文与 HTTP 状态。
    if args.command == "health":
        public_url = config["public_url"].rstrip("/")
        return subprocess.run([
            "curl", "--silent", "--show-error", "--fail-with-body", "--max-time", "15",
            "--proto", "=https", "--write-out", "\nHTTP %{http_code}\n",
            "--", f"{public_url}/healthz",
        ], stdin=subprocess.DEVNULL).returncode

    ssh = ssh_command(config)

    # 容器状态包含 Docker 的健康检查结果，镜像标签用于核对生产版本。
    if args.command == "status":
        docker(
            ssh, "ps", "-a", "--filter", PROJECT_FILTER, "--format",
            '{"service":{{.Label "com.docker.compose.service" | json}},'
            '"container":{{json .Names}},"id":{{json .ID}},'
            '"image":{{json .Image}},"state":{{json .State}},"status":{{json .Status}}}',
        )
        return 0

    # 每行附上实例名，Agent 可以在同一输出中搜索两个实例。
    if args.command == "logs":
        if args.tail < 0:
            logs.error("--tail 必须为非负整数")
        log_options = ["--timestamps", f"--since={args.since}", f"--tail={args.tail}"]
        if args.until:
            log_options.append(f"--until={args.until}")
        services = (args.service,) if args.service else API_SERVICES
        failed = False
        # 实例独立查询，一个实例不可用时仍能看到另一个实例的日志。
        for service in services:
            try:
                container = find_container(ssh, service)
                lines = docker(ssh, "logs", *log_options, container, capture=True, merge_output=True)
            except ValueError as error:
                print(error, file=sys.stderr)
                failed = True
                continue
            except subprocess.CalledProcessError as error:
                print(f"读取 {service} 日志失败，退出码 {error.returncode}", file=sys.stderr)
                if error.output:
                    print(error.output.rstrip("\n"), file=sys.stderr)
                failed = True
                continue
            for line in io.StringIO(lines):
                line = line.rstrip("\n")
                print(f"{service}\t{line}")
        return int(failed)

    # 把 SQL 作为数据交给 PostgreSQL 打开单条查询的游标，psql 只解释固定脚本。
    if sys.stdin.isatty():
        parser.error("请从 stdin 传入一条 SQL 查询")
    query = sys.stdin.buffer.read().strip()
    if not query:
        parser.error("请从 stdin 传入一条 SQL 查询")
    sql = f"""
BEGIN READ ONLY;
SET LOCAL statement_timeout = '15s';
SET LOCAL TIME ZONE 'UTC';
DO $inspect$
DECLARE
    results refcursor := 'aether_inspect_results';
BEGIN
    OPEN results NO SCROLL FOR EXECUTE convert_from(decode('{query.hex()}', 'hex'), 'UTF8');
END;
$inspect$;
FETCH ALL FROM aether_inspect_results;
ROLLBACK;
"""
    container = find_container(ssh, "postgres")
    docker(
        ssh, "exec", "-i", container, "psql", "-U", "aether", "-d", "aether",
        "-X", "-q", "--csv", "--no-password", "--set=ON_ERROR_STOP=1",
        "--pset=pager=off", "--file=-", input_text=sql,
    )
    return 0


# 命令入口集中报告连接、配置和 SQL 错误。
if __name__ == "__main__":
    try:
        sys.exit(main())
    except subprocess.CalledProcessError as error:
        sys.exit(error.returncode if error.returncode > 0 else 128 - error.returncode)
    except (KeyError, OSError, TypeError, ValueError) as error:
        sys.exit(f"生产查询失败：{error}")
