// 本文件负责聊天端任务进展快照的读取与转换，随 WebSocket 登录与实时推送事件一并下发。

package httpapi

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// chatTask 是前端只读展示的单个任务快照，包含状态与最近更新时间。
type chatTask struct {
	ID          uuid.UUID `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// listChatTasks 读取当前用户的未关闭任务与近期已结束任务，空列表固定编码为 JSON 数组。
func (s *server) listChatTasks(ctx context.Context, userID uuid.UUID) ([]chatTask, error) {
	rows, err := s.queries.ListChatTasks(ctx, userID)
	if err != nil {
		return nil, err
	}
	tasks := make([]chatTask, 0, len(rows))
	for _, row := range rows {
		tasks = append(tasks, chatTask{
			ID:          row.ID,
			Title:       row.Title,
			Description: row.Description,
			Status:      row.Status,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
		})
	}
	return tasks, nil
}
