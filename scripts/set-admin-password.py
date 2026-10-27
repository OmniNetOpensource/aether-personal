#!/usr/bin/env python3
"""设置环境配置文件中的管理员密码哈希，默认 backend/.env。"""

import argparse
import base64
import getpass
import hashlib
import os
from pathlib import Path
import secrets
import shlex

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--generate", action="store_true", help="生成随机密码，保存到 backend/bin/admin-password.txt")
parser.add_argument("--env-file", type=Path, help="目标配置文件，例如 infra/prod/.env")
args = parser.parse_args()
project_dir = Path(__file__).resolve().parent.parent
env_path = args.env_file or project_dir / "backend/.env"
if not env_path.is_file():
    parser.error(f"请先从对应的 .env.example 创建 {env_path}")

if args.generate:
    password = secrets.token_urlsafe(18)
else:
    password = getpass.getpass("管理员密码（至少 12 个字符）：")
    if password != getpass.getpass("再次输入管理员密码："):
        parser.error("两次密码不一致")
if len(password) < 12 or len(password.encode()) > 1024:
    parser.error("密码需至少 12 个字符，且 UTF-8 编码不超过 1024 字节")

salt = secrets.token_bytes(16)
key = hashlib.pbkdf2_hmac("sha256", password.encode(), salt, 600000)
salt_text = base64.urlsafe_b64encode(salt).decode().rstrip("=")
key_text = base64.urlsafe_b64encode(key).decode().rstrip("=")
password_hash = f"pbkdf2-sha256$600000${salt_text}${key_text}"
lines = [line for line in env_path.read_text().splitlines()
         if not line.startswith(("ADMIN_PASSWORD_HASH=", "ADMIN_EMAILS="))]
lines.append("ADMIN_PASSWORD_HASH=" + shlex.quote(password_hash))
env_path.chmod(0o600)
env_path.write_text("\n".join(lines) + "\n")

password_file = project_dir / "backend/bin/admin-password.txt"
if args.generate:
    password_file.parent.mkdir(parents=True, exist_ok=True)
    with open(password_file, "w", opener=lambda path, flags: os.open(path, flags, 0o600)) as output:
        output.write(password + "\n")
    password_file.chmod(0o600)
    print(f"初始密码已保存到 {password_file}")
else:
    password_file.unlink(missing_ok=True)
print(f"密码哈希已写入 {env_path}。重启后端后生效，旧看板会话会失效。")
