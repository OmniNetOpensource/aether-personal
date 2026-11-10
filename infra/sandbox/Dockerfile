# 本镜像固定 AIO 与桌面依赖；运行进程等 AgenticFS 挂载后启动，模板快照不携带用户配置或令牌。
FROM fc-e2b-registry.ap-southeast-1.cr.aliyuncs.com/runtime/desktop:v0.0.44
USER root
COPY --chmod=755 infra/sandbox/build.sh infra/sandbox/start.sh /opt/aether/
RUN /opt/aether/build.sh
ENV WORKSPACE=/mnt/workspace AIO_SANDBOX_HOME=/mnt/workspace AIO_DEFAULT_USER=user
WORKDIR /mnt/workspace
