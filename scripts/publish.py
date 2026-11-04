#!/usr/bin/env python3
# 本文件编排固定提交的发布，复用构建缓存并记录命令耗时，再备份、更新主机和核对 Cloudflare。

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tarfile
import time
from urllib.parse import urlsplit
import uuid


ROOT = Path(__file__).resolve().parent.parent
LOG = None


# 所有外部命令以参数数组运行；输出同时留在发布目录，失败保留原始退出状态。
def run(arguments, cwd=ROOT, stdin=None, quiet=False):
    command = [str(argument) for argument in arguments]
    if not quiet:
        print("$ " + shlex.join(command), flush=True)
    if LOG:
        LOG.write("\n$ " + shlex.join(command) + "\n")
        LOG.flush()
    started = time.monotonic()
    result = subprocess.run(command, cwd=cwd, input=stdin, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, text=True, check=False)
    elapsed = time.monotonic() - started
    if LOG:
        LOG.write(result.stdout)
        LOG.write(result.stderr)
        LOG.write(f"\n耗时 {elapsed:.1f}s，退出码 {result.returncode}\n")
        LOG.flush()
    if not quiet or result.returncode:
        print(result.stdout, end="", flush=True)
    if result.stderr:
        print(result.stderr, end="", file=sys.stderr, flush=True)
    if not quiet or result.returncode:
        print(f"耗时 {elapsed:.1f}s，退出码 {result.returncode}", flush=True)
    result.check_returncode()
    return result.stdout.strip()


# SSH 只使用本地已有连接配置，始终验证主机公钥且禁止交互密码提示。
def remote(ssh, command, stdin=None, quiet=False):
    return run([*ssh, shlex.join(command)], stdin=stdin, quiet=quiet)


# 归档与下载后的备份共用流式摘要，避免将大镜像或数据库完整读入内存。
def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


