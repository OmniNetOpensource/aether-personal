// 本文件负责解析助手消息：提取 Main 正文作为用户可见消息，并解析 Agent 间 send_message 工具调用的参数，供消息展示与交付判定共用。

package agent

import (
	"encoding/json"
	"errors"
	"strings"
)

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

// AssistantTextToUser 提取一条助手消息的正文：Main 生成的正文在整条回复生成结束后即作为一条用户消息展示，正文为空则不产生用户消息。
// 正文通常是字符串；少数供应商用分段数组表示文本，这里把其中的文本段按顺序拼起来。
func AssistantTextToUser(assistantContent []byte) string {
	var message assistantMessage
	if json.Unmarshal(assistantContent, &message) != nil {
		return ""
	}
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		return strings.TrimSpace(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(message.Content, &blocks) != nil {
		return ""
	}
	var combined strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			combined.WriteString(block.Text)
		}
	}
	return strings.TrimSpace(combined.String())
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
