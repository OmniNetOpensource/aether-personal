// 本文件实现管理看板的 Agent 运行时间线：把某个 Agent 的消息与未产出消息的模型请求合成按时间排列的节点，并按需读取单条消息全文。

package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	timelinePageSize  = 100
	timelineTextLimit = 4000
)

// timelineCursor 指向当前页最早的一条消息：更早一页取它之前的消息，失败请求取它之前开始的。
type timelineCursor struct {
	Sequence int64     `json:"sequence"`
	At       time.Time `json:"at"`
}

// timelineItem 是时间线上的一个节点：type 为 message 时来自消息表，为 call 时是没有产出消息的模型请求。
type timelineItem struct {
	Type             string             `json:"type"`
	At               time.Time          `json:"at"`
	ID               *uuid.UUID         `json:"id,omitempty"`
	Sequence         int64              `json:"sequence,omitempty"`
	Role             string             `json:"role,omitempty"`
	Sender           *string            `json:"sender,omitempty"`
	SenderSubagentID *uuid.UUID         `json:"sender_subagent_id,omitempty"`
	IsSummary        bool               `json:"is_summary,omitempty"`
	Name             string             `json:"name,omitempty"`
	Text             string             `json:"text,omitempty"`
	ImageCount       int                `json:"image_count,omitempty"`
	Images           []string           `json:"images,omitempty"`
	Reasoning        *timelineReasoning `json:"reasoning,omitempty"`
	ToolCalls        []timelineToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string             `json:"tool_call_id,omitempty"`
	Truncated        bool               `json:"truncated,omitempty"`
	Call             json.RawMessage    `json:"call,omitempty"`
}

// timelineReasoning 的 state：text 有可读推理，encrypted 只返回了加密推理，none 没有返回推理。
type timelineReasoning struct {
	State string `json:"state"`
	Text  string `json:"text,omitempty"`
}

type timelineToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// storedMessage 覆盖消息表 content 中会出现的字段：OpenRouter 消息、Gemini 原生 parts 以及带截图的观察消息。
type storedMessage struct {
	Name             string          `json:"name"`
	Content          json.RawMessage `json:"content"`
	Reasoning        string          `json:"reasoning"`
	ReasoningDetails []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Summary string `json:"summary"`
	} `json:"reasoning_details"`
	GeminiContent struct {
		Parts []struct {
			Text    string `json:"text"`
			Thought bool   `json:"thought"`
		} `json:"parts"`
	} `json:"gemini_content"`
	ToolCalls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
	ToolCallID string `json:"tool_call_id"`
}

// timelineMessage 把一条消息整理成节点；limit 为 0 表示返回全文和图片，否则长文本截断、图片只计数。
func timelineMessage(role string, content []byte, limit int) timelineItem {
	item := timelineItem{Type: "message", Role: role}
	var message storedMessage
	_ = json.Unmarshal(content, &message)
	item.Name, item.ToolCallID = message.Name, message.ToolCallID
	clip := func(text string) string {
		characters := []rune(text)
		if limit == 0 || len(characters) <= limit {
			return text
		}
		item.Truncated = true
		return string(characters[:limit])
	}

	// content 可能是字符串，也可能是 text / image_url 片段数组（截图观察）。
	var text string
	if json.Unmarshal(message.Content, &text) != nil {
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		_ = json.Unmarshal(message.Content, &parts)
		texts := []string{}
		for _, part := range parts {
			switch part.Type {
			case "text":
				texts = append(texts, part.Text)
			case "image_url":
				item.ImageCount++
				if limit == 0 {
					item.Images = append(item.Images, part.ImageURL.URL)
				} else {
					item.Truncated = true
				}
			}
		}
		text = strings.Join(texts, "\n")
	}
	item.Text = clip(text)

	if role == "assistant" {
		// OpenRouter 放在 reasoning / reasoning_details，Gemini 放在 thought parts；只有加密内容时无法展示原文。
		reasoning, encrypted := message.Reasoning, false
		if reasoning == "" {
			texts := []string{}
			for _, detail := range message.ReasoningDetails {
				switch detail.Type {
				case "reasoning.text":
					texts = append(texts, detail.Text)
				case "reasoning.summary":
					texts = append(texts, detail.Summary)
				case "reasoning.encrypted":
					encrypted = true
				}
			}
			for _, part := range message.GeminiContent.Parts {
				if part.Thought && part.Text != "" {
					texts = append(texts, part.Text)
				}
			}
			reasoning = strings.Join(texts, "\n")
		}
		item.Reasoning = &timelineReasoning{State: "none"}
		if reasoning != "" {
			item.Reasoning = &timelineReasoning{State: "text", Text: clip(reasoning)}
		} else if encrypted {
			item.Reasoning.State = "encrypted"
		}
		for _, call := range message.ToolCalls {
			item.ToolCalls = append(item.ToolCalls, timelineToolCall{ID: call.ID, Name: call.Function.Name, Arguments: clip(call.Function.Arguments)})
		}
	}
	return item
}

