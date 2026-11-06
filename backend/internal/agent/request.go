// 本文件只负责发送已组装好的模型请求：经 providers 按模型选择渠道发送，首段正文或工具调用交运行时占位；上下文组装见 context.go，业务结果的保存由上层负责，每次调用的终态日志由 model_call_journal.go 落库。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/providers"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
)

// chatResponse 与 modelRequest 是 providers 统一响应与请求的别名：Agent 内部沿用这两个名字，渠道差异不进本包。
type chatResponse = providers.Response

// modelRequest 见 chatResponse 的说明。
type modelRequest = providers.Request

// errEmptyReply 区分没有有效任务回复或工具动作的响应与传输故障，让运行时只为前者补试一次。
var errEmptyReply = errors.New("model provider returned no usable task reply or tool calls")

// sendModelRequest 发送一次已组装好的模型请求并返回结果：渠道由 providers 按模型名自动选择，本函数只负责凭证、传输与结果分类，终态日志在返回前由 finishModelCall 落库。
func (r *Runner) sendModelRequest(ctx context.Context, prepared modelRequest, journal database.StartModelCallParams, onOutput func() error) (response chatResponse, retryable bool, err error) {
	// 渠道标识留在运行时与日志里，供应商只收到自己的模型名和协议。
	resolved, err := (images.Store{Services: r.services}).ResolveMessages(ctx, prepared.Messages)
	if err != nil {
		return response, false, err
	}
	prepared.Messages = resolved
	provider := providers.ForModel(prepared.Model)
	credentials, err := r.services.Load(ctx, provider.Service())
	if err != nil {
		return response, false, err
	}
	outgoing, err := provider.PrepareRequest(ctx, credentials, prepared)
	if err != nil {
		return response, false, err
	}
	request, body := outgoing.Request, outgoing.Body

	journal.RequestedModel, journal.RequestBody = prepared.Model, body
	callID, err := database.New(r.databasePool).StartModelCall(ctx, journal)
	if err != nil {
		return response, false, fmt.Errorf("start model call journal: %w", err)
	}
	startedAt := time.Now()
	outcome := "transport_error"
	var status *int32
	var capturedResponseBody []byte
	var responseTruncated bool
	defer func() {
		if finishErr := r.finishModelCall(ctx, callID, startedAt, outcome, status, capturedResponseBody, responseTruncated, &response); finishErr != nil {
			err, retryable = finishErr, false
		}
	}()
	client := *r.modelClient
	if outgoing.PreventRedirect {
		// 自定义密钥头不跟随重定向，避免被 HTTP 客户端转发给其他主机。
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	httpResponse, err := client.Do(request)
	if err != nil {
		return response, true, err
	}
	defer httpResponse.Body.Close()
	statusCode := int32(httpResponse.StatusCode)
	status = &statusCode
	// 出错时供应商返回的通常是普通 JSON 而非事件流，整段读回即可拿到错误信息。
	if httpResponse.StatusCode != http.StatusOK {
		responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, providers.MaxResponseBytes+1))
		capturedResponseBody = responseBody
		responseTruncated = len(responseBody) > providers.MaxResponseBytes
		if responseTruncated {
			capturedResponseBody = responseBody[:providers.MaxResponseBytes]
		}
		if err != nil {
			return response, true, err
		}
		outcome = "http_error"
		retryable := httpResponse.StatusCode == http.StatusTooManyRequests || httpResponse.StatusCode >= 500
		return response, retryable, fmt.Errorf("model provider returned %d: %.500s", httpResponse.StatusCode, responseBody)
	}
	// 仅在首次有效输出时通知运行时占位；压缩请求和已经占位的请求直接继续收流。
	var outputErr error
	payloads, captured, truncated, readErr := providers.ReadSSEPayloads(httpResponse.Body, func(payload []byte) error {
		if onOutput != nil && provider.HasOutput(payload) {
			outputErr = onOutput()
			onOutput = nil
		}
		return outputErr
	})
	capturedResponseBody, responseTruncated = captured, truncated
	if truncated {
		outcome = "invalid_response"
		return response, false, fmt.Errorf("model response exceeds 8 MiB")
	}
	if outputErr != nil {
		outcome = "interrupted"
		return response, false, outputErr
	}
	if readErr != nil {
		outcome = "transport_error"
		return response, true, readErr
	}
	// Preserve provider usage independently of assistant validity and later tool execution.
	outcome = "invalid_response"
	var incomplete bool
	var decodeErr error
	response, incomplete, decodeErr = provider.DecodeStream(payloads, prepared)
	if decodeErr != nil {
		if incomplete {
			outcome = "transport_error"
		}
		return response, incomplete, decodeErr
	}
	if len(response.Choices) == 0 {
		return response, false, fmt.Errorf("model provider returned no choices")
	}
	var message struct {
		Role      string     `json:"role"`
		ToolCalls []toolCall `json:"tool_calls"`
	}
	if json.Unmarshal(response.Choices[0].Message, &message) != nil || message.Role != "assistant" {
		return response, false, fmt.Errorf("model provider returned invalid assistant message")
	}
	// 登记阶段没有回答任务，只说正文不能推进需求；实际执行允许正文或工具交付，空闲允许静默结束。
	if prepared.RequireOutput && len(message.ToolCalls) == 0 && (AssistantTextToUser(response.Choices[0].Message) == "" || (journal.Purpose == "agent" && prepared.TaskID == nil)) {
		return response, false, errEmptyReply
	}
	outcome = "succeeded"
	return response, false, nil
}
