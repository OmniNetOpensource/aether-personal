// 本文件定义用户引用和图片的输入边界：校验图片字节与消息大小，并把界面数据转换为模型可读的多模态内容。

package httpapi

import (
	"encoding/json"
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

// messagePart 保留用户编辑的多模态顺序，外部输入在 validMessageInput 校验后才落库。
type messagePart struct {
	Type  string        `json:"type"`
	Text  string        `json:"text,omitempty"`
	Quote *messageQuote `json:"quote,omitempty"`
	Image *messageImage `json:"image,omitempty"`
}

// validMessageInput 校验外部输入；纯图片或纯引用也可以发送，大小按解码后的图片字节计算。
func validMessageInput(frame clientFrame, userID uuid.UUID) bool {
	if len(frame.Parts) > 200 || len(frame.Images) > 8 || len(frame.Quotes) > 20 || (strings.TrimSpace(frame.Text) == "" && len(frame.Images) == 0 && len(frame.Quotes) == 0) {
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
	total := 0
	for _, image := range frame.Images {
		if len(image.Name) > 1024 || !images.OwnedReference(image.StorageURL, userID) || image.Size <= 0 || image.Size > images.MaxImageBytes {
			return false
		}
		switch image.MIMEType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return false
		}
		total += image.Size
	}
	return total <= images.MaxMessageImageBytes
}

// normalizeMessageInput 只从 parts 推导文字与附件，避免客户端发送两份互相矛盾的数据；旧纯文本客户端仍可发送 text。
func normalizeMessageInput(frame clientFrame) (clientFrame, bool) {
	if frame.Parts == nil {
		frame.Parts = []messagePart{{Type: "text", Text: frame.Text}}
	}
	frame.Text, frame.Quotes, frame.Images = "", nil, nil
	for _, part := range frame.Parts {
		switch part.Type {
		case "text":
			if part.Quote != nil || part.Image != nil {
				return frame, false
			}
			frame.Text += part.Text
		case "quote":
			if part.Quote == nil || part.Image != nil || part.Text != "" {
				return frame, false
			}
			frame.Quotes = append(frame.Quotes, *part.Quote)
		case "image":
			if part.Image == nil || part.Quote != nil || part.Text != "" {
				return frame, false
			}
			frame.Images = append(frame.Images, *part.Image)
		default:
			return frame, false
		}
	}
	return frame, true
}

// userMessageContent 将引用作为参考文本、图片作为原生 image_url 段保存；界面元信息单独保存，不混入用户输入正文。
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
			persisted[index].Image = &image
			blocks = append(blocks, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image.StorageURL}})
		}
	}
	var content any = blocks
	if len(frame.Parts) == 1 && frame.Parts[0].Type == "text" {
		content = frame.Text
	}
	raw, _ := json.Marshal(map[string]any{"role": "user", "content": content, "user_text": frame.Text, "parts": persisted})
	return raw
}
