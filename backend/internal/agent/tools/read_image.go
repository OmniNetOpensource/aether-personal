// 本文件实现 read_image 工具：下载一张直接图片地址，校验真实字节后写入该用户的 OSS，回执只保存对象地址。

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
	"github.com/google/uuid"
)

// readImageTool 登记 read_image：下载发生在事务外，回执单独提交。
var readImageTool = Tool{
	Name:               "read_image",
	Definition:         json.RawMessage(`{"type":"function","function":{"name":"read_image","description":"Read the visual content of one direct HTTP(S) JPEG, PNG, GIF, or WebP URL. Returns the image itself, not webpage text. Use fetch_url for pages.","parameters":{"type":"object","additionalProperties":false,"properties":{"url":{"type":"string","description":"The direct image URL"}},"required":["url"]}}}`),
	OutsideTransaction: true,
	Execute: func(ctx context.Context, deps Dependencies, _ *database.Queries, userID uuid.UUID, arguments string) (string, error) {
		return readImage(ctx, deps, userID, json.RawMessage(arguments))
	},
}

// readImage 下载并校验图片，写入 OSS 后返回文本段加 oss:// 图片段。发送前再由图片存储换成 base64。
func readImage(ctx context.Context, deps Dependencies, userID uuid.UUID, args json.RawMessage) (string, error) {
	var input struct {
		URL string `json:"url"`
	}
	if err := decodeArguments(args, &input); err != nil {
		return "", err
	}
	page, err := url.Parse(input.URL)
	if err != nil || page.Hostname() == "" || (page.Scheme != "http" && page.Scheme != "https") || page.User != nil {
		return "", errors.New("read_image requires an HTTP or HTTPS image URL without credentials")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, page.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AetherBot/1.0)")
	response, err := imageClient.Do(request)
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return "", errors.New("image request timed out")
		}
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("image HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, images.MaxImageBytes+1))
	if err != nil {
		return "", err
	}
	mime, err := images.ValidateBytes(data)
	if err != nil {
		return "", err
	}
	image, err := (images.Store{Services: deps.Services}).Put(ctx, userID, "image", data)
	if err != nil {
		return "", err
	}
	source := request.URL.String()
	text := fmt.Sprintf("Image fetched from %s. MIME %s, %d bytes.", source, mime, image.Size)
	if response.Request != nil && response.Request.URL != nil && response.Request.URL.String() != source {
		text = fmt.Sprintf("Image fetched from %s (redirected to %s). MIME %s, %d bytes.", source, response.Request.URL.String(), mime, image.Size)
	}
	encoded, err := json.Marshal([]any{
		map[string]string{"type": "text", "text": text},
		map[string]any{"type": "image_url", "image_url": map[string]string{"url": image.StorageURL}},
	})
	return string(encoded), err
}

// imageClient 只连接公网地址，最多跟随 3 次跳转；不走环境代理，避免代理绕过地址检查。
var imageClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           dialPublicImage,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
	CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 4 {
			return errors.New("image URL exceeded 3 redirects")
		}
		if request.URL == nil || request.URL.Hostname() == "" || request.URL.User != nil || (request.URL.Scheme != "http" && request.URL.Scheme != "https") {
			return errors.New("image URL redirected to an unsupported URL")
		}
		return nil
	},
}

// dialPublicImage 在真正连接前解析地址，只拨公网 IP，拒绝本机、内网和链路本地地址。
func dialPublicImage(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("image URL lookup failed: %w", err)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var dialErr error
	denied := false
	for _, ip := range ips {
		if !publicImageIP(ip.IP) {
			denied = true
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err != nil {
			dialErr = err
			continue
		}
		return conn, nil
	}
	if dialErr != nil {
		return nil, dialErr
	}
	if denied {
		return nil, errors.New("image URL resolves to a non-public address")
	}
	return nil, errors.New("image URL has no address")
}

// publicImageIP 接受全球单播地址，排除私网。Go 的 IsGlobalUnicast 仍会把私网当成单播。
func publicImageIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate()
}
