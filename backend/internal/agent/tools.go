// 本文件登记全部 Agent 工具：模型看到的定义、Main 与 Subagent 是否可用、
// 执行是在事务外还是与回执同一事务，以及调用哪个实现。联网请求与定时安排的具体逻辑仍在各自文件里。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/scheduler"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// agentTool 是一条工具登记。同名可以有多行：send_message 按身份发给模型的定义不同，执行函数相同。
type agentTool struct {
	name       string
	definition json.RawMessage
	main       bool
	subagent   bool
	// outsideTransaction 为 true 时在事务外执行，回执单独提交。默认与回执在同一事务里提交。
	outsideTransaction bool
	execute            func(ctx context.Context, runner *Runner, queries *database.Queries, address agentAddress, arguments string) (string, error)
}

// agentTools 是工具的唯一登记表。新增工具时改这里，并同步 admin.sql 里的已知工具名。
var agentTools = []agentTool{
	{
		name: "send_message",
		definition: json.RawMessage(`{
  "type": "function",
  "function": {
    "name": "send_message",
    "description": "Provide follow-up instructions to an existing subagent (to=its UUID). Replies to the user are written directly as assistant text, never through this tool.",
    "parameters": {
      "type": "object",
      "properties": {
        "to": {"type": "string", "description": "An existing subagent UUID returned by assign_subagents"},
        "text": {"type": "string", "description": "The instruction the subagent will read."}
      },
      "required": ["to", "text"]
    }
  }
}`),
		main:    true,
		execute: executeSendMessage,
	},
	{
		name:       "send_message",
		definition: json.RawMessage(`{"type":"function","function":{"name":"send_message","description":"Send findings, source URLs, progress or blockers to Main. After reporting the completed task, stop using tools.","parameters":{"type":"object","additionalProperties":false,"properties":{"to":{"type":"string","enum":["main"]},"text":{"type":"string"}},"required":["to","text"]}}}`),
		subagent:   true,
		execute:    executeSendMessage,
	},
	{
		name:       "assign_subagents",
		definition: json.RawMessage(`{"type":"function","function":{"name":"assign_subagents","description":"Create independent background subagents for the supplied task descriptions. Returns subagent_ids in the same order immediately. They will send results back as messages; do not poll. At most three run concurrently per user and the rest wait in the database queue.","parameters":{"type":"object","additionalProperties":false,"properties":{"tasks":{"type":"array","minItems":1,"items":{"type":"string"}}},"required":["tasks"]}}}`),
		main:       true,
		execute:    executeAssignSubagents,
	},
	{
		name:       "schedule",
		definition: json.RawMessage(`{"type":"function","function":{"name":"schedule","description":"Arrange future context delivery to this user's Main. Supports one-time timestamps or recurring five-field cron rules in an explicit IANA timezone. Create requires title, context, timezone and exactly one of at or cron. List returns up to 50 items and next_before for pagination. Update requires id and changed fields; enabled=false pauses, enabled=true resumes. Delete requires id. Pausing/deleting does not cancel context already delivered. A triggered status means delivered to Main, not task completed. Missed recurring triggers are combined into one delivery after downtime.","parameters":{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["create","list","update","delete"]},"id":{"type":"string","description":"Schedule UUID for update or delete, from create or list."},"before":{"type":"string","description":"For list only: previous page's next_before UUID."},"title":{"type":"string","minLength":1,"maxLength":160},"context":{"type":"string","minLength":1,"maxLength":24000,"description":"Self-contained task instructions, necessary background, source links, constraints and expected delivery."},"at":{"type":"string","description":"One-time RFC3339 timestamp with explicit offset, in the future. Mutually exclusive with cron."},"cron":{"type":"string","description":"Recurring rule: minute hour day-of-month month day-of-week, e.g. 0 9 * * 1-5. No seconds or @every. Mutually exclusive with at."},"timezone":{"type":"string","description":"Explicit IANA timezone such as Asia/Shanghai or UTC. Required for create."},"enabled":{"type":"boolean","description":"false to pause future triggers; true to resume."}},"required":["action"]}}}`),
		main:       true,
		subagent:   true,
		execute: func(ctx context.Context, _ *Runner, queries *database.Queries, address agentAddress, arguments string) (string, error) {
			return scheduler.ExecuteTool(ctx, queries, address.userID, arguments)
		},
	},
	{
		name:               "search_message",
		definition:         json.RawMessage(`{"type":"function","function":{"name":"search_message","description":"Search this user's message history in the messages table (all message types). Main searches its own conversation plus all of this user's subagent records; a subagent searches only its own record. keyword matches exact words or phrases such as names, IDs or quoted text; query searches by meaning; provide at least one of them. who limits the speaker: user, main, subagent or any. Returns message IDs, speakers and excerpts; read a result with read_message before relying on its details.","parameters":{"type":"object","additionalProperties":false,"properties":{"keyword":{"type":"string","description":"Exact word or phrase to match literally."},"query":{"type":"string","description":"Natural-language description of what to find, matched by meaning."},"who":{"type":"string","enum":["any","user","main","subagent"],"default":"any"},"limit":{"type":"integer","minimum":1,"maximum":20,"default":10}},"required":[]}}}`),
		main:               true,
		subagent:           true,
		outsideTransaction: true,
		execute: func(ctx context.Context, runner *Runner, _ *database.Queries, address agentAddress, arguments string) (string, error) {
			return runner.executeSearchMessage(ctx, address, arguments)
		},
	},
	{
		name:       "read_message",
		definition: json.RawMessage(`{"type":"function","function":{"name":"read_message","description":"Read the full text of one message returned by search_message, with its agent, speaker and time. Main may read any message of this user; a subagent may only read messages in its own record.","parameters":{"type":"object","additionalProperties":false,"properties":{"message_id":{"type":"string","description":"A message UUID returned by search_message"}},"required":["message_id"]}}}`),
		main:       true,
		subagent:   true,
		execute:    executeReadMessage,
	},
	{
		name:               "search",
		definition:         json.RawMessage(`{"type":"function","function":{"name":"search","description":"Search Google, Weixin Official Account articles or RedNote notes. Returns titles, URLs and descriptions. Read relevant pages with fetch_url before using them as evidence.","parameters":{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string"},"platform":{"type":"string","enum":["google","weixin","rednote"],"default":"google"}},"required":["query"]}}}`),
		main:               true,
		subagent:           true,
		outsideTransaction: true,
		execute: func(ctx context.Context, runner *Runner, _ *database.Queries, _ agentAddress, arguments string) (string, error) {
			return runner.webTools.search(ctx, json.RawMessage(arguments))
		},
	},
	{
		name:               "search_twitter",
		definition:         json.RawMessage(`{"type":"function","function":{"name":"search_twitter","description":"Search public X (Twitter) posts via EveryInfra. Returns post text, source URLs, authors, publication times and available engagement counts. Query supports X search operators such as from:, since: and until:. Defaults to Top results; latest is only a preference and does not guarantee chronological order. Results may be fewer than requested and are not a complete archive. Cite original post URLs; missing counts are unknown, not zero.","parameters":{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1,"maximum":200,"default":25},"sort":{"type":"string","enum":["top","latest"],"default":"top"}},"required":["query"]}}}`),
		main:               true,
		subagent:           true,
		outsideTransaction: true,
		execute: func(ctx context.Context, runner *Runner, _ *database.Queries, _ agentAddress, arguments string) (string, error) {
			return runner.webTools.searchTwitter(ctx, json.RawMessage(arguments))
		},
	},
	{
		name:               "fetch_url",
		definition:         json.RawMessage(`{"type":"function","function":{"name":"fetch_url","description":"Read text content from an HTTP(S) webpage, including WeChat articles, Xiaohongshu/RedNote notes and Reddit posts. Keep the source URL when reporting findings.","parameters":{"type":"object","additionalProperties":false,"properties":{"url":{"type":"string"}},"required":["url"]}}}`),
		main:               true,
		subagent:           true,
		outsideTransaction: true,
		execute: func(ctx context.Context, runner *Runner, _ *database.Queries, _ agentAddress, arguments string) (string, error) {
			return runner.webTools.fetchURL(ctx, json.RawMessage(arguments))
		},
	},
}

