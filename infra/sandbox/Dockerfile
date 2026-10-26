FROM node:24-bookworm-slim@sha256:55fb5f5a527ea2c5c4a95f963e48ef90aa38c7b346054fdc8ee9957a2b55d205 AS node-runtime

FROM opensandbox/desktop@sha256:06b5401f8c9e3fd713511e05694a132c786ea051ce73994944d762aa7938256d

USER root
ENV DEBIAN_FRONTEND=noninteractive \
    DISPLAY=:0 \
    PLAYWRIGHT_BROWSERS_PATH=/opt/browsers \
    NPM_CONFIG_PREFIX=/home/desktop/.local \
    PATH=/home/desktop/.local/bin:/opt/tools/bin:/opt/browser/bin:$PATH

RUN apt-get update \
    && apt-get install -y --no-install-recommends python3-venv xdotool scrot fonts-noto-cjk git curl jq zip unzip poppler-utils \
    && rm -rf /var/lib/apt/lists/*

RUN python3 -m venv /opt/browser \
    && /opt/browser/bin/pip install --no-cache-dir playwright==1.64.1 \
    && /opt/browser/bin/playwright install --with-deps --no-shell chromium \
    && chmod -R a+rX /opt/browser /opt/browsers \
    && rm -rf /var/lib/apt/lists/*

RUN python3 -m venv /opt/tools \
    && /opt/tools/bin/pip install --no-cache-dir pandas openpyxl python-docx pypdf pillow \
    && chown -R desktop:desktop /opt/tools

COPY --from=node-runtime /usr/local/bin/node /usr/local/bin/node
COPY --from=node-runtime /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s /usr/local/lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s /usr/local/lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

COPY start-desktop.sh /usr/local/bin/start-desktop
RUN chmod 755 /usr/local/bin/start-desktop

USER desktop
WORKDIR /home/desktop
CMD ["/usr/local/bin/start-desktop"]
