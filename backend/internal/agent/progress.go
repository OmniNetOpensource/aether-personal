// 本文件负责 Main 的进度提醒：连续多轮只调用工具、没有用户可见正文时，往模型上下文插入一条系统消息，
// 让它先写一句进度再继续。这条消息不展示给用户。

package agent

import (
	"context"
	"encoding/json"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// silentToolRoundsBeforeReminder 是连续多少个只调工具、没有用户可见正文的 Main 步骤后插入一次提醒。
const silentToolRoundsBeforeReminder = 5

// maxProgressRemindersPerSilence 限制同一次沉默里最多提醒几次。一直不写正文时，再插下去容易被当成工具结果里的注入。
const maxProgressRemindersPerSilence = 3

// progressReminder 是插入上下文的提醒原文。用整段文字识别已经插入过的提醒，避免再数成一轮工具。
const progressReminder = `Runtime progress check: several tool rounds have passed with no assistant text, so the user has not heard from you. In this reply, write a short user-facing update about what you have learned and what you are doing next, then continue the work. You may call tools in the same reply. Do not end the task. Do not repeat an update you already sent. A progress update does not replace the final result.`

// progressReminderMessage 是写进消息表、也发给模型的那一条系统消息。
func progressReminderMessage() json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"role": "system", "content": progressReminder})
	return raw
}

// progressReminderDue 从最近一条消息往回数。只数最近一条提醒之后的工具步骤；
// 再早的步骤已经催过，不再重复计算。数到用户可见正文，或用户、Subagent、日程发来的新输入就停下。
// 工具回执跳过。满 5 轮，且这次沉默里的提醒还不到 3 次，才需要再插一条。
func progressReminderDue(messages []json.RawMessage) bool {
	silentSinceReminder := 0
	reminders := 0
	seenReminder := false
	for index := len(messages) - 1; index >= 0; index-- {
		var message struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls []toolCall      `json:"tool_calls"`
		}
		if json.Unmarshal(messages[index], &message) != nil {
			continue
		}
		switch message.Role {
		case "tool":
			continue
		case "system":
			var text string
			if json.Unmarshal(message.Content, &text) == nil && text == progressReminder {
				reminders++
				seenReminder = true
			}
		case "assistant":
			// 提醒之前的工具步骤已经催过，只跳过它们；用户看见过的正文仍是这次沉默的边界。
			if len(message.ToolCalls) > 0 && AssistantTextToUser(messages[index]) == "" {
				if !seenReminder {
					silentSinceReminder++
				}
				continue
			}
			return silentSinceReminder >= silentToolRoundsBeforeReminder && reminders < maxProgressRemindersPerSilence
		default:
			return silentSinceReminder >= silentToolRoundsBeforeReminder && reminders < maxProgressRemindersPerSilence
		}
	}
	return silentSinceReminder >= silentToolRoundsBeforeReminder && reminders < maxProgressRemindersPerSilence
}

// appendProgressReminder 在这次发给模型的上下文末尾补上进度提醒，并写成一条已读系统消息，之后的请求仍能看见。
// counted 用来判断要不要提醒，outbound 是这次真正发出去的上下文；压缩时两者不同，提醒不进入摘要的输入。
// Subagent 只通过 send_message 向 Main 汇报，不在这里提醒。
func (r *Runner) appendProgressReminder(ctx context.Context, connection *pgxpool.Conn, address agentAddress, counted, outbound []json.RawMessage) ([]json.RawMessage, error) {
	if address.subagentID != nil || !progressReminderDue(counted) {
		return outbound, nil
	}
	raw := progressReminderMessage()
	err := pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, address.userID); err != nil {
			return err
		}
		// 不写 sequence 时用列默认值立刻占号，消息直接进入上下文，不会变成待读再唤醒一轮。
		return queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: address.userID, SubagentID: address.subagentID, Role: "system", Content: raw})
	})
	if err != nil {
		return nil, err
	}
	return append(outbound, raw), nil
}
