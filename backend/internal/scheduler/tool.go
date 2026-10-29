// 本文件实现 Agent 的 schedule 工具：供模型创建、查看、修改与删除定时安排，并把数据库记录转换成工具返回值。

package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ExecuteTool runs inside the agent reply transaction, after its user message lock.
func ExecuteTool(ctx context.Context, queries *database.Queries, userID uuid.UUID, raw string) (string, error) {
	var arguments struct {
		Action   string  `json:"action"`
		ID       string  `json:"id"`
		Before   string  `json:"before"`
		Title    *string `json:"title"`
		Context  *string `json:"context"`
		At       *string `json:"at"`
		Cron     *string `json:"cron"`
		Timezone *string `json:"timezone"`
		Enabled  *bool   `json:"enabled"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "Error: invalid schedule arguments: " + err.Error(), nil
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "Error: expected one JSON object", nil
	}
	if arguments.Action == "list" {
		var before *uuid.UUID
		if arguments.Before != "" {
			id, err := uuid.Parse(arguments.Before)
			if err != nil {
				return "Error: before must be a schedule UUID from the previous page", nil
			}
			before = &id
		}
		items, err := queries.ListSchedules(ctx, database.ListSchedulesParams{UserID: userID, BeforeID: before})
		if err != nil {
			return "", err
		}
		var nextBefore *uuid.UUID
		if len(items) > 50 {
			items = items[:50]
			nextBefore = &items[49].ID
		}
		views := make([]map[string]any, 0, len(items))
		for _, item := range items {
			views = append(views, scheduleView(item))
		}
		result, err := json.Marshal(map[string]any{"schedules": views, "next_before": nextBefore, "current_time": time.Now().UTC()})
		return string(result), err
	}
	if arguments.Action != "create" && arguments.Action != "update" && arguments.Action != "delete" {
		return "Error: action must be create, list, update or delete", nil
	}
	item := database.Schedule{UserID: userID, Status: "active"}
	if arguments.Action != "create" {
		id, err := uuid.Parse(arguments.ID)
		if err != nil {
			return "Error: id must be an existing schedule UUID", nil
		}
		item, err = queries.LockSchedule(ctx, database.LockScheduleParams{UserID: userID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return "Error: schedule not found for this user", nil
		}
		if err != nil {
			return "", err
		}
		if arguments.Action == "delete" {
			if _, err := queries.DeleteSchedule(ctx, database.DeleteScheduleParams{UserID: userID, ID: id}); err != nil {
				return "", err
			}
			result, err := json.Marshal(map[string]any{"id": id, "deleted": true, "already_delivered_context": "continues in Main"})
			return string(result), err
		}
		if arguments.Title == nil && arguments.Context == nil && arguments.At == nil && arguments.Cron == nil && arguments.Timezone == nil && arguments.Enabled == nil {
			return "Error: update requires at least one changed field", nil
		}
	}
	if arguments.Title != nil {
		item.Title = strings.TrimSpace(*arguments.Title)
	}
	if arguments.Context != nil {
		item.Context = strings.TrimSpace(*arguments.Context)
	}
	if length := utf8.RuneCountInString(item.Title); length < 1 || length > 160 {
		return "Error: title must contain 1 to 160 characters", nil
	}
	if length := utf8.RuneCountInString(item.Context); length < 1 || length > 24000 {
		return "Error: context must contain 1 to 24000 characters, including the task and necessary background", nil
	}
	if strings.ContainsRune(item.Title, 0) || strings.ContainsRune(item.Context, 0) {
		return "Error: title and context cannot contain a null character", nil
	}
	if arguments.Timezone != nil {
		item.Timezone = strings.TrimSpace(*arguments.Timezone)
	}
	if _, err := time.LoadLocation(item.Timezone); err != nil || item.Timezone == "" || item.Timezone == "Local" {
		return "Error: timezone must be an explicit IANA name such as Asia/Shanghai or UTC", nil
	}
	if arguments.At != nil && arguments.Cron != nil {
		return "Error: supply either at or cron, not both", nil
	}
	if arguments.At != nil {
		at, err := time.Parse(time.RFC3339Nano, *arguments.At)
		if err != nil {
			return "Error: at must be an RFC3339 timestamp with an explicit offset", nil
		}
		at = at.UTC()
		item.RunAt, item.Cron = &at, nil
	}
	if arguments.Cron != nil {
		expression := strings.Join(strings.Fields(*arguments.Cron), " ")
		item.Cron, item.RunAt = &expression, nil
	}
	if item.RunAt == nil && item.Cron == nil {
		return "Error: create requires either at or cron", nil
	}
	now := time.Now().UTC()
	reschedule := arguments.Action == "create" || arguments.At != nil || arguments.Cron != nil || arguments.Timezone != nil || (arguments.Enabled != nil && *arguments.Enabled)
	if reschedule {
		if item.RunAt != nil {
			if !item.RunAt.After(now) {
				return "Error: at must be in the future; current UTC time is " + now.Format(time.RFC3339Nano), nil
			}
			item.NextRunAt = item.RunAt
		} else {
			if len(strings.Fields(*item.Cron)) != 5 {
				return "Error: cron must have five fields: minute hour day-of-month month day-of-week", nil
			}
			next, err := nextCronTime(*item.Cron, item.Timezone, now)
			if err != nil {
				return "Error: " + err.Error(), nil
			}
			item.NextRunAt = &next
		}
		if item.Status == "triggered" {
			item.Status = "active"
		}
	}
	if arguments.Enabled != nil {
		item.Status = "paused"
		if *arguments.Enabled {
			item.Status = "active"
		}
	}
	if item.Status != "active" {
		item.NextRunAt = nil
	}
	var err error
	if arguments.Action == "create" {
		item, err = queries.CreateSchedule(ctx, database.CreateScheduleParams{
			UserID: userID, Title: item.Title, Context: item.Context, RunAt: item.RunAt,
			Cron: item.Cron, Timezone: item.Timezone, Status: item.Status, NextRunAt: item.NextRunAt,
		})
	} else {
		item, err = queries.UpdateSchedule(ctx, database.UpdateScheduleParams{
			UserID: userID, ID: item.ID, Title: item.Title, Context: item.Context, RunAt: item.RunAt,
			Cron: item.Cron, Timezone: item.Timezone, Status: item.Status, NextRunAt: item.NextRunAt,
		})
	}
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(scheduleView(item))
	return string(result), err
}

func scheduleView(item database.Schedule) map[string]any {
	return map[string]any{
		"id": item.ID, "title": item.Title, "context": item.Context,
		"at": item.RunAt, "cron": item.Cron, "timezone": item.Timezone,
		"status": item.Status, "next_run_at": item.NextRunAt, "version": item.Version,
	}
}
