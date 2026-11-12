// 本文件实现管理看板的基础只读接口：解析共用的时间与筛选条件，返回用户列表、模型候选、Main 与 Subagent 状态以及单次模型调用的请求与响应正文。

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// adminFilters 是快照与运行时间线共用的查询条件。
type adminFilters struct {
	From, To       time.Time
	UserID         *uuid.UUID
	SubagentID     *uuid.UUID
	Model, Outcome *string
}

// adminUser 是快照接口返回给看板下拉框的用户摘要。
type adminUser struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// adminAgentStatus 是选中用户 Main 的当前运行状态。
type adminAgentStatus struct {
	UserID uuid.UUID `json:"user_id"`
	Email  string    `json:"email"`
	Status string    `json:"status"`
}

// adminSubagent 是选中用户的单个后台 Subagent 状态摘要，供看板运行过程页切换查看。
type adminSubagent struct {
	ID         uuid.UUID  `json:"id"`
	Title      string     `json:"title"`
	Status     string     `json:"status"`
	Error      *string    `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// adminMe 返回当前共享管理员会话的显示名称。
func (s *server) adminMe(w http.ResponseWriter, r *http.Request) {
	writeAdminJSON(w, map[string]string{"name": "管理员"})
}

// parseAdminFilters 校验看板提交的时间窗口、用户、Subagent、模型与请求结果筛选参数。
func parseAdminFilters(r *http.Request) (adminFilters, error) {
	values := r.URL.Query()
	filter := adminFilters{To: time.Now().UTC()}
	var err error
	if value := values.Get("to"); value != "" {
		filter.To, err = time.Parse(time.RFC3339, value)
		if err != nil || filter.To.After(time.Now().Add(time.Minute)) {
			return filter, fmt.Errorf("to must be a valid RFC3339 time no later than now")
		}
	}
	filter.From = filter.To.Add(-24 * time.Hour)
	if value := values.Get("from"); value != "" {
		filter.From, err = time.Parse(time.RFC3339, value)
		if err != nil {
			return filter, fmt.Errorf("from must be a valid RFC3339 time")
		}
	}
	filter.From, filter.To = filter.From.UTC(), filter.To.UTC()
	if filter.From.Year() < 1 || filter.To.Year() < 1 || !filter.From.Before(filter.To) || filter.To.Sub(filter.From) > 31*24*time.Hour {
		return filter, fmt.Errorf("range must be positive and no longer than 31 days")
	}
	if value := values.Get("user_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			return filter, fmt.Errorf("invalid user_id")
		}
		filter.UserID = &id
	}
	if value := values.Get("subagent_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			return filter, fmt.Errorf("invalid subagent_id")
		}
		filter.SubagentID = &id
	}
	if value := values.Get("model"); value != "" {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || utf8.RuneCountInString(value) > 160 {
			return filter, fmt.Errorf("model is too long")
		}
		filter.Model = &value
	}
	if value := values.Get("outcome"); value != "" {
		switch value {
		case "in_flight", "succeeded", "http_error", "transport_error", "invalid_response", "interrupted":
			filter.Outcome = &value
		default:
			return filter, fmt.Errorf("invalid outcome")
		}
	}
	return filter, nil
}

// beginAdminRead 开启带语句超时的只读事务，确保同一次看板快照里的查询观察到一致的时间点。
func (s *server) beginAdminRead(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.databasePool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '4s'"); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// writeAdminJSON 统一以禁止缓存的 JSON 响应写出数据。
func writeAdminJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(value)
}

// adminSnapshot 返回看板筛选栏所需的用户、模型候选，以及当前选中用户的 Main 与近期 Subagent 状态。
func (s *server) adminSnapshot(w http.ResponseWriter, r *http.Request) {
	filter, err := parseAdminFilters(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	tx, err := s.beginAdminRead(ctx)
	if err != nil {
		internalError(w, "begin admin snapshot failed", err)
		return
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	payload, err := queries.AdminSnapshot(ctx, database.AdminSnapshotParams{
		FromAt: filter.From, ToAt: filter.To, UserID: filter.UserID,
	})
	if err != nil {
		internalError(w, "read admin snapshot failed", err)
		return
	}
	userRows, err := queries.ListAdminUsers(ctx, filter.UserID)
	if err != nil {
		internalError(w, "read admin users failed", err)
		return
	}
	users := make([]adminUser, 0, len(userRows))
	for _, row := range userRows {
		users = append(users, adminUser{ID: row.ID, Email: row.Email, CreatedAt: row.CreatedAt})
	}
	agents := []adminAgentStatus{}
	subagents := []adminSubagent{}
	if filter.UserID != nil {
		agent, err := queries.GetAdminAgentStatus(ctx, *filter.UserID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			internalError(w, "read admin agent status failed", err)
			return
		}
		if err == nil {
			agents = append(agents, adminAgentStatus{UserID: agent.UserID, Email: agent.Email, Status: agent.Status})
		}
		subagentRows, err := queries.ListAdminSubagents(ctx, *filter.UserID)
		if err != nil {
			internalError(w, "read admin subagents failed", err)
			return
		}
		for _, row := range subagentRows {
			subagents = append(subagents, adminSubagent{
				ID:         row.ID,
				Title:      row.Title,
				Status:     row.Status,
				StartedAt:  row.StartedAt,
				FinishedAt: row.FinishedAt,
			})
		}
	}
	if err := tx.Commit(ctx); err != nil {
		internalError(w, "commit admin snapshot failed", err)
		return
	}
	var snapshot map[string]any
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		internalError(w, "decode admin snapshot failed", err)
		return
	}
	snapshot["users"] = map[string]any{"items": users}
	snapshot["agents"] = map[string]any{"items": agents}
	snapshot["subagents"] = map[string]any{"items": subagents}
	writeAdminJSON(w, snapshot)
}

// adminCallDetail 按调用 ID 返回单次模型请求的实际请求与响应 JSON。
func (s *server) adminCallDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid call ID", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	detail, err := s.queries.GetAdminCallDetail(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "call not found", http.StatusNotFound)
		return
	}
	if err != nil {
		internalError(w, "read model call payloads failed", err)
		return
	}
	var response json.RawMessage
	var responseText *string
	if json.Valid(detail.ResponseBody) {
		response = json.RawMessage(detail.ResponseBody)
	} else if len(detail.ResponseBody) > 0 {
		text := string(detail.ResponseBody)
		responseText = &text
	}
	writeAdminJSON(w, map[string]any{
		"id": id, "request": json.RawMessage(detail.RequestBody), "response": response,
		"response_text": responseText, "response_truncated": detail.ResponseTruncated,
	})
}
