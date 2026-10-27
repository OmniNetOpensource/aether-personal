package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
	_ "time/tzdata"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/robfig/cron/v3"
)

func Run(ctx context.Context, pool *pgxpool.Pool, wake func(uuid.UUID)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		candidates, err := database.New(pool).ListDueSchedules(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("scan schedules failed", "error", err)
		}
		for _, candidate := range candidates {
			deliveryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			delivered, err := deliver(deliveryCtx, pool, candidate.UserID, candidate.ID)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Error("deliver schedule failed", "schedule_id", candidate.ID, "error", err)
			}
			if delivered {
				wake(candidate.UserID)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func deliver(ctx context.Context, pool *pgxpool.Pool, userID, scheduleID uuid.UUID) (bool, error) {
	delivered := false
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		// Database tools already hold this lock before modifying schedules.
		if err := queries.LockUserMessages(ctx, userID); err != nil {
			return err
		}
		item, err := queries.LockDueSchedule(ctx, database.LockDueScheduleParams{UserID: userID, ID: scheduleID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		now := item.DeliveryTime.UTC()
		status := "triggered"
		var next *time.Time
		if item.Cron != nil {
			value, err := nextCronTime(*item.Cron, item.Timezone, now)
			if err != nil {
				return err
			}
			next, status = &value, "active"
		}
		occurrenceID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		event, err := json.Marshal(map[string]any{
			"schedule_id": item.ID, "occurrence_id": occurrenceID, "title": item.Title,
			"scheduled_for": item.NextRunAt, "delivered_at": now, "timezone": item.Timezone,
			"context": item.Context,
		})
		if err != nil {
			return err
		}
		content, err := json.Marshal(map[string]string{
			"role": "user", "name": "schedule_event",
			"content": "Scheduled context for Main. This is a previously arranged task, not a new user message. " +
				"Continue the task using the saved context below. If delayed, consider its original time before acting. " +
				"Missed recurring triggers are combined into this single delivery.\n" + string(event),
		})
		if err != nil {
			return err
		}
		messageID, err := queries.DeliverScheduleMessage(ctx, database.DeliverScheduleMessageParams{UserID: userID, Content: content})
		if err != nil {
			return err
		}
		if err := queries.InsertScheduleOccurrence(ctx, database.InsertScheduleOccurrenceParams{
			ID: occurrenceID, ScheduleID: item.ID, ScheduleVersion: item.Version,
			ScheduledFor: *item.NextRunAt, DeliveredAt: now, Context: item.Context, TriggerMessageID: messageID,
		}); err != nil {
			return err
		}
		if err := queries.AdvanceSchedule(ctx, database.AdvanceScheduleParams{UserID: userID, ID: item.ID, Status: status, NextRunAt: next}); err != nil {
			return err
		}
		if err := queries.NotifyUserMessages(ctx, userID); err != nil {
			return err
		}
		delivered = true
		return nil
	})
	return delivered && err == nil, err
}

func nextCronTime(expression, timezone string, after time.Time) (time.Time, error) {
	rule, err := cron.ParseStandard("CRON_TZ=" + timezone + " " + expression)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron: %w", err)
	}
	next := rule.Next(after)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron has no upcoming occurrence")
	}
	return next.UTC(), nil
}
