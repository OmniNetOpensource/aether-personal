// 本文件是前端唯一接触聊天 WebSocket 的中转：负责连接与发送命令，把后端原始帧校验成服务器事件后转交订阅者。

import { serverEventSchema } from './serverEvents.js';
import type { ServerEvent } from './serverEvents.js';

// 前端通过聊天 WebSocket 发往后端的全部命令；hello 由中转在连接打开时自动发出。
export type ClientCommand =
  | { type: 'request_code'; request_id: string; email: string; turnstile_token: string }
  | { type: 'verify_code'; request_id: string; email: string; code: string }
  | { type: 'dev_login'; request_id: string }
  | { type: 'load_history'; request_id: string; before?: string }
  | { type: 'logout'; request_id: string }
  | { type: 'send_message'; client_message_id: string; text: string };

// ServerEventRelay 是前端唯一接触聊天 WebSocket 的文件：
// 它负责连接、发送 hello 与命令，并把后端原始帧校验为 serverEvents.ts
// 定义的事件后中转给订阅者。断线原因原样转交，业务状态由订阅者维护。
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

  connect(baseURL: string, token: string | null, lastPosition?: string) {
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
          ...(lastPosition ? { last_position: lastPosition } : {}),
        }),
      );
    };
    channel.onmessage = (message: MessageEvent<unknown>) => {
      if (this.channel !== channel) return;
      try {
        if (typeof message.data !== 'string') throw new Error('Expected a text frame');
        this.eventListener?.(serverEventSchema.parse(JSON.parse(message.data)));
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
