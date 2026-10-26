#!/bin/sh
set -eu

mkdir -p /home/desktop/workspace

Xvfb :0 -screen 0 1280x800x24 -nolisten tcp >/tmp/xvfb.log 2>&1 &
display_pid=$!
while ! DISPLAY=:0 xdotool getdisplaygeometry >/dev/null 2>&1; do
    if ! kill -0 "$display_pid" 2>/dev/null; then
        cat /tmp/xvfb.log >&2
        exit 1
    fi
    sleep 0.2
done

dbus-launch startxfce4 >/tmp/desktop.log 2>&1 &

/opt/browser/bin/python -u - <<'PY' >/tmp/browser.log 2>&1 &
from playwright.sync_api import sync_playwright

with sync_playwright() as playwright:
    browser = playwright.chromium.launch_persistent_context(
        "/home/desktop/.config/aether/chromium",
        headless=False,
        viewport=None,
        args=["--remote-debugging-port=9222", "--restore-last-session"],
    )
    browser.wait_for_event("close", timeout=0)
PY

# VNC stays on the sandbox loopback; only the authenticated API proxy reaches noVNC.
x11vnc -display :0 -localhost -nopw -forever -shared -rfbport 5900 >/tmp/vnc.log 2>&1 &
exec /usr/bin/websockify --web=/usr/share/novnc 6080 localhost:5900
