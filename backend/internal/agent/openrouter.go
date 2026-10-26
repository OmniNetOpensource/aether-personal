package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      json.RawMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     *int32           `json:"prompt_tokens"`
		CompletionTokens *int32           `json:"completion_tokens"`
		Cost             *decimal.Decimal `json:"cost"`
	} `json:"usage"`
}

func (r *Runner) callModel(ctx context.Context, address agentAddress, contextMessages []json.RawMessage, stepIDs ...uuid.UUID) (response chatResponse, retryable bool, err error) {
	model := r.model(address)
	outputLimit := r.settings.MaxOutputTokens
	if outputLimit == 0 {
		outputLimit = config.DefaultMaxOutputTokens
	}
	if len(stepIDs) > 0 {
		settings, loadErr := modelsettings.Load(ctx, database.New(r.databasePool), r.settings)
		if loadErr != nil {
			return response, false, fmt.Errorf("load active model settings: %w", loadErr)
		}
		model = settings.MainModel
		outputLimit = settings.MainOutputLimit
		if address.subagentID != nil {
			model = settings.EffectiveSubagentModel
			outputLimit = settings.SubagentOutputLimit
		}
	}
	prepared := r.agentRequest(address, contextMessages, model, outputLimit)
	var stepID uuid.UUID
	if len(stepIDs) > 0 {
		stepID = stepIDs[0]
	}
	return r.requestModel(ctx, prepared, database.StartModelCallParams{StepID: stepID, Purpose: "agent"})
}

type modelRequest struct {
	Reasoning   map[string]any    `json:"reasoning,omitempty"`
	RecapUserID uuid.UUID         `json:"-"`
	Model       string            `json:"model"`
	Messages    []json.RawMessage `json:"messages"`
	Tools       []json.RawMessage `json:"tools,omitempty"`
	MaxTokens   int32             `json:"max_tokens"`
}

func (r *Runner) agentRequest(address agentAddress, contextMessages []json.RawMessage, model string, outputLimit int32) modelRequest {
	prompt, tools := mainPrompt, mainTools
	if address.subagentID != nil {
		prompt, tools = subagentPrompt, subagentTools
	}
	if r.sandbox != nil {
		prompt += sandboxPrompt
		tools = append(append([]json.RawMessage{}, tools...), sandboxExecutionTools...)
	}
	system, _ := json.Marshal(map[string]string{"role": "system", "content": prompt})
	messages := []json.RawMessage{system}
	observationsLeft := 4
	retained := make([]json.RawMessage, len(contextMessages))
	copy(retained, contextMessages)
	for index := len(retained) - 1; index >= 0; index-- {
		var observation struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(retained[index], &observation) == nil && observation.Name == "sandbox_observation" {
			if observationsLeft == 0 {
				retained[index] = json.RawMessage(`{"role":"user","name":"sandbox_observation","content":"Older desktop image omitted. Take a fresh screenshot before acting on the desktop."}`)
			} else {
				observationsLeft--
			}
		}
	}
	for _, message := range retained {
		var summary struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		if json.Unmarshal(message, &summary) == nil && summary.Name == "context_summary" {
			message, _ = json.Marshal(map[string]string{"role": "user", "name": "context_summary", "content": summary.Content})
		}
		messages = append(messages, message)
	}

	return modelRequest{Model: model, Messages: messages, Tools: tools, MaxTokens: outputLimit}
}

