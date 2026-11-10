// 本命令把已创建且按用户隔离的 AgenticFS 入口绑定到用户；不创建计算实例，也不替换已有绑定。

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/sandbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// main 只向终端报告操作结果，不接收或打印服务凭证。
func main() {
	if err := run(); err != nil {
		slog.Error("bind workspace failed", "error", err)
		os.Exit(1)
	}
}

// run 在写库前校验 AP 地域与角色格式；重复执行相同绑定不会修改当前实例。
func run() error {
	user := flag.String("user-id", "", "目标用户 UUID")
	region := flag.String("region", "ap-southeast-1", "AgenticFS 与 FC 地域")
	address := flag.String("server-addr", "", "用户独立的 AgenticFS AP 地址，以 :/ 结尾")
	role := flag.String("role-arn", "", "仅允许访问上述 AP 的 RAM 角色 ARN")
	flag.Parse()
	id, err := uuid.Parse(*user)
	if err != nil {
		return fmt.Errorf("user-id must be a UUID")
	}
	if err := sandbox.ValidateBinding(*region, *address, *role); err != nil {
		return err
	}
	if os.Getenv("DATABASE_URL") == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	queries := database.New(pool)
	_, err = queries.BindSandboxWorkspace(ctx, database.BindSandboxWorkspaceParams{UserID: id, ServerAddr: *address, RoleArn: *role})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, readErr := queries.GetSandboxWorkspace(ctx, id)
		if readErr != nil {
			return readErr
		}
		if existing.ServerAddr != *address || existing.RoleArn != *role {
			return fmt.Errorf("user already has a different workspace; binding was not changed")
		}
		fmt.Println("Workspace already bound:", id)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Println("Workspace bound:", id)
	return nil
}
