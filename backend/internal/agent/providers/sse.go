// 本文件负责各渠道共用的 SSE 接收：逐块读取供应商的事件流，实时通知上层已收到的载荷，收集完整分片并保留原始字节供日志存档。

package providers

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// MaxResponseBytes 是流式分片累计的上限，与此前一次性读取的 8 MiB 上限一致，超出按无效响应处理。
const MaxResponseBytes = 8 << 20

// ReadSSEPayloads 逐行读取 SSE 事件流：合并事件的 data 行后立即回调；回调失败或流中断时仍交回截至该事件的存档。
// 返回的 truncated 表示累计原始字节超过上限；流被中途断开时读错误原样返回，调用方按可重试的传输失败处理。
func ReadSSEPayloads(body io.Reader, onPayload func([]byte) error) (payloads [][]byte, captured []byte, truncated bool, err error) {
	reader := bufio.NewReaderSize(body, 32*1024)
	var raw bytes.Buffer
	var dataLines []string
	// flushEvent 在事件之间的空行处把已攒的 data 行合成一条完整载荷。
	flushEvent := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := []byte(strings.Join(dataLines, "\n"))
		payloads = append(payloads, payload)
		dataLines = nil
		if onPayload != nil {
			return onPayload(payload)
		}
		return nil
	}
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			raw.WriteString(line)
			if raw.Len() > MaxResponseBytes {
				return payloads, raw.Bytes()[:MaxResponseBytes], true, nil
			}
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case trimmed == "":
				if err := flushEvent(); err != nil {
					return payloads, raw.Bytes(), false, err
				}
			case strings.HasPrefix(trimmed, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
			}
		}
		if readErr == io.EOF {
			err := flushEvent()
			return payloads, raw.Bytes(), false, err
		}
		if readErr != nil {
			return payloads, raw.Bytes(), false, readErr
		}
	}
}
