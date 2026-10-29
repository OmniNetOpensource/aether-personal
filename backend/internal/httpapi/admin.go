// 本文件实现管理看板的只读查询接口：汇总运行概览、用户与 Agent、模型调用列表及详情，供看板分页筛选查看。

package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type adminCursor struct {
	At time.Time `json:"at"`
	ID uuid.UUID `json:"id"`
}

type adminFilters struct {
	From, To        time.Time
	Bucket          string
	UserID, AgentID *uuid.UUID
	AgentKind       string
	Model, Outcome  *string
	Search          string
	Limit           int32
	Cursor          *adminCursor
	UserCursor      *adminCursor
	AgentCursor     *adminCursor
	StepID          *uuid.UUID
}

type adminPage struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor *string           `json:"next_cursor"`
	lastCursor *adminCursor
}

func (s *server) adminMe(w http.ResponseWriter, r *http.Request) {
	writeAdminJSON(w, map[string]string{"name": "管理员"})
}

func parseAdminFilters(r *http.Request) (adminFilters, error) {
	values := r.URL.Query()
	filter := adminFilters{To: time.Now().UTC(), AgentKind: "all", Limit: 50}
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
	filter.Bucket = values.Get("bucket")
	if filter.Bucket == "" {
		filter.Bucket = "hour"
		if filter.To.Sub(filter.From) > 48*time.Hour {
			filter.Bucket = "day"
		}
	}
	if filter.Bucket != "hour" && filter.Bucket != "day" {
		return filter, fmt.Errorf("bucket must be hour or day")
	}
	if filter.Bucket == "hour" && filter.To.Sub(filter.From.Truncate(time.Hour)) > 744*time.Hour {
		return filter, fmt.Errorf("hourly range exceeds 744 buckets; use day")
	}
	if value := values.Get("user_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			return filter, fmt.Errorf("invalid user_id")
		}
		filter.UserID = &id
	}
	if value := values.Get("agent"); value != "" {
		if filter.UserID == nil {
			return filter, fmt.Errorf("agent requires user_id")
		}
		filter.AgentKind = "main"
		if value == "recap" {
			filter.AgentKind = "recap"
		} else if value != "main" {
			id, err := uuid.Parse(value)
			if err != nil {
				return filter, fmt.Errorf("agent must be main, recap or a Subagent UUID")
			}
			filter.AgentKind, filter.AgentID = "subagent", &id
		}
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
	filter.Search = strings.TrimSpace(values.Get("q"))
	if !utf8.ValidString(filter.Search) || strings.ContainsRune(filter.Search, 0) || utf8.RuneCountInString(filter.Search) > 128 {
		return filter, fmt.Errorf("q is too long")
	}
	if value := values.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			return filter, fmt.Errorf("limit must be between 1 and 100")
		}
		filter.Limit = int32(limit)
	}
	for _, cursor := range []struct {
		name string
		out  **adminCursor
	}{{"cursor", &filter.Cursor}, {"user_cursor", &filter.UserCursor}, {"agent_cursor", &filter.AgentCursor}} {
		value := values.Get(cursor.name)
		if value == "" {
			continue
		}
		if len(value) > 256 {
			return filter, fmt.Errorf("invalid %s", cursor.name)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		var parsed adminCursor
		if err != nil || json.Unmarshal(decoded, &parsed) != nil || parsed.At.IsZero() || parsed.At.Year() < 1 {
			return filter, fmt.Errorf("invalid %s", cursor.name)
		}
		*cursor.out = &parsed
	}
	if value := values.Get("step_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			return filter, fmt.Errorf("invalid step_id")
		}
		filter.StepID = &id
	}

	return filter, nil
}

func adminCursorValues(cursor *adminCursor) (*time.Time, *uuid.UUID) {
	if cursor == nil {
		return nil, nil
	}
	return &cursor.At, &cursor.ID
}

