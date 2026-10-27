package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/sandbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sandboxUnavailableError struct {
	cause error
}

func (e *sandboxUnavailableError) Error() string { return e.cause.Error() }
func (e *sandboxUnavailableError) Unwrap() error { return e.cause }

func sandboxUnavailable(ctx context.Context, connection *pgxpool.Conn, cause error, description string) error {
	if ctx.Err() != nil || connection.Conn().IsClosed() {
		return cause
	}
	if description != "" {
		cause = fmt.Errorf("%s: %w", description, cause)
	}
	return &sandboxUnavailableError{cause: cause}
}

func (r *Runner) ensureUserSandbox(ctx context.Context, connection *pgxpool.Conn, userID uuid.UUID) (string, error) {
	queries := database.New(connection)
	computer, err := queries.EnsureUserSandbox(ctx, userID)
	if err != nil {
		return "", err
	}
	var environment sandbox.Environment
	if computer.ProviderID == nil {
		if computer.ProvisionState == "pending" {
			if err := queries.MarkSandboxCreating(ctx, userID); err != nil {
				return "", err
			}
			environment, err = whileRequest(ctx, connection, func(requestContext context.Context) (sandbox.Environment, error) {
				return r.sandbox.Create(requestContext, userID, computer.AllocationID, r.settings.SandboxImage)
			})
			if err != nil {
				r.recordSandboxProvisionError(ctx, connection, userID, "unknown", err)
				return "", sandboxUnavailable(ctx, connection, err, "sandbox creation outcome is unknown; existing allocation will be reconciled")
			}
		} else {
			matches, findErr := whileRequest(ctx, connection, func(requestContext context.Context) ([]sandbox.Environment, error) {
				return r.sandbox.Find(requestContext, computer.AllocationID)
			})
			if findErr != nil {
				return "", sandboxUnavailable(ctx, connection, findErr, "reconcile sandbox allocation")
			}
			if len(matches) != 1 {
				err := fmt.Errorf("sandbox allocation has %d matches; refusing to create or replace the persistent computer", len(matches))
				r.recordSandboxProvisionError(ctx, connection, userID, "unknown", err)
				return "", sandboxUnavailable(ctx, connection, err, "")
			}
			environment = matches[0]
		}
		if err := queries.SaveUserSandbox(ctx, database.SaveUserSandboxParams{UserID: userID, ProviderID: environment.ID}); err != nil {
			return "", err
		}
	} else {
		environment, err = whileRequest(ctx, connection, func(requestContext context.Context) (sandbox.Environment, error) {
			return r.sandbox.Get(requestContext, *computer.ProviderID)
		})
		if err != nil {
			if errors.Is(err, sandbox.ErrNotFound) {
				r.recordSandboxProvisionError(ctx, connection, userID, "missing", err)
				return "", sandboxUnavailable(ctx, connection, err, "persistent sandbox is missing; its mapping was preserved and no replacement was created")
			}
			return "", sandboxUnavailable(ctx, connection, err, "get persistent sandbox")
		}
	}
	readyTimeout := 30 * time.Second
	if r.settings.SandboxProvider == "aliyun" {
		readyTimeout = 3 * time.Minute
	}
	stateContext, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	if environment.State == "pausing" {
		if err := r.waitForSandboxState(stateContext, connection, environment.ID, "paused"); err != nil {
			return "", sandboxUnavailable(ctx, connection, err, "")
		}
		environment.State = "paused"
	}
	if environment.State == "paused" {
		_, err := whileRequest(stateContext, connection, func(requestContext context.Context) (struct{}, error) {
			return struct{}{}, r.sandbox.Resume(requestContext, environment.ID)
		})
		if err != nil {
			return "", sandboxUnavailable(ctx, connection, err, "sandbox resume outcome must be reconciled before use")
		}
		environment.State = "resuming"
	}
	if environment.State != "running" {
		if environment.State != "pending" && environment.State != "resuming" {
			return "", sandboxUnavailable(ctx, connection, fmt.Errorf("persistent sandbox is not available: state %q", environment.State), "")
		}
		if err := r.waitForSandboxState(stateContext, connection, environment.ID, "running"); err != nil {
			return "", sandboxUnavailable(ctx, connection, err, "")
		}
	}
	// Running describes the container; execd can still be starting after creation or resume.
	for {
		_, err := whileRequest(stateContext, connection, func(requestContext context.Context) (struct{}, error) {
			return struct{}{}, r.sandbox.Ping(requestContext, environment.ID)
		})
		if err == nil {
			break
		}
		select {
		case <-stateContext.Done():
			return "", sandboxUnavailable(ctx, connection, err, "sandbox execution service did not become reachable")
		case <-time.After(250 * time.Millisecond):
		}
	}
	if err := queries.SaveUserSandbox(ctx, database.SaveUserSandboxParams{UserID: userID, ProviderID: environment.ID}); err != nil {
		return "", err
	}
	return environment.ID, nil
}

func (r *Runner) recordSandboxProvisionError(ctx context.Context, connection *pgxpool.Conn, userID uuid.UUID, state string, cause error) {
	if ctx.Err() != nil || connection.Conn().IsClosed() {
		return
	}
	writeContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := connection.Ping(writeContext); err != nil {
		return
	}
	message := cause.Error()
	if err := database.New(connection).SetSandboxProvisionError(writeContext, database.SetSandboxProvisionErrorParams{UserID: userID, State: state, Error: &message}); err != nil {
		slog.Error("save sandbox provisioning error failed", "user_id", userID, "error", err)
	}
}

