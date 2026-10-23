# 04 消息与实时通信 `已实现`

用户原话：

> 我希望做成 web socket，即，一打开聊天页面，就建立起双向通信的通道。

> 我想采取类似这样的架构：生产者、共享存储、消费者。

> 把 messages 表里，没有发给用户的消息全部发给用户不就行了嘛？

因此：打开聊天页面时，App 与后端建立一条 WebSocket 双向通道，发消息与实时推送走 WebSocket，登录与历史消息走 HTTP。谁产生消息就写入 `messages` 表，谁需要就从表里查，PostgreSQL 的 `LISTEN/NOTIFY` 只负责“该去查了”的提醒。

## 整体结构

参考业界做法后，采用“实例无状态、状态放在共享存储”的结构（与 [LangGraph Agent Server](https://docs.langchain.com/langsmith/agent-server)、[DBOS](https://www.dbos.dev/blog/postgres-is-all-you-need-for-durable-execution) 同一思路），不采用把请求路由到固定实例的 Actor 模型（如 Cloudflare Durable Objects、Restate Virtual Objects），因为后者依赖专门的平台。

- 所有后端机器对等、无状态：机器内存中不保存丢失后无法找回的信息，Main 的上下文、待读消息、Subagent 状态等都在 PostgreSQL 中。
- 用户请求不需要路由到“正在运行 agent 的机器”：收到消息的机器只负责存入数据库；同一用户的 Main 由数据库锁保证同一时间只有一台机器运行，正在运行的机器在下一次模型调用前会取到新消息。
- 机器之间通过 PostgreSQL 的 `LISTEN/NOTIFY` 互相唤醒，通知只带用户 ID，数据始终以表为准。不引入 Redis。
- 内存中只有两样东西：WebSocket 连接（断开后 App 重连到任意机器即可）与进行中的模型调用（机器崩溃后由其他机器重新调用）。
- 不单独建 `events` 表：用户能看到的消息本就在 `messages` 中，每条 WebSocket 连接自己记录推送位置，从 `messages` 中查出位置之后的消息即可。
- 目前只部署一台，但代码按多台的要求编写。

## 一条消息的完整路径

```mermaid
sequenceDiagram
    participant App
    participant WS as 后端 A：WebSocket
    participant DB as PostgreSQL
    participant Runner as 后端 A 或 B：Main 运行时
    participant Model as OpenRouter

    App->>WS: send_message {client_message_id, text}
    WS->>DB: 事务：按用户加写入锁，插入消息（sequence 为空），NOTIFY
    WS-->>App: delivered {client_message_id}
    WS->>Runner: Wake(user_id)
    Runner->>DB: 尝试获取该用户的 Main 锁
    Runner->>DB: 待读消息补号（变为已读），NOTIFY
    DB-->>WS: NOTIFY user_messages
    WS-->>App: messages {..., read_up_to}
    Runner->>Model: 系统提示词 + messageList + 工具
    Model-->>Runner: assistant 消息（含 send_message 调用）
    Runner->>DB: 事务：保存模型输出与工具结果，NOTIFY
    DB-->>WS: NOTIFY user_messages
    WS-->>App: messages {Main 的回复}
```

### 用户发消息

1. App 发送 `send_message`，`client_message_id` 由 App 生成，App 显示 `sending`。
2. 后端校验：`client_message_id` 为合法 UUID；只支持纯文本，`text` 非空且不超过 32,000 个字符（防止超长消息撑爆上下文、耗费模型费用）。不合法即断开连接。
3. 存入 `messages`，`id` 由数据库生成，`sequence` 为空；同一用户下 `client_message_id` 已存在时不重复保存。
4. 在同一连接上回复 `delivered`；App 超时未收到则显示 `failed`，可点击重发。
5. 发送 NOTIFY，使用户的其他设备也能收到这条消息。
6. 唤醒该用户的 Main：尝试获取锁，获取到则运行；获取不到说明已有机器在运行，到此为止。Main 的运行见 [05 Main 运行时](05-main-runtime.md)。

## WebSocket

使用 `coder/websocket` 库，连接地址 `GET /ws`。

- 浏览器中的 WebSocket 不能自定义请求头，因此连接建立后由 App 发送的第一条消息验证登录，5 秒内未收到或验证失败即断开。不把令牌放在网址中，避免被写入访问日志。
- 登录靠第一条消息中的令牌而非 Cookie，跨站页面无法冒用身份，因此不校验 Origin（Flutter Web 可能部署在其他域名）。
- 后端每 30 秒发送心跳 ping；连接断开时 App 自动重连。
- 不合规则的消息（类型未知、缺 `client_message_id`、文字为空或超过 32,000 字符）直接断开连接，原因 `invalid send_message`。正常使用的 App 不会发出这类消息，出现即说明前端有 bug 或有人手动调用，断开让问题立刻暴露，也省去 App 处理错误回复的代码。日后若有正常用户也会触发的限制（如频率限制），再为那种情况设计错误回复。
- 多台后端时，WebSocket 连接绑定在某一台机器上，由 NOTIFY 唤醒持有该用户连接的机器去查表推送。

| 方向 | 消息 | 说明 |
| --- | --- | --- |
| App → 后端 | `{"type": "auth", "token": "...", "last_position": "..."}` | 第一条消息；`last_position` 为最后收到的消息位置，用于重连补齐，首次连接可省略 |
| App → 后端 | `{"type": "send_message", "client_message_id": "...", "text": "..."}` | 发消息 |
| 后端 → App | `{"type": "delivered", "client_message_id": "..."}` | 消息已存入数据库 |
| 后端 → App | `{"type": "messages", "messages": [...], "read_up_to": "..."}` | 新消息与当前已读位置 |

> 重连字段叫 `last_position` 而非 `last_message_id`：Main 一行里可能有多条消息，它们的 `id`（工具调用 ID）各不相同但 `position`（行 ID）相同，分开命名避免前端误把消息 `id` 当作重连位置。不用 `sequence`：用户消息在 Main 读到前 `sequence` 为空，且 `sequence` 顺序不等于写入顺序；行 ID 在写入时即生成，并由写入锁保证与写入顺序一致。

### 推送的消息

| 字段 | 说明 |
| --- | --- |
| `id` | 显示 ID：用户消息为行 ID；Main 的消息为 `send_message` 的工具调用 ID |
| `position` | 所在行的 ID，App 记下最后一条的值，重连时带上 |
| `client_message_id` | 仅用户消息有；App 据此把本地 `failed` 的消息改为 `delivered` |
| `sender` | `user` 或 `main` |
| `text` | 文字内容 |
| `created_at` | 写入时间 |

## 消息状态

用户原话：

> 我希望给用户发的消息添加这么几个状态：sending，delivered，read

| 状态 | 含义 | 判断方式 |
| --- | --- | --- |
| `sending` | App 已发出，后端尚未确认 | 只存在于 App |
| `failed` | 请求未发送成功（如断网），用户可点击重发 | 只存在于 App |
| `delivered` | 已存入数据库，Main 尚未加入上下文 | `sequence` 为空 |
| `read` | Main 已加入上下文 | `sequence` 有值 |

不新增列，后端返回消息时由 `sequence` 算出 `delivered` / `read`。

## 推送

- 每条 WebSocket 连接记录自己推送到的消息位置。同一用户可有多条连接（多设备），因此不能在消息上标记“已发送”。
- 收到该用户的 NOTIFY 后，每条连接从 `messages` 查出位置之后用户能看到的消息，以及当前已读位置，推送给 App。
- 已读位置用一个值 `read_up_to` 表示（最后一条已读的用户消息）：待读消息总是一次性按顺序补号，已读只会从前往后连续推进。
- App 重连时在 `auth` 中带上最后收到的位置，后端从其后补齐；首次连接从最新一条开始，之前的消息通过历史接口获取。
- 每台机器用一条专用数据库连接执行 `LISTEN user_messages`。监听连接断开后会重连，并让本机所有连接各查一次表，补上断开期间丢失的通知。

## 用户能看到的消息

用户只能看到两类内容：自己发的消息，以及 Main 用 `send_message` 发给用户的文字。

不加列，在 Go 代码中解析：查出 Main 列表中位置之后的用户消息与 `assistant` 行，用户消息直接取文字；`assistant` 行遍历 `tool_calls`，取出发给用户的 `send_message` 的 `text`。每条消息以工具调用 ID 作为显示 ID，同一行中的多条消息一起推送，推送位置仍用行 ID。一行中也可能一条都没有（Main 只思考、不说话）。

- 不加 `user_visible_text` 列：表结构不变，文字只存一份。
- 显示与执行共用同一个解析函数（`agent.MessagesToUser` 与执行时共用的 `parseSendMessage`）：执行时因参数不合法而失败的调用，显示时同样跳过。

## 历史消息

`GET /messages?before=<位置>`，需要登录。从最新往前翻页，每页 50 条用户可见的消息，返回格式与 WebSocket 的 `messages` 相同（不含 `type`）。

- 以 UUIDv7 的消息 ID 作翻页位置，因为待读消息没有序号。
- 部分行（Main 未发消息给用户的 `assistant` 行）不产生可见消息，因此后端会继续往前查，直到凑满一页或没有更早的行。

## 消息 ID 与重发

- 所有消息的 `id` 由数据库生成（`uuidv7()`），用于排序：若由 App 生成，UUIDv7 中的时间来自手机，手机时钟不准或被伪造时顺序会错乱。
- `messages.client_message_id`（App 生成，同一用户下唯一）只用于识别重发：断网导致 `delivered` 未送达时，用户重发会带同一个 `client_message_id`，后端发现已存在则不再保存，直接回复 `delivered`。
- 推送的消息带上 `client_message_id`，App 重连后收到断线期间已保存的消息，可自动将本地 `failed` 的消息改为 `delivered`。

## 写入顺序

`id` 与 `sequence` 都在执行插入语句时分配，提交后才能被查到。两台机器同时写入同一用户的消息时，先分配到编号的可能后提交；连接若已把推送位置更新到较大的编号，较小的那条会被永远跳过。

因此，往某个用户的消息列表插入消息时，在同一事务中先执行 `pg_advisory_xact_lock`（按用户区分）再插入，提交时自动释放。同一用户的写入因此排队进行，先分配编号的一定先提交，按“位置之后”查询不会漏。每次只排队几毫秒，不同用户互不影响。

UUIDv7 的时间来自数据库服务器时钟；时钟被人为往回调时，新 `id` 可能小于旧 `id`。服务器时钟同步通常是逐渐校准，风险可接受。

两把锁的区别：

| 锁 | 范围 | 作用 |
| --- | --- | --- |
| 写入锁 `pg_advisory_xact_lock('messages:<user_id>')` | 单个插入事务，提交时自动释放 | 同一用户的插入排队，保证 `id` 顺序与提交顺序一致 |
| Main 锁 `pg_try_advisory_lock('main:<user_id>')` | Main 运行全程，结束后手动释放 | 同一用户的 Main 同一时间只在一台机器上运行；机器崩溃时数据库自动释放 |

## 暂不采用、以后再评估

- 将“连接服务器”与“agent worker”拆成两组机器（LangGraph 的做法）：代码按两种角色分开编写，目前在同一进程中运行。
- 用租约代替 advisory lock：目前使用 advisory lock，更简单且机器崩溃时立即释放。
- 边生成边显示时通知量大，届时评估 NOTIFY 是否足够，或合并发送、引入 Redis。

`待确认`：边生成边显示的推送方式；各平台的 WebSocket 连接恢复行为；其他平台的连接与部署验收。