// toolDefinitions 按调用者身份选出要发给模型的定义，顺序与登记表一致。
func toolDefinitions(address agentAddress) []json.RawMessage {
	definitions := make([]json.RawMessage, 0, len(agentTools))
	for _, tool := range agentTools {
		if (address.subagentID != nil && tool.subagent) || (address.subagentID == nil && tool.main) {
			definitions = append(definitions, tool.definition)
		}
	}
	return definitions
}

// toolByName 返回登记表中该名字的第一条。同名行的执行函数相同，取第一条即可。
func toolByName(name string) (agentTool, bool) {
	for _, tool := range agentTools {
		if tool.name == name {
			return tool, true
		}
	}
	return agentTool{}, false
}

// recordedToolName 把模型点出的工具名收成调用日志认识的名字，未登记的归为 other。
func recordedToolName(name string) string {
	if _, ok := toolByName(name); ok {
		return name
	}
	return "other"
}

// executeDatabaseTool 执行与回执同一事务的工具。未登记的名字返回工具错误，让模型可以改口。
func (r *Runner) executeDatabaseTool(ctx context.Context, queries *database.Queries, address agentAddress, call toolCall) (string, error) {
	tool, ok := toolByName(call.Function.Name)
	if !ok || tool.outsideTransaction {
		return "Error: tool not available to this agent: " + call.Function.Name, nil
	}
	return tool.execute(ctx, r, queries, address, call.Function.Arguments)
}

