# 10 Schedule 定时任务

2026-11-08，用户重新加入两项能力：Main 用 `schedule` 工具设置定时任务，以及系统在对话闲置后定时请 Main 回看历史的闲置后台反思。2026-11-26，用户决定移除闲置后台反思，目前仅保留 `schedule` 定时任务：后台调度器到点往该用户的对话里插入一条不可见的 user 消息（`schedule_event`），再唤醒 Main。

此前 2026-10-27 曾确认过一版定时安排（提交 `89b82dd2`），2026-11-03 用户决定先不做，代码与表由 `00030_drop_schedules` 删除。本次是重新设计，不沿用旧版的暂停状态、版本号与投递记录表。

## 移除闲置后台反思（2026-11-26）

用户原话：

> 我想把proactive去掉，我感觉目前没啥用

据此整体移除系统内置的闲置主动触发（`proactive`），仅保留用户/Agent 通过 `schedule` 工具创建的定时任务：

- 删除后台扫描包 `backend/internal/proactive/proactive.go` 及 `main.go` 中的启动协程。
- 新增迁移 `00069_drop_reflection_idle_minutes.sql` 删除 `model_settings.reflection_idle_minutes` 列；同步移除 `ListReflectionDue` 查询、运行时配置字段、`GET/PUT /admin/runtime-settings` 对应参数、管理看板「闲置反思时长」表单项，以及 `prompt.go` 中的 `ReflectionMessageName`、`ReflectionPrompt` 与 `MainInstructions` 相关段落。
- 保留 `messages` 表中已产生的历史反思触发记录，并保留管理看板时间线（`admin/src/Timeline.tsx`）对 `reflection`、`memory_update`、`recent_followup`、`unknown_unknowns` 的中文标题映射，以便回看历史轨迹。

## 已确认的产品决定（2026-11-08）

| 事项 | 选择 | 理由 |
| --- | --- | --- |
| 定时方式 | 一次性（带时区偏移的时间点）与周期（五段 cron 加 IANA 时区）都支持 | 用户选择；提醒与周期检查都是常见用法 |
| 界面呈现 | 聊天界面不加任何东西：触发消息不可见，只看到模型发出的正文；查看或修改都通过跟模型对话完成 | 用户选择；不新增前端页面，界面保持现有 IM 形态 |
| 闲置后台反思 | 2026-11-26 整体移除（见上文）；2026-11-08 至 2026-11-17 的历史迭代记录保留于文末 | 用户反馈目前没有实际用处，减少无用后台调用与配置复杂度 |

## 运行机制

**触发消息。** 定时任务以 `role = user`、`sender = system`、`visible = false` 的消息写入 `messages`，`content.name` 标明为 `schedule_event`。Main 是否有活只看消息尾部（视图 `main_work`），最后一条是 user 消息即有活，所以 agent loop 的三个阶段与循环主体都不需要知道“定时”这回事：触发消息照常进入请求组装、思考中被新输入打断、压缩进摘要。投递事务使用 `LockUserMessages` 与 `FOR UPDATE SKIP LOCKED`，因此多个对等后端容器同时扫描也绝不会重复投递；事务提交后调用 `Manager.Wake` 经 Redis 广播并唤醒空闲容器。

**后台定时循环。** `backend/internal/scheduler/`（`scheduler.go`）负责扫描和投递 Agent 创建的 `schedule`，由 `main.go` 启动独立循环，每 5 秒扫描一次，失败在外层记录日志，未投递的下一次扫描再试。触发精度因此在 5 秒量级。

### 定时任务

- 表 `schedules`（`00045`）：标题、到点时交给模型的上下文、可空的 `cron` 与 `timezone`（两者同时为空即一次性）、`next_run_at`。
- 工具 `schedule` 支持 `create`、`list`、`update`、`delete`，与回执同一事务提交。`update` 只改给出的字段：给 `at` 变为一次性，给 `cron` 变为周期，只改标题或上下文时保持原触发时间。参数错误返回工具错误，让模型自行修正（过去的时间、缺时区、非五段 cron、未知时区等）。
- 投递在一个事务里完成：取用户消息锁、锁定这条到期安排、插入触发消息、删除一次性安排或把周期安排推进到下一次。两步同时提交，所以不会出现消息已投而安排未推进、被下次扫描重复投递的情况；扫描之后被模型改到未来或删除的安排不再投递。
- 周期安排从投递时刻往后计算下一次，停机期间错过的多次触发合并成一次投递；触发消息里附原定时间 `scheduled_for` 与实际投递时间 `delivered_at`，由模型考虑延迟。
- 一次性安排投递后即删除，原上下文保留在触发消息里。
- 模型只能拿到 UTC 时间标记，数据库没有用户时区；固定指令要求模型从记忆中找用户时区，不知道且有影响时询问。

### 提示词

固定指令包含一段定时任务说明：何时使用 `schedule`、上下文要写成未来自己能独立执行的说明、时间须带偏移或 IANA 时区；`schedule_event` 不是用户本人的新消息。定时触发的文案集中在 `backend/internal/agent/prompt.go`。

### 闲置后台反思（已于 2026-11-26 移除，历史记录）

- 2026-11-08 最初接入单一 `reflection`（`00046`），2026-11-09 曾由 `00047_split_idle_work_minutes` 拆成按前置链依次运行的三种后台工作。2026-11-11 改为持续按间隔触发时，前置链在用户中途插话或不同档时长悬殊时会跳过或互相推迟，因此由迁移 `00053_merge_idle_work_minutes` 合并回单一的 `reflection`（`model_settings.reflection_idle_minutes`，默认 120 分钟），并将三条提示词融合为一条两步指引。
- 到期条件（原查询 `ListReflectionDue`，2026-11-11 起，2026-11-16 补充空触发去重，2026-11-17 随 `model_calls` 删除改为直接由 `messages` 表判断）：该用户发过本人消息、Main 与后台 Subagent 当前都没有活（`main_work` 与 `subagents`），Main 最新一条 `messages.created_at` 距今已满 `reflection_idle_minutes`，且最近一条主对话输入不能是一次空的 proactive（`reflection` 或历史同类触发且其后没有任何 `visible = true` 的消息）。
- 提示词（原 `ReflectionPrompt`，2026-11-14 起精简为两步，由独立包 `backend/internal/proactive/proactive.go` 投递）：阅读近期对话，1. 检查是否有用户潜在需要但未意识到的内容，有则直接告知，没有则沉默；2. 按需更新记忆。以上实现已于 2026-11-26 移除。

## 本地验证（2026-11-08，历史记录）

`00045` 与 `00046` 在本地 PostgreSQL 18 完成执行、回滚、再执行；Go 构建、`go vet` 与看板构建通过。用真实后端与 ikun Gemini 模型验证：已到期的一次性安排与逾期三天的周期安排各投递一次，一次性安排被删除，周期安排推进到下一个上海时间 9:00，Main 收到后回复并说明了延迟；`schedule` 工具的创建、修改、列出、删除与各类参数错误均按预期返回。闲置反思在测试对话闲置后投递一次，Main 读取记忆根、新建个人生活主题并更新索引，随后不写正文结束，没有推送消息或记为失败；之后持续扫描未再次触发。没有新增测试文件，测试数据已清理，没有推送或部署。
