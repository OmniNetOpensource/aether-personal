set -eu
mkdir -p /var/lib/aether /home/desktop/workspace /home/desktop/.config/aether/chromium
exec 9>/var/lib/aether/bootstrap.lock
flock -x 9
if [ -f /var/lib/aether/initialized-v1 ]; then
    exit 0
fi
# The upstream desktop image has Chrome, Node and Git, but no document libraries.
# Install once per persistent environment; Playwright connects to the existing Chrome.
python3 -m pip install --disable-pip-version-check --no-cache-dir playwright==1.64.1 pandas openpyxl python-docx pypdf pillow >/var/lib/aether/python-install.log 2>&1
if ! command -v pdftotext >/dev/null || ! fc-match :lang=zh | grep -q 'Noto'; then
    apt-get update >/var/lib/aether/apt-install.log 2>&1
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends poppler-utils fonts-noto-cjk zip unzip >>/var/lib/aether/apt-install.log 2>&1
fi
if ! DISPLAY=:0 xdpyinfo >/dev/null 2>&1; then
    Xvfb :0 -ac -screen 0 1280x800x24 -nolisten tcp >/tmp/aether-xvfb.log 2>&1 9>&- &
fi
for attempt in $(seq 1 50); do
    if DISPLAY=:0 xdpyinfo >/dev/null 2>&1; then break; fi
    sleep 0.1
done
DISPLAY=:0 xdpyinfo >/dev/null
if ! pgrep -x xfce4-session >/dev/null; then
    DISPLAY=:0 dbus-launch startxfce4 >/tmp/aether-xfce.log 2>&1 9>&- &
fi
if ! curl -fsS http://127.0.0.1:9222/json/version >/dev/null 2>&1 && ! pgrep -f '^/opt/chrome/chrome .*--user-data-dir=/home/desktop/.config/aether/chromium' >/dev/null; then
    DISPLAY=:0 google-chrome --no-sandbox --disable-dev-shm-usage --no-first-run --no-default-browser-check --user-data-dir=/home/desktop/.config/aether/chromium --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 --restore-last-session about:blank >/tmp/aether-chrome.log 2>&1 9>&- &
fi
if [ ! -f /var/lib/aether/desktop-secret ]; then
    umask 077
    python3 -c 'import secrets; print(secrets.token_urlsafe(32))' >/var/lib/aether/desktop-secret
fi
if ! pgrep -x x11vnc >/dev/null; then
    x11vnc -display :0 -localhost -nopw -forever -shared -rfbport 5900 >/tmp/aether-vnc.log 2>&1 9>&- &
fi
if ! curl -s -o /dev/null http://127.0.0.1:6080; then
    /opt/noVNC/utils/novnc_proxy --vnc localhost:5900 --listen 6080 --auth-plugin BasicHTTPAuth --auth-source "aether:$(cat /var/lib/aether/desktop-secret)" >/tmp/aether-novnc.log 2>&1 9>&- &
fi
for attempt in $(seq 1 50); do
    if curl -fsS http://127.0.0.1:9222/json/version >/dev/null 2>&1 && curl -s -o /dev/null http://127.0.0.1:6080; then
        touch /var/lib/aether/initialized-v1
        exit 0
    fi
    sleep 0.1
done
exit 1
