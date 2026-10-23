# Aether Flutter

简版聊天客户端，只展示用户与 Main 的消息流。Subagent 与工具在后端运行。

SDK 为 Flutter 3.47.7、Dart 3.13.5，安装在 `/Users/lhyu/develop/flutter`。

```sh
cd frontend
/Users/lhyu/develop/flutter/bin/flutter pub get
/Users/lhyu/develop/flutter/bin/flutter analyze
/Users/lhyu/develop/flutter/bin/flutter test
/Users/lhyu/develop/flutter/bin/flutter build web --no-web-resources-cdn
```

本地由 Go 后端同源提供 `build/web`，浏览器打开后端地址。客户端自动连接同源 HTTP 和 WebSocket，不需要把服务商密钥放入前端。需要连接其他后端时，构建加入 `--dart-define=API_BASE_URL=http://localhost:8080`，并由该后端配置相应的跨源访问。

开发环境的邮箱验证码打印在后端日志中。登录凭证保存在当前设备，刷新后会从后端重新加载真实消息。重连使用消息的 `position`，重发保留 `client_message_id`，避免重复入库。

原生平台工程已生成，尚未在本机验证原生构建。

Web 构建使用 `--no-web-resources-cdn`，从本地加载渲染资源。中文字体随应用打包，避免 Web 渲染依赖外部字体请求。字体为 Google Fonts 的 [Noto Sans SC](https://github.com/google/fonts/tree/main/ofl/notosanssc)，许可证见 `assets/fonts/OFL.txt`。
