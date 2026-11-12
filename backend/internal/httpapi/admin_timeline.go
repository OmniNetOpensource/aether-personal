// 本文件实现管理看板的运行时间线：支持按 Main 或指定后台 Subagent 把消息与未产出消息的模型请求合成按时间排列的节点，并按需读取单条消息全文。

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

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	timelinePageSize  = 100
	timelineTextLimit = 4000
)

// timelineCursor 指向当前页最早的节点；消息与失败请求共用排序位置。
type timelineCursor struct {
	Sequence int64     `json:"sequence"`
	At       time.Time `json:"at"`
	ID       uuid.UUID `json:"id"`
}

// timelineItem 是时间线上的一个节点：type 为 message 时来自消息表，为 call 时是没有产出消息的模型请求。
type timelineItem struct {
	Type       string             `json:"type"`
	At         time.Time          `json:"at"`
	ID         *uuid.UUID         `json:"id,omitempty"`
	Sequence   int64              `json:"sequence,omitempty"`
	Role       string             `json:"role,omitempty"`
	Sender     *string            `json:"sender,omitempty"`
	IsSummary  bool               `json:"is_summary,omitempty"`
	Name       string             `json:"name,omitempty"`
	Text       string             `json:"text,omitempty"`
	ImageCount int                `json:"image_count,omitempty"`
	Images     []string           `json:"images,omitempty"`
	Reasoning  *timelineReasoning `json:"reasoning,omitempty"`
	ToolCalls  []timelineToolCall `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
	Truncated  bool               `json:"truncated,omitempty"`
	Call       json.RawMessage    `json:"call,omitempty"`
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

// storedMessage 覆盖消息表 content 中会出现的字段：OpenRouter、Gemini 与 Claude 的消息结构及工具调用。
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
	ClaudeContent []struct {
		Type string `json:"type"`
	} `json:"claude_content"`
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
		// OpenRouter 放在 reasoning / reasoning_details，Gemini 放在 thought parts，Claude 加密块为 redacted_thinking；只有加密内容时无法展示原文。
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
			for _, block := range message.ClaudeContent {
				if block.Type == "redacted_thinking" {
					encrypted = true
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

// adminTimeline 按用户、可选 Subagent、时间、模型与结果查询运行过程，消息和失败请求使用同一游标分页。
func (s *server) adminTimeline(w http.ResponseWriter, r *http.Request) {
	filter, err := parseAdminFilters(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if filter.UserID == nil {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}
	params := database.ListAdminTimelineParams{
		UserID: *filter.UserID, SubagentID: filter.SubagentID, FromAt: filter.From, ToAt: filter.To,
		Model: filter.Model, Outcome: filter.Outcome, PageLimit: timelinePageSize + 1,
	}
	// 游标只携带排序位置；时间窗口与筛选值由客户端在每一页原样传回。
	if value := r.URL.Query().Get("before"); value != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		var cursor timelineCursor
		if len(value) > 256 || err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.At.IsZero() || cursor.ID == uuid.Nil {
			http.Error(w, "invalid before cursor", http.StatusBadRequest)
			return
		}
		params.CursorAt, params.CursorID, params.CursorSequence = &cursor.At, &cursor.ID, cursor.Sequence
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var subagentSummary *adminSubagent
	if filter.SubagentID != nil {
		subagent, err := s.queries.GetSubagentForUser(ctx, database.GetSubagentForUserParams{UserID: *filter.UserID, ID: *filter.SubagentID})
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "subagent not found", http.StatusNotFound)
			return
		}
		if err != nil {
			internalError(w, "read admin subagent failed", err)
			return
		}
		subagentSummary = &adminSubagent{
			ID:         subagent.ID,
			Title:      subagent.Title,
			Status:     subagent.Status,
			Error:      subagent.Error,
			StartedAt:  subagent.StartedAt,
			FinishedAt: subagent.FinishedAt,
		}
	}
	rows, err := s.queries.ListAdminTimeline(ctx, params)
	if err != nil {
		internalError(w, "read admin timeline failed", err)
		return
	}
	var nextCursor *string
	if len(rows) > timelinePageSize {
		rows = rows[:timelinePageSize]
		oldest := rows[len(rows)-1]
		data, _ := json.Marshal(timelineCursor{Sequence: oldest.Sequence, At: oldest.OrderAt, ID: oldest.ID})
		value := base64.RawURLEncoding.EncodeToString(data)
		nextCursor = &value
	}
	// API 沿用从旧到新的返回顺序；前端统一倒序显示，加载更早记录时直接追加。
	slices.Reverse(rows)
	items := make([]timelineItem, 0, len(rows))
	for _, row := range rows {
		if row.Type == "call" {
			items = append(items, timelineItem{Type: "call", At: row.At, Call: row.Call})
			continue
		}
		item := timelineMessage(row.Role, row.Content, timelineTextLimit)
		item.ID, item.Sequence, item.At = &row.ID, row.Sequence, row.At
		item.Sender, item.IsSummary, item.Call = row.Sender, row.IsSummary, row.Call
		items = append(items, item)
	}
	// 固定指令由代码注入，首页展示对应角色的当前部署原文。
	var systemPrompt any
	if r.URL.Query().Get("before") == "" {
		instructions := agent.MainInstructions
		if filter.SubagentID != nil {
			instructions = agent.SubagentInstructions
		}
		systemPrompt = map[string]any{"text": instructions}
	}
	response := map[string]any{"items": items, "next_cursor": nextCursor, "system_prompt": systemPrompt}
	if subagentSummary != nil {
		response["subagent"] = subagentSummary
	}
	writeAdminJSON(w, response)
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
	item := timelineMessage(message.Role, message.Content, 0)
	// 库内图片是私有 OSS 引用；展开时重新签名，避免把过期链接持久化。
	for index, reference := range item.Images {
		if !strings.HasPrefix(reference, "oss://") {
			continue
		}
		preview, err := s.images.Preview(ctx, reference)
		if err != nil {
			internalError(w, "sign timeline image failed", err)
			return
		}
		item.Images[index] = preview
	}
	writeAdminJSON(w, item)
}
