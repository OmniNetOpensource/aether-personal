// 本文件定义用户引用、图片与工作区文件的输入边界：校验大小与对象归属，并把界面数据转换为模型可读的多模态内容。

package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
	"github.com/google/uuid"
)

// messageQuote 保存引用文本快照与原消息位置，重新加载历史后仍可回到来源。
type messageQuote struct {
	Text      string    `json:"text"`
	MessageID uuid.UUID `json:"message_id"`
	Start     int       `json:"start"`
	End       int       `json:"end"`
}

// messageImage 共用 OSS 存储引用与展示结构，签名 URL 只在发往前端时填入。
type messageImage = images.Image

// messageFile 共用工作区文件的 OSS 持久引用与展示结构，签名下载 URL 只在发往前端时填入。
type messageFile = images.File

// messagePart 保留用户与助手消息的多模态顺序，外部输入在 validMessageInput 校验后才落库。
type messagePart struct {
	Type  string        `json:"type"`
	Text  string        `json:"text,omitempty"`
	Quote *messageQuote `json:"quote,omitempty"`
	Image *messageImage `json:"image,omitempty"`
	File  *messageFile  `json:"file,omitempty"`
}

// validMessageInput 校验外部输入；纯图片、纯文件或纯引用也可以发送，图片与文件合计不超过 50 MiB。
func validMessageInput(frame clientFrame, userID uuid.UUID) bool {
	if len(frame.Parts) > 200 || len(frame.Images) > 8 || len(frame.Files) > 10 || len(frame.Quotes) > 20 ||
		(strings.TrimSpace(frame.Text) == "" && len(frame.Images) == 0 && len(frame.Files) == 0 && len(frame.Quotes) == 0) {
		return false
	}
	characters := utf8.RuneCountInString(frame.Text)
	for _, quote := range frame.Quotes {
		if strings.TrimSpace(quote.Text) == "" || quote.MessageID == uuid.Nil || quote.Start < 0 || quote.End <= quote.Start {
			return false
		}
		characters += utf8.RuneCountInString(quote.Text)
	}
	if characters > maxMessageCharacters {
		return false
	}
	var imageTotal int
	var attachmentTotal int64
	for _, image := range frame.Images {
		if len(image.Name) > 1024 || !images.OwnedReference(image.StorageURL, userID) || image.Size <= 0 || image.Size > images.MaxImageBytes {
			return false
		}
		switch image.MIMEType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return false
		}
		imageTotal += image.Size
		attachmentTotal += int64(image.Size)
	}
	if imageTotal > images.MaxMessageImageBytes {
		return false
	}
	for _, file := range frame.Files {
		if len(file.Name) == 0 || len(file.Name) > 1024 || !images.OwnedFileReference(file.StorageURL, userID) ||
			file.Size <= 0 || file.Size > images.MaxFileBytes || len(file.MIMEType) > 128 {
			return false
		}
		attachmentTotal += file.Size
	}
	return attachmentTotal <= images.MaxFileBytes
}

// normalizeMessageInput 只从 parts 推导文字与附件，避免客户端发送两份互相矛盾的数据；旧纯文本客户端仍可发送 text。
func normalizeMessageInput(frame clientFrame) (clientFrame, bool) {
	if frame.Parts == nil {
		frame.Parts = []messagePart{{Type: "text", Text: frame.Text}}
	}
	frame.Text, frame.Quotes, frame.Images, frame.Files = "", nil, nil, nil
	for _, part := range frame.Parts {
		switch part.Type {
		case "text":
			if part.Quote != nil || part.Image != nil || part.File != nil {
				return frame, false
			}
			frame.Text += part.Text
		case "quote":
			if part.Quote == nil || part.Image != nil || part.File != nil || part.Text != "" {
				return frame, false
			}
			frame.Quotes = append(frame.Quotes, *part.Quote)
		case "image":
			if part.Image == nil || part.Quote != nil || part.File != nil || part.Text != "" {
				return frame, false
			}
			frame.Images = append(frame.Images, *part.Image)
		case "file":
			if part.File == nil || part.Quote != nil || part.Image != nil || part.Text != "" {
				return frame, false
			}
			frame.Files = append(frame.Files, *part.File)
		default:
			return frame, false
		}
	}
	return frame, true
}

// userMessageContent 将引用与工作区附件的持久 oss:// 引用写入模型可读文本块、视觉图片同时保留为 image_url 段；界面元信息单独保存在 parts 中。
func userMessageContent(frame clientFrame) json.RawMessage {
	blocks := []any{}
	persisted := make([]messagePart, len(frame.Parts))
	for index, part := range frame.Parts {
		persisted[index] = part
		switch part.Type {
		case "text":
			blocks = append(blocks, map[string]string{"type": "text", "text": part.Text})
		case "quote":
			blocks = append(blocks, map[string]string{"type": "text", "text": "引用（参考原文）：\n> " + strings.ReplaceAll(part.Quote.Text, "\n", "\n> ")})
		case "image":
			image := *part.Image
			image.URL = ""
			image.Name = images.CleanFileName(image.Name)
			persisted[index].Image = &image
			blocks = append(blocks,
				map[string]any{"type": "image_url", "image_url": map[string]string{"url": image.StorageURL}},
				map[string]string{"type": "text", "text": fmt.Sprintf("[Attached image: %s (%d bytes, %s, source: %s)]", image.Name, image.Size, image.MIMEType, image.StorageURL)},
			)
		case "file":
			file := *part.File
			file.URL = ""
			file.Name = images.CleanFileName(file.Name)
			persisted[index].File = &file
			blocks = append(blocks, map[string]string{
				"type": "text",
				"text": fmt.Sprintf("[Attached file: %s (%d bytes, %s, source: %s) — call sandbox_import_file to copy it into /mnt/workspace before reading it on the computer]", file.Name, file.Size, file.MIMEType, file.StorageURL),
			})
		}
	}
	var content any = blocks
	if len(frame.Parts) == 1 && frame.Parts[0].Type == "text" {
		content = frame.Text
	}
	raw, _ := json.Marshal(map[string]any{"role": "user", "content": content, "user_text": frame.Text, "parts": persisted})
	return raw
}
