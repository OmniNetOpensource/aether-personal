// 本文件负责历史消息的向量补算：用 OpenRouter 的 embedding 接口给尚无向量的消息逐批计算向量并存回 messages，
// 供 search_message 的语义搜索使用；补算在后台进行，接口未配置或失败不影响消息保存与聊天。

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

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
)

// embeddingModel 与 messages.embedding 的 1536 维列绑定，写死不开放配置，理由见 docs/wiki/02-data-model.md。
const embeddingModel = "google/gemini-embedding-2"

// embeddingDimensions 必须与建表时的 vector(1536) 一致，请求时显式要求供应商按此维度返回。
const embeddingDimensions = 1536

// embeddingMaxRunes 是单条消息送去计算向量的最长文本：超长消息（多为工具抓取的网页全文）只向量化开头，
// 关键词路仍覆盖全文，不会因此搜不到。
const embeddingMaxRunes = 16000

// embeddingHTTPClient 是 embedding 专用客户端：单条请求不该等满模型调用的十分钟上限。
var embeddingHTTPClient = &http.Client{Timeout: 120 * time.Second}

// embedText 把一段文本向量化：凭据从看板保存的 OpenRouter 配置读取，未配置时返回可识别的错误供调用方降级。
func (r *Runner) embedText(ctx context.Context, text string) ([]float32, error) {
	credentials, err := r.services.Load(ctx, services.OpenRouter)
	if err != nil {
		return nil, err
	}
	runes := []rune(text)
	if len(runes) > embeddingMaxRunes {
		text = string(runes[:embeddingMaxRunes])
	}
	payload, err := json.Marshal(map[string]any{"model": embeddingModel, "input": text, "dimensions": embeddingDimensions})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+credentials.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := embeddingHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding request failed with status %d", response.StatusCode)
	}
	var decoded struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.Data) == 0 || len(decoded.Data[0].Embedding) != embeddingDimensions {
		return nil, fmt.Errorf("embedding response is invalid")
	}
	return decoded.Data[0].Embedding, nil
}

// StartEmbeddingWorker 是向量补算的常驻循环：先抢一把全进程共用的咨询锁，保证多实例部署时只有一个工人在补算，
// 其余实例定期重试接手；工人按批取尚无向量的消息逐条补算，单条失败跳过本轮，不挡住后面的消息。
func (r *Runner) StartEmbeddingWorker(ctx context.Context) {
	for ctx.Err() == nil {
		if !r.runEmbeddingLeader(ctx) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}
}

// runEmbeddingLeader 抢到补算锁后持续工作直到上下文结束或数据库出错；返回 false 表示没抢到锁，调用方稍后再试。
func (r *Runner) runEmbeddingLeader(ctx context.Context) bool {
	connection, err := r.databasePool.Acquire(ctx)
	if err != nil {
		return false
	}
	defer connection.Release()
	queries := database.New(connection)
	locked, err := queries.TryLockRuntime(ctx, "embedding_worker")
	if err != nil || !locked {
		return false
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = queries.UnlockRuntime(unlockContext, "embedding_worker")
	}()
	skipped := map[uuid.UUID]bool{}
	for ctx.Err() == nil {
		rows, err := database.New(r.databasePool).ListMessagesNeedingEmbedding(ctx, 32)
		if err != nil {
			slog.Error("list messages needing embedding failed", "error", err)
			return true
		}
		pending := make([]database.ListMessagesNeedingEmbeddingRow, 0, len(rows))
		for _, row := range rows {
			if !skipped[row.ID] {
				pending = append(pending, row)
			}
		}
		if len(pending) == 0 {
			select {
			case <-ctx.Done():
				return true
			case <-time.After(10 * time.Second):
			}
			continue
		}
		for _, row := range pending {
			if ctx.Err() != nil {
				return true
			}
			vector, err := r.embedText(ctx, row.SearchText)
			if err != nil {
				skipped[row.ID] = true
				slog.Error("embed message failed", "message_id", row.ID, "error", err)
				continue
			}
			if err := database.New(r.databasePool).UpdateMessageEmbedding(ctx, database.UpdateMessageEmbeddingParams{MessageID: row.ID, Embedding: newVector(vector)}); err != nil {
				slog.Error("save message embedding failed", "message_id", row.ID, "error", err)
				return true
			}
		}
	}
	return true
}
