// 本文件保存部署共用的服务凭证：数据库只存 AES-GCM 密文，调用前读取当前值，管理接口只取得脱敏状态。

package services

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 支持的服务有固定边界，浏览器不能新增任意供应商或任意凭证名称。
const (
	OpenRouter = "openrouter"
	IkunGemini = "ikun_gemini"
	Resend     = "resend"
	Serper     = "serper"
	Parallel   = "parallel"
	EveryInfra = "everyinfra"
	// ikun Gemini 的默认线路由部署方确定，管理员日常只需填写 Key。
	DefaultGeminiBaseURL = "https://api.ikuncode.cc"
)

// Definition 提供看板名称与旧环境变量名称，导入命令与管理页使用同一份服务目录。
type Definition struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	LegacyKey string `json:"-"`
}

var Definitions = []Definition{
	{OpenRouter, "OpenRouter", "OPENROUTER_API_KEY"},
	{IkunGemini, "ikun Gemini", "GEMINI_API_KEY_IKUNCODE"},
	{Resend, "Resend", "RESEND_API_KEY"},
	{Serper, "Serper", "SERP_API_KEY"},
	{Parallel, "Parallel", "PARALLEL_API_KEY"},
	{EveryInfra, "EveryInfra", "EVERYINFRA_API_KEY"},
}

// 固定错误让调用者区分未配置、解密失败与输入错误，不把秘密带入日志或 HTTP 响应。
var (
	ErrNotConfigured = errors.New("service is not configured")
	ErrUnreadable    = errors.New("service credential cannot be decrypted; check SERVICE_ENCRYPTION_KEY")
	ErrInvalid       = errors.New("invalid service settings")
)

// Settings 仅供后端发出请求使用，不提供 JSON 序列化出口。
type Settings struct {
	APIKey    string `json:"-"`
	BaseURL   string `json:"-"`
	EmailFrom string `json:"-"`
}

