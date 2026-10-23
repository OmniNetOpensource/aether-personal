package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/shopspring/decimal"
)

type chatResponse struct {
	Choices []struct {
		Message json.RawMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int32           `json:"prompt_tokens"`
		CompletionTokens int32           `json:"completion_tokens"`
		Cost             decimal.Decimal `json:"cost"`
	} `json:"usage"`
}

func (r *Runner) callModel(ctx context.Context, address agentAddress, contextMessages []json.RawMessage) (response chatResponse, retryable bool, err error) {
	prompt, tools := mainPrompt, mainTools
	if address.subagentID != nil {
		prompt, tools = subagentPrompt, subagentTools
	}
	messages := []any{map[string]string{"role": "system", "content": prompt}}
	for _, message := range contextMessages {
		messages = append(messages, message)
	}
	body, err := json.Marshal(map[string]any{
		"model":    r.model(address),
		"messages": messages,
		"tools":    tools,
		"usage":    map[string]bool{"include": true},
	})
	if err != nil {
		return chatResponse{}, false, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return chatResponse{}, false, err
	}
	request.Header.Set("Authorization", "Bearer "+r.settings.OpenRouterAPIKey)
	request.Header.Set("Content-Type", "application/json")

	httpResponse, err := r.httpClient.Do(request)
	if err != nil {
		return chatResponse{}, true, err
	}
	defer httpResponse.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, (8<<20)+1))
	if err != nil {
		return chatResponse{}, true, err
	}

	if httpResponse.StatusCode != http.StatusOK {
		retryable := httpResponse.StatusCode == http.StatusTooManyRequests || httpResponse.StatusCode >= 500
		return chatResponse{}, retryable, fmt.Errorf("openrouter returned %d: %.500s", httpResponse.StatusCode, responseBody)
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return chatResponse{}, false, fmt.Errorf("decode openrouter response: %w", err)
	}
	if len(response.Choices) == 0 {
		return chatResponse{}, false, fmt.Errorf("openrouter returned no choices: %.500s", responseBody)
	}
	var message struct {
		Role string `json:"role"`
	}
	if len(responseBody) > 8<<20 || json.Unmarshal(response.Choices[0].Message, &message) != nil || message.Role != "assistant" {
		return chatResponse{}, false, fmt.Errorf("openrouter returned invalid assistant message")
	}
	return response, false, nil
}
