#!/usr/bin/env bash
# 本文件在应用主机安装已校验的发布包：保留生产配置，先备份再迁移，并记录失败后继续所需的阶段。
set -euo pipefail
umask 077

# 同一 Compose 项目共用数据库与证书卷，发布目录只保存各次源码和配置。
publish_root=${PUBLISH_ROOT:-/opt/aether-personal}
project=${PUBLISH_PROJECT:-aether-personal-prod}
[[ $EUID == 0 ]] || { echo '请通过 sudo -n 执行主机安装器' >&2; exit 1; }
[[ $publish_root == /* && $publish_root != / && $project =~ ^[a-z][a-z0-9-]*$ ]] || exit 1
command -v flock >/dev/null
docker compose version >/dev/null
# 安装模式先校验输入并取得锁，再读取当前配置，避免并发发布后复制到过时的 .env。
if [[ ${1:-} != --check ]]; then
    [[ $# == 3 ]] || { echo '用法：install-release.sh <上传目录> <发布编号> <完整提交 SHA>' >&2; exit 1; }
    bundle=$1
    release_id=$2
    revision=$3
    [[ $bundle == /* && $release_id =~ ^[a-f0-9]{12}-[0-9]{8}T[0-9]{6}Z-[a-f0-9]{8}$ && $revision =~ ^[a-f0-9]{40}$ && $release_id == "${revision:0:12}"-* ]] || exit 1
    release=$publish_root/releases/$release_id
    state=$publish_root/publish/$release_id
    backup=$publish_root/backups/pre-$release_id.dump
    install -d -m 700 "$publish_root/publish" "$publish_root/backups"
    exec 9>"$publish_root/publish/install.lock"
    flock -n 9 || { echo '另一个主机发布正在运行' >&2; exit 1; }
fi
api=$(docker ps -aq --filter "label=com.docker.compose.project=$project" --filter label=com.docker.compose.service=api)
[[ -n $api && $api != *$'\n'* ]] || { echo '无法唯一定位现有生产 API 容器' >&2; exit 1; }
previous_config=$(docker inspect "$api" --format '{{index .Config.Labels "com.docker.compose.project.config_files"}}')
[[ $previous_config == /*/infra/prod/compose.yaml && $previous_config != *,* ]] || { echo '当前 Compose 配置路径不符合仓库布局' >&2; exit 1; }
[[ -r ${previous_config%/*}/.env ]] || { echo '缺少原生产 .env' >&2; exit 1; }
case $(uname -m) in
    x86_64) ;;
    *) echo '当前构建仅支持 linux/amd64 应用主机' >&2; exit 1 ;;
esac

# 预检仅查询 Docker 与路径，不建立发布目录、不停服务、不运行迁移。
if [[ ${1:-} == --check ]]; then
    docker info --format 'Docker: {{.OSType}}/{{.Architecture}}'
    docker inspect "$api" --format '当前提交：{{index .Config.Labels "org.opencontainers.image.revision"}}'
    printf '主机预检通过；Compose 项目：%s\n' "$project"
    exit 0
fi
# 镜像标签只取发布目录内的 .env，避免调用环境覆盖新旧 Compose 各自的标签。
unset RELEASE_TAG

# 持久化阶段让断连或迁移失败后的重试保留原恢复点。
install -d -m 700 "$state"
if [[ -f $state/previous-compose ]]; then
    previous_config=$(cat "$state/previous-compose")
else
    printf '%s\n' "$previous_config" > "$state/previous-compose"
fi

# 只抽出重复使用的 Compose 调用，显式传入配置文件，保留固定项目名与已有数据卷。
compose() {
    local config=$1
    shift
    docker compose --project-name "$project" --env-file "${config%/*}/.env" -f "$config" "$@"
}

# 迁移开始前可重启旧服务；迁移开始后旧结构可能已改变，只保留诊断和可恢复的发布包。
finish() {
    local status=$?
    if (( status != 0 )); then
        if [[ -f $state/writers-stopped && ! -f $state/migration-started ]]; then
            compose "$previous_config" up -d --wait --pull never api admin || true
        fi
        printf '主机发布未完成。源码：%s；阶段：%s；备份：%s\n' "$release" "$state" "$backup" >&2
        echo '修正原因后重试同一发布包；不要自动 goose down 或恢复旧镜像。' >&2
    fi
    exit "$status"
}
trap finish EXIT

# 校验清单限定为三个固定文件，随后按同一摘要核对，避免恢复时混入新包。
cd "$bundle"
[[ $(wc -l < SHA256SUMS) -eq 3 ]] || { echo '校验清单长度错误' >&2; exit 1; }
for name in source.tar.gz images.tar.gz install-release.sh; do
    grep -Eq "^[a-f0-9]{64}  $name$" SHA256SUMS || exit 1
done
sha256sum --check SHA256SUMS
if [[ -f $state/SHA256SUMS ]]; then
    cmp SHA256SUMS "$state/SHA256SUMS"
else
    cp SHA256SUMS "$state/SHA256SUMS"
fi

# 发布目录建好后不再重新展开；生产密钥仅从服务器原配置复制，不使用本地 .env 或服务导入。
if [[ ! -f $state/prepared ]]; then
    install -d -m 755 "$release"
    tar -xzf source.tar.gz -C "$release"
    install -m 600 "${previous_config%/*}/.env" "$release/infra/prod/.env"
    sed -i '/^RELEASE_TAG=/d' "$release/infra/prod/.env"
    printf '\nRELEASE_TAG=%s\n' "$release_id" >> "$release/infra/prod/.env"
    docker load -i images.tar.gz
    for service in backend admin; do
        actual=$(docker image inspect "aether-personal-$service:$release_id" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')
        [[ $actual == "$revision" ]] || { echo '镜像提交与源码不一致' >&2; exit 1; }
    done
    compose "$release/infra/prod/compose.yaml" config --quiet
    # 在停机前准备固定基础镜像；已有镜像不重复访问仓库。
    for image in $(compose "$release/infra/prod/compose.yaml" config --images | sort -u); do
        docker image inspect "$image" >/dev/null 2>&1 || docker pull "$image"
    done
    touch "$state/prepared"
fi

# 已健康安装的包不再停机或迁移，允许本地接着完成备份下载和 Worker 发布。
if [[ ! -f $state/installed ]]; then
    touch "$state/writers-stopped"
    compose "$previous_config" stop api admin
    if [[ ! -f $state/backup-ready ]]; then
        compose "$previous_config" exec -T postgres pg_dump -U aether -d aether -Fc > "$backup.partial"
        compose "$previous_config" exec -T postgres pg_restore --list < "$backup.partial" >/dev/null
        mv "$backup.partial" "$backup"
        cp "${previous_config%/*}/.env" "$state/previous.env"
        chmod 600 "$state/previous.env" "$backup"
        touch "$state/backup-ready"
        printf '备份完成：%s\n' "$backup"
    fi
    # 必须先持久化迁移边界；goose 每项迁移单独提交，失败不能推断仍是旧版本。
    touch "$state/migration-started"
    compose "$release/infra/prod/compose.yaml" run --rm --pull never migrate
    compose "$release/infra/prod/compose.yaml" up -d --wait --pull never api admin gateway
fi

# 每次重试仍验证实际运行镜像；成功记录由真实容器与数据库值生成。
for service in api admin; do
    container=$(compose "$release/infra/prod/compose.yaml" ps -q "$service")
    actual=$(docker inspect "$container" --format '{{index .Config.Labels "org.opencontainers.image.revision"}} {{.State.Health.Status}}')
    [[ $actual == "$revision healthy" ]] || { echo "$service 未运行本次健康镜像" >&2; exit 1; }
done
database_version=$(compose "$release/infra/prod/compose.yaml" exec -T postgres psql -U aether -d aether -Atc 'SELECT max(version_id) FROM goose_db_version WHERE is_applied')
latest_migration=$(basename "$(find "$release/backend/db/migrations" -name '[0-9]*_*.sql' | sort | tail -1)")
[[ $database_version -eq $((10#${latest_migration%%_*})) ]] || { echo '数据库版本与本次源码不一致' >&2; exit 1; }
backup_sha256=$(sha256sum "$backup" | cut -d ' ' -f 1)
printf '{"revision":"%s","database_version":%s,"backup":"%s","backup_sha256":"%s"}\n' \
    "$revision" "$database_version" "$backup" "$backup_sha256" > "$state/result.json.partial"
mv "$state/result.json.partial" "$state/result.json"
touch "$state/installed"
compose "$release/infra/prod/compose.yaml" ps
printf '主机发布完成：%s\n' "$release"
