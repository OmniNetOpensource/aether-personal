// 本文件负责发送登录验证码邮件：prod 通过 Resend 发信，dev 改为打印到日志方便本地登录。

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

func (s *server) sendLoginCode(ctx context.Context, email string, code string) error {
	if s.appConfig.AppEnv == "dev" {
		slog.Info("login code", "email", email, "code", code)
		return nil
	}

	body, err := json.Marshal(struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		HTML    string   `json:"html"`
		Text    string   `json:"text"`
	}{
		From:    s.appConfig.EmailFrom,
		To:      []string{email},
		Subject: "阿芒登录验证码 / Aether login code",
		HTML:    strings.ReplaceAll(loginEmailHTML, "{{code}}", code),
		Text:    fmt.Sprintf("你的登录验证码是 %s，10 分钟内有效。\nYour login code is %s. It expires in 10 minutes.", code, code),
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+s.appConfig.ResendAPIKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := s.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("resend returned %d: %s", response.StatusCode, responseBody)
	}
	return nil
}

const loginEmailHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark">
<meta name="supported-color-schemes" content="dark">
</head>
<body style="margin:0;padding:0;background-color:#0d0d0d;">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="background-color:#0d0d0d;">
<tr><td align="center" style="padding:48px 20px;">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="max-width:440px;">
<tr><td style="border-top:3px solid #5e6ad2;border-radius:12px;background-color:#141414;border-left:1px solid #262626;border-right:1px solid #262626;border-bottom:1px solid #262626;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,sans-serif;">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
<tr><td style="padding:36px 32px 28px;">
<p style="margin:0 0 20px;font-size:11px;font-weight:600;letter-spacing:0.12em;text-transform:uppercase;color:#8a8f98;">阿芒 / Aether</p>
<h1 style="margin:0 0 24px;font-size:20px;font-weight:600;line-height:1.3;color:#f7f8f8;letter-spacing:-0.02em;">登录验证 / Sign in</h1>
<p style="margin:0 0 16px;font-size:14px;line-height:1.5;color:#8a8f98;">请使用以下验证码登录。<br>Use the code below to sign in.</p>
<p style="margin:0;font-family:ui-monospace,SFMono-Regular,'SF Mono',Menlo,Consolas,monospace;font-size:36px;font-weight:600;letter-spacing:10px;line-height:1.2;color:#f7f8f8;">{{code}}</p>
</td></tr>
<tr><td style="padding:0 32px 32px;">
<p style="margin:0;font-size:13px;line-height:1.55;color:#6b7280;border-top:1px solid #262626;padding-top:24px;">验证码 10 分钟内有效。如非本人操作，请忽略此邮件。<br>This code expires in 10 minutes. If you didn't request it, you can ignore this email.</p>
</td></tr>
</table>
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`
