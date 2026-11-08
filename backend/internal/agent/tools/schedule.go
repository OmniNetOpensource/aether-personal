// 本文件实现 schedule 工具：让 Main 创建、查看、修改与删除一次性或周期定时安排，并提供工具与调度器共用的 cron 下次时间计算。

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"
)

// scheduleTool 登记 schedule：安排的读写与回执同一事务提交，所以不设 OutsideTransaction。
var scheduleTool = Tool{
	Name:       "schedule",
	Definition: json.RawMessage(`{"type":"function","function":{"name":"schedule","description":"Arrange for saved context to be delivered back to you later, once or on a recurring basis, for example a reminder or a periodic check. When due, the context arrives as a schedule_event message and you act on it then. create needs title, context and exactly one of at (one-time) or cron with timezone (recurring). list returns all of this user's schedules with their next run time. update needs id plus only the fields to change; giving at turns a schedule into one-time, giving cron turns it into recurring. delete needs id. A one-time schedule is removed once delivered. Recurring runs missed during downtime are combined into one delivery.","parameters":{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["create","list","update","delete"]},"id":{"type":"string","description":"Schedule ID for update or delete, from create or list."},"title":{"type":"string","maxLength":160},"context":{"type":"string","maxLength":24000,"description":"Self-contained instructions for your future self: what to do, why, the background needed and how to deliver the result. When it fires you only have this context and the conversation."},"at":{"type":"string","description":"One-time run time as an RFC3339 timestamp with an explicit offset, in the future. Mutually exclusive with cron."},"cron":{"type":"string","description":"Recurring rule with five fields: minute hour day-of-month month day-of-week, e.g. 0 9 * * 1-5. Requires timezone. Mutually exclusive with at."},"timezone":{"type":"string","description":"IANA timezone the cron rule is evaluated in, such as Asia/Shanghai."}},"required":["action"]}}}`),
	Execute:    executeSchedule,
}

// scheduleInput 是工具参数；指针区分没给的字段与要改成的值，update 只改给出的字段。
type scheduleInput struct {
	Action   string  `json:"action"`
	ID       string  `json:"id"`
	Title    *string `json:"title"`
	Context  *string `json:"context"`
	At       *string `json:"at"`
	Cron     *string `json:"cron"`
	Timezone *string `json:"timezone"`
}

// scheduleView 是工具返回给模型的安排快照；一次性安排的 cron 与 timezone 为空。
type scheduleView struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Context   string    `json:"context"`
	Cron      *string   `json:"cron"`
	Timezone  *string   `json:"timezone"`
	NextRunAt time.Time `json:"next_run_at"`
}

// newScheduleView 把数据库行转成工具返回的快照。
func newScheduleView(item database.Schedule) scheduleView {
	return scheduleView{ID: item.ID, Title: item.Title, Context: item.Context, Cron: item.Cron, Timezone: item.Timezone, NextRunAt: item.NextRunAt}
}

