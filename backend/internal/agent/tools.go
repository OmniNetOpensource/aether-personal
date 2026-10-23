package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Runner) executeDatabaseTool(ctx context.Context, queries *database.Queries, address agentAddress, call toolCall) (string, error) {
	switch call.Function.Name {
	case "send_message":
		arguments, err := parseSendMessage(call)
		if err != nil {
			return "Error: " + err.Error(), nil
		}
		if address.subagentID != nil {
			if arguments.To != "main" {
				return "Error: subagents can only message main", nil
			}
			text := fmt.Sprintf("Message from subagent %s:\n%s", address.subagentID, arguments.Text)
			err := deliverText(ctx, queries, agentAddress{userID: address.userID}, "subagent", address.subagentID, text)
			return "delivered", err
		}
		if arguments.To == "user" {
			return "delivered", nil
		}
		subagentID, err := uuid.Parse(arguments.To)
		if err != nil {
			return "Error: to must be user or an existing subagent UUID", nil
		}
		_, err = queries.GetSubagent(ctx, database.GetSubagentParams{UserID: address.userID, ID: subagentID})
		if errors.Is(err, pgx.ErrNoRows) {
			return "Error: subagent does not belong to this user", nil
		}
		if err != nil {
			return "", err
		}
		err = deliverText(ctx, queries, agentAddress{userID: address.userID, subagentID: &subagentID}, "main", nil, "Message from Main:\n"+arguments.Text)
		return "delivered", err
	case "assign_subagents":
		if address.subagentID != nil {
			return "Error: only main can assign subagents", nil
		}
		var arguments struct {
			Tasks []string `json:"tasks"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil || len(arguments.Tasks) == 0 {
			return "Error: tasks must be a non-empty array of task descriptions", nil
		}
		for _, task := range arguments.Tasks {
			if strings.TrimSpace(task) == "" {
				return "Error: task descriptions must not be empty", nil
			}
		}
		ids := make([]uuid.UUID, 0, len(arguments.Tasks))
		for _, task := range arguments.Tasks {
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
	default:
		return "Error: tool not available to this agent: " + call.Function.Name, nil
	}
}

func deliverText(ctx context.Context, queries *database.Queries, recipient agentAddress, sender string, senderSubagentID *uuid.UUID, text string) error {
	content, err := json.Marshal(map[string]string{"role": "user", "content": text})
	if err != nil {
		return err
	}
	return queries.DeliverAgentMessage(ctx, database.DeliverAgentMessageParams{UserID: recipient.userID, SubagentID: recipient.subagentID, Content: content, Sender: &sender, SenderSubagentID: senderSubagentID})
}
