// 本文件管理 OSS 配置的加密载荷与脱敏读取：AccessKey Secret 和 ID 一起加密，Bucket 与地域供管理页核对。

package services

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

// OSSSettings 只供后端 OSS 客户端使用，完整凭证不进入管理接口响应。
type OSSSettings struct {
	AccessKeyID     string `json:"access_key_id"`
	AccessKeySecret string `json:"access_key_secret"`
	Bucket          string `json:"bucket"`
	Region          string `json:"region"`
}

// OSSInfo 是管理页可回显的非秘密信息，凭证仍只展示末尾四位。
type OSSInfo struct {
	Bucket string `json:"bucket"`
	Region string `json:"region"`
}

// parseOSSSettings 在配置写入边界校验完整载荷，不将请求内容带入错误文本。
func parseOSSSettings(raw string) (OSSSettings, error) {
	var settings OSSSettings
	if json.Unmarshal([]byte(raw), &settings) != nil || !regexp.MustCompile(`^[a-zA-Z0-9]{8,128}$`).MatchString(settings.AccessKeyID) || !regexp.MustCompile(`^[a-zA-Z0-9+/=_-]{8,256}$`).MatchString(settings.AccessKeySecret) || !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`).MatchString(settings.Bucket) || !regexp.MustCompile(`^[a-z]{2,10}-[a-z0-9-]{2,40}$`).MatchString(settings.Region) {
		return OSSSettings{}, fmt.Errorf("%w: invalid OSS credentials, bucket or region", ErrInvalid)
	}
	return settings, nil
}

// LoadOSS 解密当前 OSS 配置，不从环境变量读取服务密钥。
func (s *Store) LoadOSS(ctx context.Context) (OSSSettings, error) {
	settings, err := s.Load(ctx, AliyunOSS)
	if err != nil {
		return OSSSettings{}, err
	}
	return parseOSSSettings(settings.APIKey)
}
