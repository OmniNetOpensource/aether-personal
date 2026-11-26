#!/usr/bin/env bash
# 本文件按本机异机副本的 SHA-256 清理旧发布备份，并始终保护主机最近三份备份。
set -euo pipefail
umask 077

# 和安装器共用锁，使备份排序与删除期间不会有发布写入新备份。
publish_root=/opt/aether-personal
backup_dir=$publish_root/backups
exec 9<"$publish_root/publish/install.lock"
flock -n 9

# 按备份完成时间排序，旧版手工备份也计入最近三份。
inventory=$(find "$backup_dir" -maxdepth 1 -type f -name 'pre-*.dump' -printf '%T@ %f\n' |
    sort -k1,1nr -k2,2 | tail -n +4 | cut -d ' ' -f2-)
if [[ ${1:-} == --list ]]; then
    while read -r backup; do
        [[ $backup =~ ^pre-[a-z0-9A-Z-]+\.dump$ ]] || continue
        printf '%s\n' "$backup"
    done <<< "$inventory"
    exit 0
fi

# 只接收已核对的文件名和摘要。删除前重新核对主机文件，确保两边是同一份备份。
cd "$backup_dir"
while read -r checksum backup; do
    [[ $checksum =~ ^[a-f0-9]{64}$ && $backup =~ ^pre-[a-z0-9A-Z-]+\.dump$ ]]
    if ! grep -Fxq -- "$backup" <<< "$inventory"; then
        continue
    fi
    printf '%s  %s\n' "$checksum" "$backup" | sha256sum --check --strict
    rm -- "$backup"
    printf '已清理旧发布备份 %s\n' "$backup"
done