# 主流程保留本地构建与生产变更的清晰边界；恢复时复用原目录里的不可变产物。
def main():
    global LOG
    parser = argparse.ArgumentParser(description="发布固定的 main 提交到应用主机和 Cloudflare。")
    parser.add_argument("--config", type=Path, default=ROOT / ".local/publish.json")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="只检查依赖、SSH/sudo 与 Cloudflare 登录")
    mode.add_argument("--prepare-only", action="store_true", help="只构建发布包，不连接或修改生产")
    mode.add_argument("--resume", metavar="RELEASE_ID", help="从已有发布目录继续，不重新构建")
    mode.add_argument("--ci", action="store_true", help="发布 GitHub Actions main 事件的准确提交，不拉取后续提交")
    args = parser.parse_args()
    os.umask(0o077)
    # CI 只信任 main 的受支持事件；先核对 checkout 的提交，禁止发布运行期间新出现的 main。
    if args.ci:
        if (os.environ.get("GITHUB_ACTIONS") != "true" or
                os.environ.get("GITHUB_REF") != "refs/heads/main" or
                os.environ.get("GITHUB_EVENT_NAME") not in ("push", "workflow_dispatch")):
            raise RuntimeError("CI 发布只接受 GitHub Actions 的 main push 或手动运行事件")
        revision = os.environ.get("GITHUB_SHA", "")
        if not re.fullmatch(r"[a-f0-9]{40}", revision) or run(["git", "rev-parse", "HEAD"], quiet=True) != revision:
            raise RuntimeError("CI checkout 与触发事件的提交不一致")
        # Actions 不保证排队顺序；过时任务直接结束，避免把已经推进的 main 发布回旧提交。
        current_main = run(["git", "ls-remote", "origin", "refs/heads/main"], quiet=True).split()
        if not current_main:
            raise RuntimeError("无法读取远端 main")
        if current_main[0] != revision:
            print("main 已推进到后续提交，跳过本次过时发布。")
            return
    for command in ("git", "docker", "npm", "npx", "go", "make", "tar", "gzip", "curl", "ssh", "scp"):
        if not shutil.which(command):
            raise RuntimeError(f"缺少命令：{command}")

    # 一个工作区只允许一个发布进程；锁随进程退出释放，不留下需要手工删除的锁状态。
    (ROOT / ".local/publish").mkdir(parents=True, exist_ok=True)
    with (ROOT / ".local/publish.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("另一个发布进程正在运行")

        # 配置是普通 JSON，连接密钥和代理由 SSH 配置及现有环境变量负责。
        config = {}
        if not args.prepare_only and not args.config.is_file():
            raise RuntimeError("先将 infra/prod/publish.example.json 复制到 .local/publish.json 并配置 SSH")
        if args.config.is_file():
            config = json.loads(args.config.read_text())
            if not isinstance(config, dict) or set(config) - {"ssh_host", "ssh_config", "public_url"}:
                raise RuntimeError("发布配置只接受 ssh_host、ssh_config 和 public_url")
            host = config.get("ssh_host", "")
            public_url = config.get("public_url", "")
            if not isinstance(host, str) or not re.fullmatch(r"[a-zA-Z0-9_][a-zA-Z0-9_.@-]*", host):
                raise RuntimeError("ssh_host 必须为 SSH alias 或 user@host")
            if not isinstance(public_url, str):
                raise RuntimeError("public_url 必须为 HTTPS 站点地址")
            url = urlsplit(public_url)
            if url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment or url.path not in ("", "/"):
                raise RuntimeError("public_url 必须为不带路径、凭证或参数的 HTTPS 站点地址")
            public_url = public_url.rstrip("/")
            options = ["-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "StrictHostKeyChecking=yes",
                       "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"]
            if "ssh_config" in config:
                if not isinstance(config["ssh_config"], str):
                    raise RuntimeError("ssh_config 必须为本地 SSH 配置文件路径")
                ssh_config = Path(config["ssh_config"]).expanduser()
                if not ssh_config.is_absolute():
                    ssh_config = ROOT / ssh_config
                if not ssh_config.is_file():
                    raise RuntimeError(f"SSH 配置文件不存在：{ssh_config}")
                options += ["-F", str(ssh_config)]
            ssh = ["ssh", *options, host]
            scp = ["scp", *options]

        # 检查只读取生产状态；无法无人值守登录时，在构建和停服务之前退出。
        run(["docker", "info", "--format", "{{.OSType}}"], quiet=True)
        if not args.prepare_only:
            installer = (ROOT / "scripts/install-release.sh").read_text()
            remote(ssh, ["sudo", "-n", "bash", "-s", "--", "--check"], stdin=installer)
            edge = ROOT / "infra/cloudflare"
            run(["npm", "ci"], cwd=edge)
            if not args.ci:
                run(["npx", "--no-install", "wrangler", "whoami"], cwd=edge)
            # 直接读取部署记录验证发布权限；CI 无需额外授予 Token 读取用户资料的权限。
            run(["npx", "--no-install", "wrangler", "deployments", "list", "--json"], cwd=edge, quiet=True)
            if args.check:
                print("发布预检通过；未构建或修改生产。")
                return

        if args.resume:
            if not re.fullmatch(r"[a-f0-9]{12}-[0-9]{8}T[0-9]{6}Z-[a-f0-9]{8}", args.resume):
                raise RuntimeError("无效的发布编号")
            release_dir = ROOT / ".local/publish" / args.resume
            manifest = json.loads((release_dir / "release.json").read_text())
            if manifest["target"] and manifest["target"] != config:
                raise RuntimeError("恢复时的目标配置必须与原发布一致")
            manifest["target"] = config
            (release_dir / "release.json").write_text(json.dumps(manifest, indent=2) + "\n")
        else:
            # 本机同步 main；CI 保留事件快照，允许 Actions checkout 使用 detached HEAD。
            if not args.ci and run(["git", "branch", "--show-current"], quiet=True) != "main":
                raise RuntimeError("请在 main 分支发布")
            if run(["git", "status", "--porcelain"], quiet=True):
                raise RuntimeError("请先提交工作区改动，再发布，避免未提交内容被漏掉")
            if not args.ci:
                run(["git", "fetch", "origin", "main"])
                run(["git", "merge", "--ff-only", "origin/main"])
            revision = run(["git", "rev-parse", "HEAD"], quiet=True)
            release_id = revision[:12] + "-" + time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()) + "-" + uuid.uuid4().hex[:8]
            release_dir = ROOT / ".local/publish" / release_id
            release_dir.mkdir()
            manifest = {"release_id": release_id, "revision": revision, "target": config}

        print(f"发布记录：{release_dir}", flush=True)
        # 即使后续发布失败，Actions 仍可定位已经下载的备份并加密保存。
        if args.ci:
            with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
                output.write(f"release_dir={release_dir}\nrelease_id={manifest['release_id']}\n")
        LOG = (release_dir / "publish.log").open("a", buffering=1)
        snapshot = release_dir / "source"
        release_id = manifest["release_id"]
        revision = manifest["revision"]
        if not args.resume:
            # 所有检查与镜像使用同一个 git archive 快照，工作区后续编辑不会进入本次发布。
            run(["git", "archive", "--format=tar.gz", "--output=" + str(release_dir / "source.tar.gz"), revision])
            with tarfile.open(release_dir / "source.tar.gz") as archive:
                for member in archive.getmembers():
                    parts = Path(member.name).parts
                    if any(part in (".git", ".local") or part == ".env" or
                           (part.startswith(".env.") and part != ".env.example") for part in parts):
                        raise RuntimeError(f"源码提交包含禁止发布的本地配置：{member.name}")
            snapshot.mkdir()
            # 保留 git 中的源文件权限；发布记录使用的私有 umask 不能进入非 root 运行镜像。
            run(["tar", "-xpzf", release_dir / "source.tar.gz", "-C", snapshot])
            shutil.copyfile(snapshot / "scripts/install-release.sh", release_dir / "install-release.sh")
            run(["go", "build", "./..."], cwd=snapshot / "backend")
            run(["go", "vet", "./..."], cwd=snapshot / "backend")
            run(["make", "edge-dry-run"], cwd=snapshot)
            # 看板 Docker 构建中的 npm run build 已包含类型与服务脚本检查，不在宿主机重复安装。
            cache_root = os.environ.get("PUBLISH_BUILD_CACHE")
            for service in ("backend", "admin"):
                command = ["docker", "buildx", "build", "--load", "--platform", "linux/amd64", "-f", f"infra/prod/Dockerfile.{service}",
                           "--label", f"org.opencontainers.image.revision={revision}", "-t", f"aether-personal-{service}:{release_id}"]
                if cache_root:
                    cache = Path(cache_root).resolve() / service
                    next_cache = cache.with_name(service + "-next")
                    shutil.rmtree(next_cache, ignore_errors=True)
                    if (cache / "index.json").is_file():
                        command += ["--cache-from", f"type=local,src={cache}"]
                    command += ["--cache-to", f"type=local,dest={next_cache},mode=max"]
                run([*command, "."], cwd=snapshot)
                # 每次导出到新目录再替换，避免旧版本的无用缓存层一直累积。
                if cache_root:
                    shutil.rmtree(cache, ignore_errors=True)
                    next_cache.replace(cache)
            run(["docker", "save", "-o", release_dir / "images.tar", f"aether-personal-backend:{release_id}", f"aether-personal-admin:{release_id}"])
            # pigz 并行压缩并保持 gzip 格式，服务器加载与既有恢复包格式保持兼容。
            run(["pigz" if shutil.which("pigz") else "gzip", release_dir / "images.tar"])
            manifest["sha256"] = {name: sha256(release_dir / name) for name in ("source.tar.gz", "images.tar.gz", "install-release.sh")}
            (release_dir / "SHA256SUMS").write_text("".join(f"{digest}  {name}\n" for name, digest in manifest["sha256"].items()))
            (release_dir / "release.json").write_text(json.dumps(manifest, indent=2) + "\n")
            if args.prepare_only:
                print(f"发布包已生成，未连接生产：{release_dir}")
                return

        # 恢复前核对本地包未改变；上传目录每次独立，服务器端再校验一次。
        for name, digest in manifest["sha256"].items():
            if sha256(release_dir / name) != digest:
                raise RuntimeError(f"本地发布包校验失败：{name}")
        upload = f"/tmp/aether-publish-{release_id}"
        remote(ssh, ["mkdir", "-p", "-m", "700", upload])
        run([*scp, *[release_dir / name for name in (*manifest["sha256"], "SHA256SUMS")], f"{host}:{upload}/"])
        remote(ssh, ["sudo", "-n", "bash", f"{upload}/install-release.sh", upload, release_id, revision])

        # 通过既有 SSH 通道下载备份，校验后才落为完成文件，不经过网页临时下载链接。
        server_result = json.loads(remote(ssh, ["sudo", "-n", "cat", f"/opt/aether-personal/publish/{release_id}/result.json"], quiet=True))
        manifest["server"] = server_result
        (release_dir / "release.json").write_text(json.dumps(manifest, indent=2) + "\n")
        backup = Path(server_result["backup"])
        if backup.parent != Path("/opt/aether-personal/backups") or backup.name != f"pre-{release_id}.dump":
            raise RuntimeError("服务器返回了不属于本次发布的备份路径")
        print("下载并校验数据库备份……", flush=True)
        backup_started = time.monotonic()
        with (release_dir / "database.dump.partial").open("wb") as output:
            subprocess.run([*ssh, shlex.join(["sudo", "-n", "cat", str(backup)])], stdout=output, check=True)
        if sha256(release_dir / "database.dump.partial") != server_result["backup_sha256"]:
            raise RuntimeError("异机备份校验失败；服务器备份与新后端保留")
        (release_dir / "database.dump.partial").replace(release_dir / "database.dump")
        backup_timing = f"数据库备份下载及校验耗时 {time.monotonic() - backup_started:.1f}s\n"
        print(backup_timing, end="", flush=True)
        LOG.write(backup_timing)
        LOG.flush()

        # 后端健康后才更新 Worker；失败保留可恢复目录，不自动回退已迁移的数据库。
        edge = snapshot / "infra/cloudflare"
        output = run(["npx", "--no-install", "wrangler", "deploy", "--no-autoconfig", "--message", f"Release {release_id}"], cwd=edge)
        version = re.search(r"Current Version ID:\s*([a-f0-9-]{36})", output)
        if not version:
            raise RuntimeError("Cloudflare 未返回版本编号，请检查发布日志")
        manifest["worker_version"] = version.group(1)
        (release_dir / "release.json").write_text(json.dumps(manifest, indent=2) + "\n")
        deployments = json.loads(run(["npx", "--no-install", "wrangler", "deployments", "list", "--json"], cwd=edge, quiet=True))
        (release_dir / "cloudflare-deployments.json").write_text(json.dumps(deployments, indent=2) + "\n")
        latest = max(deployments, key=lambda item: item["created_on"])
        if latest["versions"] != [{"version_id": manifest["worker_version"], "percentage": 100}]:
            raise RuntimeError("Cloudflare 当前流量未全部指向本次版本")

        # 公网文件逐字节核对，同时检查数据库健康与管理接口隔离；不冒充真实邮件或模型验收。
        for path, expected in (("/", 200), ("/sw.js", 200), ("/manifest.webmanifest", 200), ("/healthz", 200), ("/admin", 404)):
            destination = release_dir / (path.strip("/").replace("/", "_") or "index.html")
            status = run(["curl", "--silent", "--show-error", "--retry", "3", "--max-time", "30", "-o", destination,
                          "-w", "%{http_code}", public_url + path], quiet=True)
            if status != str(expected):
                raise RuntimeError(f"公网校验失败：{path} 返回 {status}，预期 {expected}")
            local = snapshot / "frontend/build/web" / destination.name
            if path in ("/", "/sw.js", "/manifest.webmanifest") and sha256(destination) != sha256(local):
                raise RuntimeError(f"公网文件与本次构建不一致：{path}")
        manifest["status"] = "published"
        (release_dir / "release.json").write_text(json.dumps(manifest, indent=2) + "\n")
        print(f"发布完成：{public_url}\n提交：{revision}\n备份与发布记录：{release_dir}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.CalledProcessError, KeyboardInterrupt) as error:
        print(f"发布未完成：{error}", file=sys.stderr)
        if LOG and (Path(LOG.name).parent / "release.json").is_file():
            print("修正原因后可用 --resume <发布编号> 继续已有发布包；不要自动执行数据库回滚。", file=sys.stderr)
        else:
            print("请修正预检或构建错误后重新运行。", file=sys.stderr)
        sys.exit(1)
