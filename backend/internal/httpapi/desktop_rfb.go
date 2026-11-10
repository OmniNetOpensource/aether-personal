// 本文件解析只读 RFB 流，只允许画面协商与刷新请求通过，拒绝键鼠、剪贴板写入和远程控制。

package httpapi

import (
	"encoding/binary"
	"errors"
	"io"
)

// relayDesktopHandshake 协商共享的 RFB 3.8 显示连接；应用和 FC 已完成两层鉴权。
func relayDesktopHandshake(provider, viewer io.ReadWriter) error {
	version := make([]byte, 12)
	for _, endpoints := range [][2]io.ReadWriter{{provider, viewer}, {viewer, provider}} {
		if _, err := io.ReadFull(endpoints[0], version); err != nil {
			return err
		}
		if string(version) != "RFB 003.008\n" {
			return errors.New("unsupported desktop handshake")
		}
		if _, err := endpoints[1].Write(version); err != nil {
			return err
		}
	}
	security := make([]byte, 1)
	if _, err := io.ReadFull(provider, security); err != nil {
		return err
	}
	count := int(security[0])
	if count == 0 || count > 32 {
		return errors.New("unsupported desktop security")
	}
	security = append(security, make([]byte, count)...)
	if _, err := io.ReadFull(provider, security[1:]); err != nil {
		return err
	}
	// VNC 仅接受已经通过应用与 FC 鉴权的代理连接，不向浏览器泄露云端凭证。
	found := false
	for _, kind := range security[1:] {
		if kind == 1 {
			found = true
		}
	}
	if !found {
		return errors.New("unsupported desktop security")
	}
	if _, err := viewer.Write([]byte{1, 1}); err != nil {
		return err
	}
	if _, err := io.ReadFull(viewer, security[:1]); err != nil {
		return err
	}
	if security[0] != 1 {
		return errors.New("unsupported desktop security selection")
	}
	if _, err := provider.Write(security[:1]); err != nil {
		return err
	}
	result := make([]byte, 4)
	if _, err := io.ReadFull(provider, result); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(result) != 0 {
		return errors.New("desktop authentication failed")
	}
	if _, err := viewer.Write(result); err != nil {
		return err
	}
	if _, err := io.ReadFull(viewer, security[:1]); err != nil {
		return err
	}
	if security[0] != 1 {
		return errors.New("desktop must use shared mode")
	}
	if _, err := provider.Write(security[:1]); err != nil {
		return err
	}
	initial := make([]byte, 24)
	if _, err := io.ReadFull(provider, initial); err != nil {
		return err
	}
	nameLength := binary.BigEndian.Uint32(initial[20:24])
	if nameLength > 4096 {
		return errors.New("desktop name exceeds limit")
	}
	initial = append(initial, make([]byte, int(nameLength))...)
	if _, err := io.ReadFull(provider, initial[24:]); err != nil {
		return err
	}
	_, err := viewer.Write(initial)
	return err
}

// relayDesktopRequests 按字节流读取客户端消息，不能把 WebSocket 帧边界当成 RFB 消息边界。
func relayDesktopRequests(provider io.Writer, viewer io.Reader) error {
	buffer := make([]byte, 8<<10)
	for {
		if _, err := io.ReadFull(viewer, buffer[:1]); err != nil {
			return err
		}
		length := 0
		switch buffer[0] {
		case 0:
			length = 20 // SetPixelFormat
		case 2:
			if _, err := io.ReadFull(viewer, buffer[1:4]); err != nil {
				return err
			}
			count := int(binary.BigEndian.Uint16(buffer[2:4]))
			if count > 1024 {
				return errors.New("too many desktop encodings")
			}
			length = 4 + 4*count
			if _, err := io.ReadFull(viewer, buffer[4:length]); err != nil {
				return err
			}
		case 3, 150:
			length = 10 // FramebufferUpdateRequest / EnableContinuousUpdates
		case 248:
			if _, err := io.ReadFull(viewer, buffer[1:9]); err != nil {
				return err
			}
			if buffer[8] > 64 {
				return errors.New("desktop fence exceeds limit")
			}
			length = 9 + int(buffer[8])
			if _, err := io.ReadFull(viewer, buffer[9:length]); err != nil {
				return err
			}
		case 6:
			// noVNC 在只读模式仍回复剪贴板能力协商；消费该消息，但绝不向桌面写入剪贴板。
			if _, err := io.ReadFull(viewer, buffer[1:8]); err != nil {
				return err
			}
			length := int64(int32(binary.BigEndian.Uint32(buffer[4:8])))
			if length < 0 {
				length = -length
			}
			if length > 64<<10 {
				return errors.New("desktop clipboard exceeds limit")
			}
			if _, err := io.CopyN(io.Discard, viewer, length); err != nil {
				return err
			}
			continue
		default:
			return errors.New("desktop input is not allowed")
		}
		if buffer[0] == 0 || buffer[0] == 3 || buffer[0] == 150 {
			if _, err := io.ReadFull(viewer, buffer[1:length]); err != nil {
				return err
			}
		}
		if _, err := provider.Write(buffer[:length]); err != nil {
			return err
		}
	}
}