func (r *Runner) requestModel(ctx context.Context, prepared modelRequest, journal database.StartModelCallParams) (response chatResponse, retryable bool, err error) {
	body, err := json.Marshal(prepared)
	if err != nil {
		return response, false, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return chatResponse{}, false, err
	}
	request.Header.Set("Authorization", "Bearer "+r.settings.OpenRouterAPIKey)
	request.Header.Set("Content-Type", "application/json")

	var callID uuid.UUID
	if prepared.RecapUserID != uuid.Nil {
		callID, err = database.New(r.databasePool).StartRecapModelCall(ctx, database.StartRecapModelCallParams{
			UserID: prepared.RecapUserID, RequestedModel: prepared.Model, RequestBody: body,
		})
		if err != nil {
			return response, false, fmt.Errorf("start recap call journal: %w", err)
		}
	} else if journal.StepID != uuid.Nil {
		journal.RequestedModel, journal.RequestBody = prepared.Model, body
		callID, err = database.New(r.databasePool).StartModelCall(ctx, journal)
		if err != nil {
			return response, false, fmt.Errorf("start model call journal: %w", err)
		}
	}
	startedAt := time.Now()
	outcome := "transport_error"
	var status *int32
	var capturedResponseBody []byte
	var responseTruncated bool
	defer func() {
		if response.Usage.PromptTokens != nil && *response.Usage.PromptTokens < 0 {
			response.Usage.PromptTokens = nil
		}
		if response.Usage.CompletionTokens != nil && *response.Usage.CompletionTokens < 0 {
			response.Usage.CompletionTokens = nil
		}
		if response.Usage.Cost != nil && response.Usage.Cost.IsNegative() {
			response.Usage.Cost = nil
		}
		if callID == uuid.Nil {
			return
		}
		duration := time.Since(startedAt).Milliseconds()
		var durationMS *int64 = &duration
		if ctx.Err() != nil {
			outcome, durationMS = "interrupted", nil
		}
		var failure *string
		if outcome != "succeeded" {
			failure = &outcome
		}
		toolNames := []string{}
		if len(response.Choices) > 0 {
			var message assistantMessage
			if json.Unmarshal(response.Choices[0].Message, &message) == nil {
				for _, call := range message.ToolCalls {
					name := call.Function.Name
					switch name {
					case "send_message", "assign_subagents", "search", "fetch_url", "run_command", "get_command", "stop_command", "read_file", "write_file", "desktop":
					default:
						name = "other"
					}
					toolNames = append(toolNames, name)
				}
			}
		}
		var responseModel, generationID *string
		if response.Model != "" && utf8.RuneCountInString(response.Model) <= 256 {
			responseModel = &response.Model
		}
		if response.ID != "" && utf8.RuneCountInString(response.ID) <= 256 {
			generationID = &response.ID
		}
		persistCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		rows, finishErr := database.New(r.databasePool).FinishModelCall(persistCtx, database.FinishModelCallParams{
			ID: callID, Outcome: outcome, HttpStatus: status, DurationMs: durationMS,
			ResponseModel: responseModel, ProviderGenerationID: generationID,
			InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens,
			Cost: response.Usage.Cost, ToolNames: toolNames, FailureKind: failure,
			ResponseBody: capturedResponseBody, ResponseTruncated: responseTruncated,
		})
		if finishErr != nil {
			err, retryable = fmt.Errorf("finish model call journal: %w", finishErr), false
		} else if rows != 1 {
			err, retryable = fmt.Errorf("model call execution ownership was recovered"), false
		}
		if journal.StepID != uuid.Nil {
			r.recordActivity(persistCtx, journal.StepID, "model", outcome, journal.Purpose)
		}
	}()
	if journal.StepID != uuid.Nil {
		if _, countErr := database.New(r.databasePool).CountRecapRequest(ctx, journal.StepID); countErr != nil {
			slog.Warn("count recap model request failed", "error", countErr)
		}
		r.recordActivity(ctx, journal.StepID, "model", "started", journal.Purpose)
		r.wakeRecap()
	}
	httpResponse, err := r.httpClient.Do(request)
	if err != nil {
		return response, true, err
	}
	defer httpResponse.Body.Close()
	statusCode := int32(httpResponse.StatusCode)
	status = &statusCode
	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, (8<<20)+1))
	capturedResponseBody = responseBody
	responseTruncated = len(responseBody) > 8<<20
	if responseTruncated {
		capturedResponseBody = responseBody[:8<<20]
	}
	if err != nil {
		return response, true, err
	}

	// Preserve provider usage independently of assistant validity and later tool execution.
	outcome = "invalid_response"
	if len(responseBody) <= 8<<20 {
		_ = json.Unmarshal(responseBody, &response)
	}
	if httpResponse.StatusCode != http.StatusOK {
		outcome = "http_error"
		retryable := httpResponse.StatusCode == http.StatusTooManyRequests || httpResponse.StatusCode >= 500
		return response, retryable, fmt.Errorf("openrouter returned %d: %.500s", httpResponse.StatusCode, responseBody)
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return response, false, fmt.Errorf("decode openrouter response: %w", err)
	}
	if len(response.Choices) == 0 {
		return response, false, fmt.Errorf("openrouter returned no choices: %.500s", responseBody)
	}
	var message struct {
		Role string `json:"role"`
	}
	if len(responseBody) > 8<<20 || json.Unmarshal(response.Choices[0].Message, &message) != nil || message.Role != "assistant" {
		return response, false, fmt.Errorf("openrouter returned invalid assistant message")
	}
	outcome = "succeeded"
	return response, false, nil
}