// executeSchedule 按 action 读写当前用户的安排；参数错误以工具错误回执返回，让模型自己修正。
func executeSchedule(ctx context.Context, _ Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error) {
	var input scheduleInput
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return "Error: schedule arguments must be valid JSON", nil
	}
	now := time.Now().UTC()
	// list 附上当前时间，方便模型把 next_run_at 换算成离现在多久。
	if input.Action == "list" {
		rows, err := queries.ListSchedules(ctx, userID)
		if err != nil {
			return "", err
		}
		views := make([]scheduleView, 0, len(rows))
		for _, row := range rows {
			views = append(views, newScheduleView(row))
		}
		result, err := json.Marshal(map[string]any{"current_time": now, "schedules": views})
		return string(result), err
	}
	if input.Action != "create" && input.Action != "update" && input.Action != "delete" {
		return "Error: action must be create, list, update or delete", nil
	}
	// update 与 delete 先按 ID 找到这位用户自己的安排；update 锁住这一行，避免与调度器同时投递它。
	var item database.Schedule
	if input.Action != "create" {
		id, err := uuid.Parse(input.ID)
		if err != nil {
			return "Error: id must be a schedule ID from create or list", nil
		}
		if input.Action == "delete" {
			affected, err := queries.DeleteSchedule(ctx, database.DeleteScheduleParams{UserID: userID, ID: id})
			if err != nil {
				return "", err
			}
			if affected == 0 {
				return "Error: schedule not found for this user", nil
			}
			result, err := json.Marshal(map[string]any{"id": id, "deleted": true})
			return string(result), err
		}
		item, err = queries.LockSchedule(ctx, database.LockScheduleParams{UserID: userID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return "Error: schedule not found for this user; a one-time schedule is removed once delivered", nil
		}
		if err != nil {
			return "", err
		}
	}
	// 把给出的字段叠加到原安排上（新建时原安排为空），再整体检查标题与上下文。
	if input.Title != nil {
		item.Title = strings.TrimSpace(*input.Title)
	}
	if input.Context != nil {
		item.Context = strings.TrimSpace(*input.Context)
	}
	if length := utf8.RuneCountInString(item.Title); length < 1 || length > 160 {
		return "Error: title must contain 1 to 160 characters", nil
	}
	if length := utf8.RuneCountInString(item.Context); length < 1 || length > 24000 {
		return "Error: context must contain 1 to 24000 characters, including what to do and the background needed", nil
	}
	if strings.ContainsRune(item.Title, 0) || strings.ContainsRune(item.Context, 0) {
		return "Error: title and context cannot contain a null character", nil
	}
	if input.At != nil && input.Cron != nil {
		return "Error: give either at or cron, not both", nil
	}
	// 时间相关的字段一变就重新算下次触发时间；只改标题或上下文时保持原来的时间。
	switch {
	case input.At != nil:
		at, err := time.Parse(time.RFC3339, *input.At)
		if err != nil {
			return "Error: at must be an RFC3339 timestamp with an explicit offset", nil
		}
		if !at.After(now) {
			return "Error: at must be in the future; current UTC time is " + now.Format(time.RFC3339), nil
		}
		item.Cron, item.Timezone, item.NextRunAt = nil, nil, at.UTC()
	case input.Cron != nil || input.Timezone != nil:
		// 改周期规则或时区都要重算；没给的那一项沿用原安排。
		cronRule, timezone := item.Cron, item.Timezone
		if input.Cron != nil {
			expression := strings.Join(strings.Fields(*input.Cron), " ")
			cronRule = &expression
		}
		if input.Timezone != nil {
			name := strings.TrimSpace(*input.Timezone)
			timezone = &name
		}
		if cronRule == nil {
			return "Error: timezone only applies to a recurring schedule; give cron as well, or use at for a one-time schedule", nil
		}
		if timezone == nil {
			return "Error: cron requires timezone, an IANA name such as Asia/Shanghai", nil
		}
		next, err := NextCronTime(*cronRule, *timezone, now)
		if err != nil {
			return "Error: " + err.Error(), nil
		}
		item.Cron, item.Timezone, item.NextRunAt = cronRule, timezone, next
	case input.Action == "create":
		return "Error: create needs at (one-time) or cron with timezone (recurring)", nil
	}
	var err error
	if input.Action == "create" {
		item, err = queries.CreateSchedule(ctx, database.CreateScheduleParams{UserID: userID, Title: item.Title, Context: item.Context, Cron: item.Cron, Timezone: item.Timezone, NextRunAt: item.NextRunAt})
	} else {
		item, err = queries.UpdateSchedule(ctx, database.UpdateScheduleParams{UserID: userID, ID: item.ID, Title: item.Title, Context: item.Context, Cron: item.Cron, Timezone: item.Timezone, NextRunAt: item.NextRunAt})
	}
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(newScheduleView(item))
	return string(result), err
}

// NextCronTime 计算五段 cron 规则在指定时区里 after 之后的下一次触发时间（UTC）。
// 工具保存周期安排、调度器投递后推进周期安排都用它；从投递时刻往后算，停机期间错过的多次触发因此合并成一次。
func NextCronTime(expression, timezone string, after time.Time) (time.Time, error) {
	if len(strings.Fields(expression)) != 5 {
		return time.Time{}, errors.New("cron must have five fields: minute hour day-of-month month day-of-week")
	}
	if _, err := time.LoadLocation(timezone); err != nil || timezone == "" || timezone == "Local" {
		return time.Time{}, errors.New("timezone must be an IANA name such as Asia/Shanghai or UTC")
	}
	rule, err := cron.ParseStandard("CRON_TZ=" + timezone + " " + expression)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron: %w", err)
	}
	next := rule.Next(after)
	if next.IsZero() {
		return time.Time{}, errors.New("cron has no upcoming run")
	}
	return next.UTC(), nil
}