// Info 是管理接口唯一返回的服务状态，不含完整 Key 或密文，也不声称供应商已验证通过。
type Info struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	KeySuffix string     `json:"key_suffix"`
	BaseURL   string     `json:"base_url"`
	EmailFrom string     `json:"email_from"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// Store 无凭证缓存，每个外部请求读取一份当前值，已经发出的请求不受后续替换影响。
type Store struct {
	queries *database.Queries
	cipher  cipher.AEAD
	appEnv  string
}

// New 只组装本地密码算法，不依赖启动时数据库已就绪。
func New(pool *pgxpool.Pool, key []byte, appEnv string) (*Store, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return &Store{queries: database.New(pool), cipher: aead, appEnv: appEnv}, nil
}

// Load 为实际请求解密一条服务配置；缺失或损坏时失败，不回退到旧环境变量。
func (s *Store) Load(ctx context.Context, service string) (Settings, error) {
	row, err := s.queries.GetServiceSettings(ctx, service)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, fmt.Errorf("%s: %w", service, ErrNotConfigured)
	}
	if err != nil {
		return Settings{}, err
	}
	if row.ApiKeyCiphertext == nil {
		return Settings{}, fmt.Errorf("%s: %w", service, ErrNotConfigured)
	}
	key, err := s.cipher.Open(nil, nil, row.ApiKeyCiphertext, []byte(service))
	if err != nil {
		return Settings{}, fmt.Errorf("%s: %w", service, ErrUnreadable)
	}
	return Settings{APIKey: string(key), BaseURL: serviceBaseURL(service, row.BaseUrl), EmailFrom: row.EmailFrom}, nil
}

// List 仅生成脱敏信息；单条凭证无法解密时仍允许管理员在看板替换或清除它。
func (s *Store) List(ctx context.Context) ([]Info, error) {
	rows, err := s.queries.ListServiceSettings(ctx)
	if err != nil {
		return nil, err
	}
	byService := map[string]database.ServiceSetting{}
	for _, row := range rows {
		byService[row.Service] = row
	}
	items := make([]Info, 0, len(Definitions))
	for _, definition := range Definitions {
		info := Info{ID: definition.ID, Name: definition.Name, Status: "not_configured", BaseURL: serviceBaseURL(definition.ID, "")}
		if row, exists := byService[definition.ID]; exists {
			info.BaseURL, info.EmailFrom, info.UpdatedAt = serviceBaseURL(definition.ID, row.BaseUrl), row.EmailFrom, &row.UpdatedAt
			if row.ApiKeyCiphertext != nil {
				info.Status, info.KeySuffix = "saved", row.KeySuffix
				if _, err := s.cipher.Open(nil, nil, row.ApiKeyCiphertext, []byte(row.Service)); err != nil {
					info.Status = "unreadable"
				}
			}
		}
		items = append(items, info)
	}
	return items, nil
}

// Update 处理单管理员的一次修改：nil Key 保留，空 Key 清除，非空 Key 替换。
// 清除仍保留数据库行，因此重新运行旧配置导入命令不会恢复被清除的 Key。
func (s *Store) Update(ctx context.Context, service string, key, baseURL, emailFrom *string) error {
	row, err := s.queries.GetServiceSettings(ctx, service)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	settings := database.SaveServiceSettingsParams{Service: service, ApiKeyCiphertext: row.ApiKeyCiphertext, KeySuffix: row.KeySuffix, BaseUrl: row.BaseUrl, EmailFrom: row.EmailFrom}
	if key != nil {
		settings.ApiKeyCiphertext, settings.KeySuffix = nil, ""
		if *key != "" {
			if len(*key) > 8192 || strings.ContainsFunc(*key, func(r rune) bool { return r <= ' ' || r > '~' }) {
				return fmt.Errorf("%w: API Key must contain visible ASCII characters", ErrInvalid)
			}
			settings.ApiKeyCiphertext = s.cipher.Seal(nil, nil, []byte(*key), []byte(service))
			if len(*key) > 4 {
				settings.KeySuffix = (*key)[len(*key)-4:]
			}
		}
	}
	if baseURL != nil {
		settings.BaseUrl = strings.TrimRight(strings.TrimSpace(*baseURL), "/")
	}
	if emailFrom != nil {
		settings.EmailFrom = strings.TrimSpace(*emailFrom)
	}
	settings.BaseUrl = serviceBaseURL(service, settings.BaseUrl)
	if err := validate(service, settings.ApiKeyCiphertext != nil, settings.BaseUrl, settings.EmailFrom, s.appEnv); err != nil {
		return err
	}
	return s.queries.SaveServiceSettings(ctx, settings)
}

// Import 仅填入从未配置过的服务，批量迁移由显式命令调用，正常启动不读取旧 Key。
func (s *Store) Import(ctx context.Context, service string, settings Settings) (bool, error) {
	settings.BaseURL = serviceBaseURL(service, settings.BaseURL)
	if err := validate(service, settings.APIKey != "", settings.BaseURL, settings.EmailFrom, s.appEnv); err != nil {
		return false, err
	}
	if settings.APIKey == "" {
		return false, nil
	}
	if len(settings.APIKey) > 8192 || strings.ContainsFunc(settings.APIKey, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return false, ErrInvalid
	}
	suffix := ""
	if len(settings.APIKey) > 4 {
		suffix = settings.APIKey[len(settings.APIKey)-4:]
	}
	count, err := s.queries.ImportServiceSettings(ctx, database.ImportServiceSettingsParams{
		Service: service, ApiKeyCiphertext: s.cipher.Seal(nil, nil, []byte(settings.APIKey), []byte(service)), KeySuffix: suffix, BaseUrl: settings.BaseURL, EmailFrom: settings.EmailFrom,
	})
	return count == 1, err
}

// serviceBaseURL 统一正常请求、看板和旧配置导入的默认地址，保留已显式配置的线路。
func serviceBaseURL(service, baseURL string) string {
	if service == IkunGemini && baseURL == "" {
		return DefaultGeminiBaseURL
	}
	return baseURL
}

// validate 是服务配置写入边界：只接受固定服务及其配套字段，密钥不出现在错误文本中。
func validate(service string, configured bool, baseURL, emailFrom, appEnv string) error {
	if len(baseURL) > 2048 || len(emailFrom) > 320 {
		return fmt.Errorf("%w: service fields are too long", ErrInvalid)
	}
	switch service {
	case IkunGemini:
		if emailFrom != "" || (configured && baseURL == "") {
			return fmt.Errorf("%w: Gemini requires a base URL", ErrInvalid)
		}
		if baseURL != "" {
			parsed, err := url.Parse(baseURL)
			if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && !(appEnv == "dev" && parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"))) {
				return fmt.Errorf("%w: use an HTTPS base URL without credentials, query or fragment", ErrInvalid)
			}
		}
	case Resend:
		if baseURL != "" || (configured && emailFrom == "") {
			return fmt.Errorf("%w: Resend requires a sender address", ErrInvalid)
		}
		if emailFrom != "" {
			if _, err := mail.ParseAddress(emailFrom); err != nil {
				return fmt.Errorf("%w: invalid sender address", ErrInvalid)
			}
		}
	case OpenRouter, Serper, Parallel, EveryInfra:
		if baseURL != "" || emailFrom != "" {
			return fmt.Errorf("%w: unsupported service fields", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown service", ErrInvalid)
	}
	return nil
}
