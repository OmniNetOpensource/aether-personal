// 本文件识别可恢复的数据库故障，供启动恢复与单用户重试共用；模型和业务失败继续遵守原有失败策略。

package agent

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// temporaryDatabaseError 只重试驱动明确报告的超时、连接失效和暂时性数据库错误，避免重新放大模型重试次数。
func temporaryDatabaseError(err error) bool {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "40001", "40P01", "55P03", "53300", "57P01", "57P02", "57P03":
			return true
		default:
			return strings.HasPrefix(postgresError.Code, "08")
		}
	}
	var connectionError *pgconn.ConnectError
	return errors.As(err, &connectionError) || pgconn.SafeToRetry(err) || pgconn.Timeout(err) || errors.Is(err, pgconn.ErrConnClosed)
}