// parseTimelineAgent 解析看板传来的用户与 Agent；agent 为 main 时返回空的 Subagent ID。
func parseTimelineAgent(r *http.Request) (uuid.UUID, *uuid.UUID, bool) {
	userID, err := uuid.Parse(r.URL.Query().Get("user_id"))
	if err != nil {
		return userID, nil, false
	}
	agent := r.URL.Query().Get("agent")
	if agent == "main" {
		return userID, nil, true
	}
	subagentID, err := uuid.Parse(agent)
	return userID, &subagentID, err == nil
}

// adminTimeline 返回一页时间线：默认最新一页，before 游标向更早翻页；首页附带最近一次请求使用的系统提示词。
func (s *server) adminTimeline(w http.ResponseWriter, r *http.Request) {
	userID, subagentID, ok := parseTimelineAgent(r)
	if !ok {
		http.Error(w, "user_id and agent (main or a Subagent UUID) are required", http.StatusBadRequest)
		return
	}
	var cursor *timelineCursor
	if value := r.URL.Query().Get("before"); value != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		cursor = &timelineCursor{}
		if len(value) > 256 || err != nil || json.Unmarshal(decoded, cursor) != nil || cursor.At.IsZero() {
			http.Error(w, "invalid before cursor", http.StatusBadRequest)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.beginAdminRead(ctx)
	if err != nil {
		internalError(w, "begin admin timeline failed", err)
		return
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)

	// 多取一条用来判断是否还有更早的消息；结果按 sequence 从新到旧返回。
	messageParams := database.ListAdminTimelineMessagesParams{UserID: userID, SubagentID: subagentID, PageLimit: timelinePageSize + 1}
	callParams := database.ListAdminTimelineFailedCallsParams{UserID: userID, SubagentID: subagentID}
	if cursor != nil {
		messageParams.BeforeSequence, callParams.ToAt = &cursor.Sequence, &cursor.At
	}
	messages, err := queries.ListAdminTimelineMessages(ctx, messageParams)
	if err != nil {
		internalError(w, "read admin timeline messages failed", err)
		return
	}
	var nextCursor *string
	if len(messages) > timelinePageSize {
		messages = messages[:timelinePageSize]
		oldest := messages[len(messages)-1]
		callParams.FromAt = &oldest.CreatedAt
		data, _ := json.Marshal(timelineCursor{Sequence: oldest.Sequence, At: oldest.CreatedAt})
		value := base64.RawURLEncoding.EncodeToString(data)
		nextCursor = &value
	}
	slices.Reverse(messages)
	calls, err := queries.ListAdminTimelineFailedCalls(ctx, callParams)
	if err != nil {
		internalError(w, "read admin timeline calls failed", err)
		return
	}
	slices.Reverse(calls)
	var systemPrompt any
	if cursor == nil {
		prompt, err := queries.GetAdminTimelineSystemPrompt(ctx, database.GetAdminTimelineSystemPromptParams{UserID: userID, SubagentID: subagentID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			internalError(w, "read admin timeline system prompt failed", err)
			return
		}
		if err == nil {
			systemPrompt = map[string]any{"text": prompt.Prompt, "from_call_at": prompt.StartedAt}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		internalError(w, "commit admin timeline failed", err)
		return
	}

	// 消息按 sequence 排序；别人发来的消息到达早、读取晚，因此用不回退的时间作为合并失败请求的位置。
	items := []timelineItem{}
	var position time.Time
	nextCall := 0
	for _, message := range messages {
		if message.CreatedAt.After(position) {
			position = message.CreatedAt
		}
		for nextCall < len(calls) && calls[nextCall].StartedAt.Before(position) {
			items = append(items, timelineItem{Type: "call", At: calls[nextCall].StartedAt, Call: calls[nextCall].Call})
			nextCall++
		}
		item := timelineMessage(message.Role, message.Content, timelineTextLimit)
		item.ID, item.Sequence, item.At = &message.ID, message.Sequence, message.CreatedAt
		item.Sender, item.SenderSubagentID, item.IsSummary = message.Sender, message.SenderSubagentID, message.IsSummary
		if len(message.Call) > 0 {
			item.Call = message.Call
		}
		items = append(items, item)
	}
	for _, call := range calls[nextCall:] {
		items = append(items, timelineItem{Type: "call", At: call.StartedAt, Call: call.Call})
	}
	writeAdminJSON(w, map[string]any{"items": items, "next_cursor": nextCursor, "system_prompt": systemPrompt})
}

// adminTimelineMessage 返回单条消息的全文与图片，供时间线展开被截断的节点。
func (s *server) adminTimelineMessage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid message ID", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	message, err := s.queries.GetAdminTimelineMessage(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}
	if err != nil {
		internalError(w, "read admin timeline message failed", err)
		return
	}
	writeAdminJSON(w, timelineMessage(message.Role, message.Content, 0))
}
