# 07 Linux 沙盒工作环境与双向文件流转

2026-10-29 曾决定暂不考虑沙盒并删除早期原型（`00014`）。随后为满足维护者日常代码执行、数据处理与浏览器操作需求，重新接入基于 **阿里云函数计算 Agent Sandbox（FC E2B 协议）+ AgenticFS（NAS Access Point）** 的持久化个人 Linux 电脑，并在 2026-11-11 打通聊天与 `/mnt/workspace` 的双向文件流转。

## 架构与持久边界

- **计算实例可丢弃，工作区持久保留**：每位开通电脑的用户在 `sandbox_workspaces` 表（`00049`）中绑定独立的 AgenticFS Access Point（`server_addr`）与最小权限 RAM 角色（`role_arn`），由维护者通过 `go run ./cmd/init-workspace` 显式绑定。FC 计算实例将 Access Point 以 UID/GID `1000` 挂载到 `/mnt/workspace`。实例空闲超时自动 `pause`（关闭公网流量与自动唤醒），若控制面已释放实例（404），下次调用工具时自动新建实例并重新挂回同一 `/mnt/workspace`，文件、HOME 目录与浏览器 Profile 原样保留。
- **凭证与配置分层**：VPC、交换机、安全组、模板与空闲超时（`SANDBOX_*`）属于部署基础设施环境变量；FC API Key 保存在管理看板「服务配置」的 `aliyun_fc` 加密记录中；每个运行实例的 AIO 令牌与流量访问令牌（`00050`）用部署主密钥 AES-GCM 加密保存在 `sandbox_workspaces`。未配置沙箱或未绑定工作区不会阻止后端或看板启动。
- **有序执行与截图回执**：沙箱内部运行固定版本的 AIO MCP 服务（`sandbox_execute_bash`、`sandbox_execute_code`、`sandbox_file_operations`、`sandbox_str_replace_editor`、`browser_*` 等，定义见 `sandbox_tools.json`）。所有电脑工具在 `tools.Tool` 上标记 `Ordered: true`，同一轮模型回复中的多个电脑调用在同一协程内严格按模型列出的顺序串行执行，避免并发争抢桌面或终端状态；MCP 返回的截图自动存入该用户的私有 OSS，消息表只留 `oss://` 引用。

## 只读实时桌面（noVNC / RFB）

- **按需展示入口**：聊天 WebSocket 在 `ready` 与 `authenticated` 事件中下发 `desktop_available`（2026-11-11），仅当实例已配置 `SANDBOX_*` 且当前用户在 `sandbox_workspaces` 中已有绑定记录时，聊天右上角才显示「查看电脑」按钮；未开通实例或普通未绑定账号不渲染无效按钮。
- **被动观察，不唤醒、不续期**：点击「查看电脑」建立 `GET /sandbox/desktop` WebSocket，首帧发送 `{type: "hello", token}` 完成认证（避免令牌进 URL）。后端 `sandbox.Store.Desktop` 只调用 `GetSandboxInfo` 查询状态并解密已保存的流量令牌直连 `18092` VNC 端口，绝不调用会唤醒或延长计时的 `ConnectSandbox`。电脑休眠或未分配时直接返回状态说明；页面切到后台或关闭弹窗时立即断开流。
- **双向双重只读**：前端 noVNC 开启 `viewOnly`，后端 `desktop_rfb.go` 按字节流解析 RFB 3.8 协议，只放行像素格式、编码声明、画面刷新请求与 fence，丢弃剪贴板写入并拒绝一切键鼠控制报文，同时每 5 秒复查登录会话与实例状态。

## 聊天 ↔ `/mnt/workspace` 双向文件流转（2026-11-11）

参考 Anthropic Files API、OpenAI Code Interpreter、Claude Cowork 与 Manus Sandbox 的前沿实践，采用**「私有 OSS 持久存储 + 入站自动同步进 `uploads` + 出站显式工具交付独立气泡」**双层结构，单文件与单条消息附件合计上限均为 **50 MiB**（远低于 Cloudflare 100 MB 请求上限，全程 `io.Reader` 流式传输，不占宿主机内存，也不要求修改已有 OSS Bucket 的私有权限或新增 `DeleteObject` 授权）：

1. **用户发文件给阿芒（零工具调用、自动落盘）**
   - 已绑定工作区的账号在聊天输入框可选择、粘贴或拖放任意格式文件：`<= 4 MiB` 的 PNG/JPEG/GIF/WebP（单条最多 8 张、合计 `<= 8 MiB`）继续作为视觉图片经 `POST /images` 上传并直接给模型看；其余任意格式文件及超过 4 MiB 的大图（单条最多 10 个，全部附件合计 `<= 50 MiB`）经 `POST /files` 流式存入私有 OSS `aether-chat/<user_id>/files/<UUID>`，聊天流与历史记录中立即展示可点击下载的文件卡片。
   - 下载链接在读取历史或实时推送时由后端通过 OSS `Presign` 本地签发（有效期 24 小时），附带 RFC 5987 `response-content-disposition` 响应参数，浏览器保存时自动还原原始中英文文件名。
   - 发给模型的用户消息不塞入文件字节，只附带一行结构化说明，指明文件名、字节数、媒体类型及其在电脑中的固定路径 `/mnt/workspace/uploads/<文件名>`（同名附件自动加序号区分）。
   - 当阿芒随后调用任何沙箱电脑工具时，`sandbox.Store.ensure` 根据 `sandbox_workspaces.synced_through_sequence`（`00051`）自动把尚未同步过的用户文件与图片从 OSS 流式写入 `/mnt/workspace/uploads/`，阿芒无需调用任何“导入工具”即可直接用脚本或命令读取。

2. **阿芒从电脑发文件给用户（`sandbox_send_file` 独立气泡）**
   - 阿芒在 `/mnt/workspace` 中生成最终交付物（报告、表格、压缩包、代码或图表）后，显式调用 `sandbox_send_file(path="/mnt/workspace/...")`；中间草稿与临时文件留在工作区不打扰用户。
   - 后端校验路径位于 `/mnt/workspace` 内且为非空普通文件（`<= 50 MiB`）：若文件 `<= 4 MiB` 且真实字节为受支持的 PNG/JPEG/GIF/WebP，则存入图片路径并在聊天中生成可放大预览的独立图片气泡；否则流式存入 `files/` 路径并在聊天中生成带文件名、体积和下载按钮的独立文件气泡。
   - 该独立气泡直接复用 `sandbox_send_file` 自身的 `role = 'tool'` 回执记录（标为 `sender = 'main'`、`visible = true`，并在 JSON 中附带展示用的 `parts`）：请求组装时自动剥离 `parts`，模型上下文仍只看到标准文本回执，不破坏 `main_work` 尾部判断，且进程重启恢复时天然幂等、不会重复发送。
