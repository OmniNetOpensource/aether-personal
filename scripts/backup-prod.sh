#!/bin/sh
# 本文件是生产数据库备份脚本：通过 Docker Compose 导出一份带时间戳的数据库转储到本地备份目录。
set -eu
umask 077

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
backup_dir=${1:-"$project_dir/infra/prod/.local/backups"}
mkdir -p "$backup_dir"
backup_file=$(mktemp "$backup_dir/aether-$(date -u +%Y%m%dT%H%M%SZ).dump.XXXXXX")
trap 'rm -f "$backup_file"' EXIT HUP INT TERM

docker compose --env-file "$project_dir/infra/prod/.env" \
    -f "$project_dir/infra/prod/compose.yaml" \
    exec -T postgres pg_dump -U aether -d aether -Fc > "$backup_file"

trap - EXIT HUP INT TERM
printf 'Backup saved: %s\n' "$backup_file"