func appendAdminItem(page *adminPage, payload []byte, at time.Time, id uuid.UUID, limit int32) {
	if len(page.Items) < int(limit) {
		page.Items = append(page.Items, json.RawMessage(payload))
		page.lastCursor = &adminCursor{At: at, ID: id}
		return
	}
	// The extra row proves another page exists; resume after the last visible row.
	if page.NextCursor == nil {
		data, _ := json.Marshal(page.lastCursor)
		value := base64.RawURLEncoding.EncodeToString(data)
		page.NextCursor = &value
	}
}

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

func writeAdminJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(value)
}

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
		FromAt: filter.From, ToAt: filter.To, Bucket: filter.Bucket,
		UserID: filter.UserID, AgentKind: filter.AgentKind, AgentID: filter.AgentID,
		Model: filter.Model, Outcome: filter.Outcome, Search: filter.Search,
	})
	if err != nil {
		internalError(w, "read admin snapshot failed", err)
		return
	}
	cursorAt, cursorID := adminCursorValues(filter.UserCursor)
	userRows, err := queries.ListAdminUsers(ctx, database.ListAdminUsersParams{
		FromAt: filter.From, ToAt: filter.To, UserID: filter.UserID,
		AgentKind: filter.AgentKind, AgentID: filter.AgentID, Model: filter.Model,
		Outcome: filter.Outcome, Search: filter.Search,
		CursorAt: cursorAt, CursorID: cursorID, PageLimit: filter.Limit + 1,
	})
	if err != nil {
		internalError(w, "read admin users failed", err)
		return
	}
	users := adminPage{Items: []json.RawMessage{}}
	for _, row := range userRows {
		appendAdminItem(&users, row.Payload, row.CreatedAt, row.ID, filter.Limit)
	}
	agents := adminPage{Items: []json.RawMessage{}}
	if filter.UserID != nil {
		cursorAt, cursorID = adminCursorValues(filter.AgentCursor)
		rows, err := queries.ListAdminAgents(ctx, database.ListAdminAgentsParams{
			FromAt: filter.From, ToAt: filter.To, UserID: filter.UserID,
			SelectedUserID: *filter.UserID, AgentKind: filter.AgentKind, AgentID: filter.AgentID,
			Model: filter.Model, Outcome: filter.Outcome, Search: filter.Search,
			CursorAt: cursorAt, CursorID: cursorID, PageLimit: filter.Limit + 1,
		})
		if err != nil {
			internalError(w, "read admin agents failed", err)
			return
		}
		for _, row := range rows {
			id := uuid.Nil
			if row.ID != nil {
				id = *row.ID
			}
			appendAdminItem(&agents, row.Payload, row.CreatedAt, id, filter.Limit)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		internalError(w, "commit admin snapshot failed", err)
		return
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		internalError(w, "decode admin snapshot failed", err)
		return
	}
	snapshot["users"], _ = json.Marshal(users)
	snapshot["agents"], _ = json.Marshal(agents)
	writeAdminJSON(w, snapshot)
}

func (s *server) adminCalls(w http.ResponseWriter, r *http.Request) {
	filter, err := parseAdminFilters(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.beginAdminRead(ctx)
	if err != nil {
		internalError(w, "begin admin calls failed", err)
		return
	}
	defer tx.Rollback(ctx)
	cursorAt, cursorID := adminCursorValues(filter.Cursor)
	rows, err := s.queries.WithTx(tx).ListAdminCalls(ctx, database.ListAdminCallsParams{
		FromAt: filter.From, ToAt: filter.To, UserID: filter.UserID,
		AgentKind: filter.AgentKind, AgentID: filter.AgentID, Model: filter.Model,
		Outcome: filter.Outcome, Search: filter.Search, StepID: filter.StepID,
		CursorAt: cursorAt, CursorID: cursorID, PageLimit: filter.Limit + 1,
	})
	if err != nil {
		internalError(w, "read admin calls failed", err)
		return
	}
	page := adminPage{Items: []json.RawMessage{}}
	for _, row := range rows {
		appendAdminItem(&page, row.Payload, row.StartedAt, row.ID, filter.Limit)
	}
	if err := tx.Commit(ctx); err != nil {
		internalError(w, "commit admin calls failed", err)
		return
	}
	writeAdminJSON(w, page)
}

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
