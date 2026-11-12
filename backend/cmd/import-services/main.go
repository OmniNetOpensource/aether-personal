// 本命令显式导入服务凭证：旧环境变量只填空缺，OSS CSV 与 FC Key 文件按参数加密保存，均不输出明文。

package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

// main 只做显式迁移，日常启动和请求不执行导入，因此旧环境变量不会恢复被清除的 Key。
func main() {
	ossCSV := flag.String("oss-csv", "", "阿里云 RAM 导出的凭证 CSV，仅导入 OSS 配置")
	ossBucket := flag.String("oss-bucket", "", "聊天图片的私有 OSS Bucket")
	ossRegion := flag.String("oss-region", "", "OSS 地域，例如 cn-hangzhou")
	fcKeyFile := flag.String("fc-key-file", "", "仅导入 FC API Key 的文件路径；可用 /dev/stdin，Key 不进入命令行")
	flag.Parse()
	if *fcKeyFile != "" && *ossCSV != "" {
		slog.Error("choose either fc-key-file or oss-csv")
		os.Exit(1)
	}
	settings, err := config.Load()
	if err != nil {
		slog.Error("load config failed", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, settings.DatabaseURL)
	if err != nil {
		slog.Error("connect database failed")
		os.Exit(1)
	}
	defer pool.Close()
	store, err := services.New(pool, settings.ServiceEncryptionKey, settings.AppEnv, settings.OpenRouterAPIKey)
	if err != nil {
		slog.Error("initialize service encryption failed")
		os.Exit(1)
	}
	// FC 凭证由维护者显式提供，不读取任何旧环境变量，也不回显完整 Key。
	if *fcKeyFile != "" {
		data, err := os.ReadFile(*fcKeyFile)
		if err != nil {
			slog.Error("cannot read FC credential file")
			os.Exit(1)
		}
		key := strings.TrimSpace(string(data))
		if key == "" {
			slog.Error("FC credential file is empty")
			os.Exit(1)
		}
		if err := store.Update(ctx, services.AliyunFC, &key, nil, nil); err != nil {
			slog.Error("FC credential import failed")
			os.Exit(1)
		}
		slog.Info("FC API Key encrypted and saved")
		return
	}
	if *ossCSV != "" {
		file, err := os.Open(*ossCSV)
		if err != nil {
			slog.Error("cannot open OSS credential file")
			os.Exit(1)
		}
		records, err := csv.NewReader(io.LimitReader(file, 32768)).ReadAll()
		file.Close()
		if err != nil || len(records) != 2 {
			slog.Error("OSS CSV must contain one header and one user")
			os.Exit(1)
		}
		credentials := services.OSSSettings{Bucket: *ossBucket, Region: *ossRegion}
		for index, header := range records[0] {
			header = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(header, "\ufeff")))
			header = strings.NewReplacer(" ", "", "_", "", "-", "").Replace(header)
			if header == "accesskeyid" {
				credentials.AccessKeyID = strings.TrimSpace(records[1][index])
			}
			if header == "accesskeysecret" {
				credentials.AccessKeySecret = strings.TrimSpace(records[1][index])
			}
		}
		raw, _ := json.Marshal(credentials)
		key := string(raw)
		if err := store.Update(ctx, services.AliyunOSS, &key, nil, nil); err != nil {
			slog.Error("OSS import failed; check credentials, bucket and region")
			os.Exit(1)
		}
		slog.Info("OSS configuration encrypted and saved")
		return
	}
	failed := false
	for _, definition := range services.Definitions {
		if definition.LegacyKey == "" {
			continue
		}
		value := services.Settings{APIKey: os.Getenv(definition.LegacyKey)}
		if definition.ID == services.IkunGemini {
			value.BaseURL = strings.TrimRight(strings.TrimSpace(os.Getenv("GEMINI_BASE_URL_IKUNCODE")), "/")
		}
		if definition.ID == services.Resend {
			value.EmailFrom = strings.TrimSpace(os.Getenv("EMAIL_FROM"))
		}
		imported, err := store.Import(ctx, definition.ID, value)
		if err != nil {
			slog.Error("service import failed; check its configuration", "service", definition.ID)
			failed = true
			continue
		}
		slog.Info("service import finished", "service", definition.ID, "imported", imported)
	}
	if failed {
		os.Exit(1)
	}
}
