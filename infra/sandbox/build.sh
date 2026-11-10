#!/usr/bin/env bash
# 本脚本安装固定版本的桌面工具与 AIO，并把用户主目录指向未来挂载的工作区；供 Docker 与 FC 模板构建共用。
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
    curl ca-certificates ripgrep tmux python3 nodejs xvfb x11-utils dbus-x11 \
    libatspi2.0-0 at-spi2-core libgtk-3-0 libxdo3 xdotool xclip xfwm4 ffmpeg util-linux fonts-noto-cjk \
    x11vnc websockify
rm -rf /var/lib/apt/lists/*

# 固定下载地址与摘要，使快照和自有镜像使用相同的守护进程。
curl --fail --show-error --location https://aio-static.tos-cn-beijing.volces.com/v0.9.2/linux-x86_64/aiod -o /usr/local/bin/aiod
curl --fail --show-error --location https://aio-static.tos-cn-beijing.volces.com/v0.9.2/linux-x86_64/computer-use -o /usr/local/bin/computer-use
echo '1f52cb04e3e6e9df42e0937db060b390c3220af49a14e17bee8696ab9f11faae  /usr/local/bin/aiod' | sha256sum -c -
echo '7b8458e9e89ec6206eea36505efe55b8630e711adcff75afeca033029439405e  /usr/local/bin/computer-use' | sha256sum -c -
chmod 755 /usr/local/bin/aiod /usr/local/bin/computer-use /opt/aether/start.sh

# 主目录和显式浏览器配置都落在挂载点；保留基础镜像的旧主目录供运维查看。
test "$(id -u user)" = 1000
test "$(id -g user)" = 1000
command -v google-chrome
install -d -o 1000 -g 1000 /mnt/workspace
mv /home/user /opt/aether-default-home
ln -s /mnt/workspace /home/user
usermod -d /mnt/workspace user