// executeSendMessage 把一段文本投递给对方 Agent。Main 只能发给自己的 Subagent，Subagent 只能发给 Main。
func executeSendMessage(ctx context.Context, _ *Runner, queries *database.Queries, address agentAddress, arguments string) (string, error) {
	call := toolCall{}
	call.Function.Name = "send_message"
	call.Function.Arguments = arguments
	parsed, err := parseSendMessage(call)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	if address.subagentID != nil {
		if parsed.To != "main" {
			return "Error: subagents can only message main", nil
		}
		text := fmt.Sprintf("Message from subagent %s:\n%s", address.subagentID, parsed.Text)
		err := deliverText(ctx, queries, agentAddress{userID: address.userID}, "subagent", address.subagentID, text)
		return "delivered", err
	}
	if parsed.To == "user" {
		return "Error: main replies to the user directly in assistant text; send_message only sends instructions to an existing subagent", nil
	}
	subagentID, err := uuid.Parse(parsed.To)
	if err != nil {
		return "Error: to must be an existing subagent UUID", nil
	}
	_, err = queries.GetSubagent(ctx, database.GetSubagentParams{UserID: address.userID, ID: subagentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "Error: subagent does not belong to this user", nil
	}
	if err != nil {
		return "", err
	}
	err = deliverText(ctx, queries, agentAddress{userID: address.userID, subagentID: &subagentID}, "main", nil, "Message from Main:\n"+parsed.Text)
	return "delivered", err
}

// executeAssignSubagents 为每段任务说明创建一个 Subagent，并把任务写成它的第一条消息。
func executeAssignSubagents(ctx context.Context, _ *Runner, queries *database.Queries, address agentAddress, arguments string) (string, error) {
	if address.subagentID != nil {
		return "Error: only main can assign subagents", nil
	}
	var parsed struct {
		Tasks []string `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil || len(parsed.Tasks) == 0 {
		return "Error: tasks must be a non-empty array of task descriptions", nil
	}
	for _, task := range parsed.Tasks {
		if strings.TrimSpace(task) == "" {
			return "Error: task descriptions must not be empty", nil
		}
	}
	ids := make([]uuid.UUID, 0, len(parsed.Tasks))
	for _, task := range parsed.Tasks {
		id, err := queries.CreateSubagent(ctx, address.userID)
		if err != nil {
			return "", err
		}
		if err := deliverText(ctx, queries, agentAddress{userID: address.userID, subagentID: &id}, "main", nil, "Task from Main:\n"+task); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	result, err := json.Marshal(struct {
		SubagentIDs []uuid.UUID `json:"subagent_ids"`
	}{ids})
	return string(result), err
}

// deliverText 把一段文本写成接收方的一条用户消息，并记下是谁发的。
func deliverText(ctx context.Context, queries *database.Queries, recipient agentAddress, sender string, senderSubagentID *uuid.UUID, text string) error {
	content, err := json.Marshal(map[string]string{"role": "user", "content": text})
	if err != nil {
		return err
	}
	return queries.DeliverAgentMessage(ctx, database.DeliverAgentMessageParams{UserID: recipient.userID, SubagentID: recipient.subagentID, Content: content, Sender: &sender, SenderSubagentID: senderSubagentID})
}
