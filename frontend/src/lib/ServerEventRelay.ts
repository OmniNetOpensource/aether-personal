// 本文件是前端唯一接触聊天 WebSocket 的中转：负责连接与发送命令，并解析后端下行事件转交订阅者。

import type { MessagePart } from './messageAttachments.js';
import type { ServerEvent } from './serverEvents.js';

// 前端通过聊天 WebSocket 发往后端的全部命令；hello 由中转在连接打开时自动发出。
export type ClientCommand =
  | { type: 'request_code'; request_id: string; email: string; turnstile_token: string }
  | { type: 'verify_code'; request_id: string; email: string; code: string }
  | { type: 'dev_login'; request_id: string }
  | { type: 'load_history'; request_id: string; before?: string }
  | { type: 'search_messages'; request_id: string; keyword: string; before?: string }
  | {
      type: 'load_message_context';
      request_id: string;
      message_id: string;
      direction: 'around' | 'older' | 'newer';
    }
  | { type: 'logout'; request_id: string }
  | {
      type: 'send_message';
      client_message_id: string;
      parts: MessagePart[];
    };

// ServerEventRelay 管理单条 WebSocket 生命周期，解析 JSON 事件帧并转发给订阅者。
export class ServerEventRelay {
  private channel?: WebSocket;
  private intentionallyClosed = false;
  private eventListener?: (event: ServerEvent) => void;
  private closeListener?: (reason: string) => void;

  subscribe(onEvent: (event: ServerEvent) => void, onClose: (reason: string) => void): () => void {
    this.eventListener = onEvent;
    this.closeListener = onClose;
    return () => {
      this.eventListener = undefined;
      this.closeListener = undefined;
    };
  }

  connect(baseURL: string, token: string | null, lastChangeCursor?: string) {
    this.close();
    this.intentionallyClosed = false;
    const channel = new WebSocket(baseURL.replace(/^http/, 'ws') + '/ws');
    this.channel = channel;
    channel.onopen = () => {
      if (this.channel !== channel) return;
      channel.send(
        JSON.stringify({
          type: 'hello',
          ...(token ? { token } : {}),
          ...(lastChangeCursor ? { last_change_cursor: lastChangeCursor } : {}),
        }),
      );
    };
    channel.onmessage = (message: MessageEvent<unknown>) => {
      if (this.channel !== channel) return;
      try {
        if (typeof message.data !== 'string') throw new Error('Expected a text frame');
        const parsed: unknown = JSON.parse(message.data);
        if (
          typeof parsed !== 'object' ||
          parsed === null ||
          typeof (parsed as { type?: unknown }).type !== 'string'
        ) {
          throw new Error('Expected event object');
        }
        this.eventListener?.(parsed as ServerEvent);
      } catch {
        this.close();
        this.closeListener?.('invalid');
      }
    };
    channel.onerror = () => {
      if (this.channel === channel) channel.close();
    };
    channel.onclose = (event) => {
      if (this.channel !== channel) return;
      this.channel = undefined;
      if (!this.intentionallyClosed) this.closeListener?.(event.reason);
    };
  }

  send(command: ClientCommand): boolean {
    if (this.channel?.readyState !== WebSocket.OPEN) return false;
    try {
      this.channel.send(JSON.stringify(command));
      return true;
    } catch {
      return false;
    }
  }

  close() {
    this.intentionallyClosed = true;
    const channel = this.channel;
    this.channel = undefined;
    channel?.close();
  }
}
