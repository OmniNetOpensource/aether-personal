package agent

import (
	"encoding/json"
	"errors"
	"strings"
)

type MessageToUser struct {
	ToolCallID string
	Text       string
}

type toolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type assistantMessage struct {
	Content   json.RawMessage `json:"content"`
	ToolCalls []toolCall      `json:"tool_calls"`
}

// MessagesToUser 与执行 send_message 共用 sendMessageText，执行时失败的调用在这里同样被跳过。
func MessagesToUser(assistantContent []byte) []MessageToUser {
	var message assistantMessage
	json.Unmarshal(assistantContent, &message)

	var messages []MessageToUser
	for _, call := range message.ToolCalls {
		arguments, err := parseSendMessage(call)
		if err == nil && arguments.To == "user" {
			messages = append(messages, MessageToUser{ToolCallID: call.ID, Text: arguments.Text})
		}
	}
	return messages
}

type sendMessageArguments struct {
	To   string `json:"to"`
	Text string `json:"text"`
}

func parseSendMessage(call toolCall) (sendMessageArguments, error) {
	if call.Function.Name != "send_message" {
		return sendMessageArguments{}, errors.New("unknown tool " + call.Function.Name)
	}
	var arguments sendMessageArguments
	if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
		return arguments, errors.New("arguments are not valid JSON")
	}
	if arguments.To == "" || strings.TrimSpace(arguments.Text) == "" {
		return arguments, errors.New("to and text must not be empty")
	}
	return arguments, nil
}