func (r *Runner) waitForSandboxState(ctx context.Context, connection *pgxpool.Conn, sandboxID, target string) error {
	for {
		environment, err := whileRequest(ctx, connection, func(requestContext context.Context) (sandbox.Environment, error) {
			return r.sandbox.Get(requestContext, sandboxID)
		})
		if err != nil {
			return fmt.Errorf("confirm sandbox %s state: %w", target, err)
		}
		if environment.State == target {
			return nil
		}
		switch environment.State {
		case "pending", "pausing", "paused", "resuming", "running":
		default:
			return fmt.Errorf("sandbox entered state %q while waiting for %s", environment.State, target)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sandbox did not reach %s state: %w", target, ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (r *Runner) pauseIdleSandboxes(ctx context.Context) {
	if r.sandbox == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		idleBefore := time.Now().Add(-time.Duration(r.settings.SandboxIdleSeconds) * time.Second)
		computers, err := database.New(r.databasePool).ListIdleUserSandboxes(ctx, idleBefore)
		if err != nil && ctx.Err() == nil {
			slog.Error("scan idle sandboxes failed", "error", err)
		}
		for _, computer := range computers {
			if ctx.Err() != nil {
				return
			}
			func() {
				operationContext, cancel := context.WithTimeout(ctx, 40*time.Second)
				defer cancel()
				connection, err := r.databasePool.Acquire(operationContext)
				if err != nil {
					if ctx.Err() == nil {
						slog.Error("acquire idle sandbox connection failed", "error", err)
					}
					return
				}
				defer connection.Release()
				queries := database.New(connection)
				lockKey := "sandbox:" + computer.UserID.String()
				locked, err := queries.TryLockRuntime(operationContext, lockKey)
				if err != nil || !locked {
					return
				}
				defer func() {
					cleanup, cancelCleanup := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancelCleanup()
					unlocked, err := queries.UnlockRuntime(cleanup, lockKey)
					if err != nil || !unlocked {
						connection.Conn().Close(cleanup)
					}
				}()
				current, err := queries.GetUserSandbox(operationContext, computer.UserID)
				if err != nil {
					slog.Error("read idle sandbox failed", "user_id", computer.UserID, "error", err)
					return
				}
				busy, err := queries.HasSandboxAgentWork(operationContext, computer.UserID)
				if err != nil {
					slog.Error("check idle sandbox work failed", "user_id", computer.UserID, "error", err)
					return
				}
				if busy || current.ProviderID == nil || current.ProvisionState != "ready" || !current.LastActivityAt.Before(idleBefore) {
					return
				}
				environment, err := whileRequest(operationContext, connection, func(requestContext context.Context) (sandbox.Environment, error) {
					return r.sandbox.Get(requestContext, *current.ProviderID)
				})
				if err != nil {
					slog.Error("get idle sandbox state failed", "user_id", computer.UserID, "error", err)
					return
				}
				if environment.State != "running" {
					return
				}
				operations, err := queries.ListActiveSandboxOperations(operationContext, computer.UserID)
				if err != nil {
					slog.Error("read active sandbox commands failed", "user_id", computer.UserID, "error", err)
					return
				}
				commandFinished := false
				for _, operation := range operations {
					canReconcile := operation.State == "running" || (operation.State == "unknown" && operation.ToolName == "stop_command")
					if !canReconcile || operation.ProviderCommandID == nil {
						return
					}
					command, err := whileRequest(operationContext, connection, func(requestContext context.Context) (sandbox.Command, error) {
						return r.sandbox.Command(requestContext, environment.ID, *operation.ProviderCommandID)
					})
					if err != nil {
						slog.Error("reconcile idle sandbox command failed", "user_id", computer.UserID, "error", err)
						return
					}
					if command.Running {
						return
					}
					if err := pgx.BeginFunc(operationContext, connection, func(tx pgx.Tx) error {
						queries := database.New(tx)
						if err := queries.CompleteSandboxOperation(operationContext, database.CompleteSandboxOperationParams{ID: operation.ID, State: "completed", Result: operation.Result}); err != nil {
							return err
						}
						return queries.TouchUserSandbox(operationContext, computer.UserID)
					}); err != nil {
						slog.Error("complete idle sandbox command failed", "user_id", computer.UserID, "error", err)
						return
					}
					commandFinished = true
				}
				if commandFinished {
					return
				}
				busy, err = queries.HasSandboxAgentWork(operationContext, computer.UserID)
				if err != nil {
					slog.Error("recheck idle sandbox work failed", "user_id", computer.UserID, "error", err)
					return
				}
				if busy {
					return
				}
				_, err = whileRequest(operationContext, connection, func(requestContext context.Context) (struct{}, error) {
					return struct{}{}, r.sandbox.Pause(requestContext, environment.ID)
				})
				if err == nil {
					stateContext, cancelState := context.WithTimeout(operationContext, 30*time.Second)
					err = r.waitForSandboxState(stateContext, connection, environment.ID, "paused")
					cancelState()
				}
				if err != nil && ctx.Err() == nil {
					slog.Error("pause idle sandbox outcome needs reconciliation", "user_id", computer.UserID, "error", err)
				}
			}()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
