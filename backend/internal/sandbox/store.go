// 本文件按用户恢复或创建 FC 实例，并把实例挂到已有长期工作区；只有实例确实消失时才重建。

package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	e2b "github.com/aliyun-fc/e2b-go-sdk"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 共享无状态依赖；同一用户的实例操作由现有单用户 Runner 与工具顺序保证串行。
type Store struct {
	queries  *database.Queries
	services *services.Store
	images   *images.Store
	config   Config
}

// New 仅组装依赖，不因未开通沙箱而阻止管理看板启动。
func New(pool *pgxpool.Pool, serviceStore *services.Store, imageStore *images.Store, config Config) *Store {
	return &Store{queries: database.New(pool), services: serviceStore, images: imageStore, config: config}
}

// ensure 先恢复现有实例，再启动挂载后的用户进程；启动失败也保留映射，下一次调用继续同一个实例。
func (s *Store) ensure(ctx context.Context, userID uuid.UUID) (*e2b.Sandbox, string, error) {
	if err := s.config.Validate(); err != nil {
		return nil, "", err
	}
	workspace, err := s.queries.GetSandboxWorkspace(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", errors.New("sandbox workspace is not configured; operator must run init-workspace")
	}
	if err != nil {
		return nil, "", err
	}
	if err := ValidateBinding(s.config.Region, workspace.ServerAddr, workspace.RoleArn); err != nil {
		return nil, "", err
	}
	client, err := s.client(ctx)
	if err != nil {
		return nil, "", err
	}
	// 工具可能持续四分钟，执行期间不能按空闲策略暂停；回执处理结束后再设置空闲时长。
	seconds := int(max(s.config.Timeout, 5*time.Minute).Seconds())
	var instance *e2b.Sandbox
	var token string
	if workspace.SandboxID != nil {
		instance, err = client.ConnectSandbox(ctx, *workspace.SandboxID, seconds)
		// ConnectSandbox 把控制面 404 返回为 NotFoundError；这里只将实例不存在视为重建条件。
		var missing *e2b.NotFoundError
		if err != nil && !errors.As(err, &missing) {
			return nil, "", err
		}
		if err == nil {
			token, err = s.services.DecryptSecret("sandbox:"+userID.String(), workspace.TokenCiphertext)
			if err != nil {
				return nil, "", err
			}
		}
	}
	// 计算实例可以丢弃，AP 与角色绑定保持不变；新实例仍使用 UID 1000 读写同一个挂载。
	created := instance == nil
	if created {
		token = rand.Text()
		storage, err := json.Marshal(map[string]any{"userID": 1000, "groupID": 1000, "mountPoints": []map[string]string{{"serverAddr": workspace.ServerAddr, "mountDir": "/mnt/workspace"}}})
		if err != nil {
			return nil, "", err
		}
		network, err := json.Marshal(map[string]any{"vpcId": s.config.VPCID, "securityGroupId": s.config.SecurityGroupID, "vSwitchIds": s.config.VSwitchIDs})
		if err != nil {
			return nil, "", err
		}
		public := false
		instance, err = client.CreateSandbox(ctx, e2b.WithTemplate(s.config.Template), e2b.WithTimeout(seconds), e2b.WithSecure(true),
			e2b.WithLifecycle(e2b.SandboxLifecycle{OnTimeout: "pause", AutoResume: false}),
			e2b.WithNetwork(e2b.SandboxNetworkOpts{AllowPublicTraffic: &public}),
			e2b.WithMetadata(map[string]string{"aether.user_id": userID.String(), "aether.desktop_stream": "vnc1", "fc.sandbox.storage.agenticfs": string(storage), "fc.sandbox.network.vpc": string(network), "fc.sandbox.auth.role": workspace.RoleArn}))
		if err != nil {
			return nil, "", err
		}
	}
	// 令牌只由正常的创建／连接路径更新；监控端读取密文，不调用会恢复实例的 ConnectSandbox。
	id := instance.SandboxID()
	if err := s.queries.SaveSandboxInstance(ctx, database.SaveSandboxInstanceParams{
		UserID: userID, SandboxID: &id, TokenCiphertext: s.services.EncryptSecret("sandbox:"+userID.String(), token),
		TrafficTokenCiphertext: s.services.EncryptSecret("sandbox-traffic:"+userID.String(), instance.TrafficAccessToken()),
	}); err != nil {
		if !created {
			return nil, "", err
		}
		// 尚未登记的实例不能被下一次调用找到，保存失败时立即释放，清理错误一并向上传递。
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, killErr := client.KillSandbox(cleanup, id)
		return nil, "", errors.Join(err, killErr)
	}
	result, err := instance.Commands.Run(ctx, "/opt/aether/start.sh", e2b.WithCommandUser("user"),
		e2b.WithCommandCwd("/mnt/workspace"), e2b.WithCommandEnv("AIO_API_KEY", token),
		e2b.WithCommandTimeout(75*time.Second), e2b.WithCommandRequestTimeout(80*time.Second))
	if err != nil {
		return nil, "", fmt.Errorf("start workspace services: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, "", fmt.Errorf("workspace services exited with code %d: %s", result.ExitCode, result.Stderr)
	}
	if err := s.syncUploadedFiles(ctx, userID, instance, workspace.SyncedThroughSequence); err != nil {
		return nil, "", fmt.Errorf("sync workspace uploads: %w", err)
	}
	return instance, token, nil
}

// client 为工具执行和只读监控创建同地域客户端；凭证只从加密服务设置读取。
func (s *Store) client(ctx context.Context) (*e2b.Client, error) {
	settings, err := s.services.Load(ctx, services.AliyunFC)
	if err != nil {
		return nil, err
	}
	// 旧数据面调试地址不能悄悄改变生产访问目标。
	if os.Getenv("E2B_SANDBOX_URL") != "" {
		return nil, errors.New("remove E2B_SANDBOX_URL; sandbox endpoints are assigned by FC")
	}
	domain := s.config.Region + ".e2b.fc.aliyuncs.com"
	return e2b.NewClient(e2b.WithAPIKey(settings.APIKey), e2b.WithValidateAPIKey(true), e2b.WithDomain(domain),
		e2b.WithAPIURL("https://api."+domain), e2b.WithDebug(false), e2b.WithAccessToken(""),
		e2b.WithRequestTimeout(90*time.Second), e2b.WithHTTPClient(&http.Client{Timeout: 90 * time.Second}))
}
