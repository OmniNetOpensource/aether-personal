// 本文件是前端唯一接触聊天 WebSocket 的中转：通过 Effect Scope 管理连接生命周期、发送命令，并用 Schema 校验解码下行事件。

import { Effect, Queue, Result, Schema, Scope } from 'effect';
import { WsConnectionError } from './errors.js';
import type { MessagePart } from './messageAttachments.js';
import { ServerEventFromJsonSchema } from './serverEvents.js';
import type { ServerEvent } from './serverEvents.js';

// ClientCommand 枚举前端通过聊天 WebSocket 发往后端的全部命令；hello 由中转在连接打开时自动发出。
export type ClientCommand =
  | {
      readonly type: 'request_code';
      readonly request_id: string;
      readonly email: string;
      readonly turnstile_token: string;
    }
  | {
      readonly type: 'verify_code';
      readonly request_id: string;
      readonly email: string;
      readonly code: string;
    }
  | { readonly type: 'dev_login'; readonly request_id: string }
  | { readonly type: 'load_history'; readonly request_id: string; readonly before?: string }
  | {
      readonly type: 'search_messages';
      readonly request_id: string;
      readonly keyword: string;
      readonly before?: string;
    }
  | {
      readonly type: 'load_message_context';
      readonly request_id: string;
      readonly message_id: string;
      readonly direction: 'around' | 'older' | 'newer';
    }
  | { readonly type: 'logout'; readonly request_id: string }
  | {
      readonly type: 'send_message';
      readonly client_message_id: string;
      readonly parts: readonly MessagePart[];
    };

// RelaySignal 将单条 WebSocket 的下行事件与关闭原因统一排入队列，供控制器按序消费。
export type RelaySignal =
  | { readonly _tag: 'Event'; readonly event: ServerEvent }
  | { readonly _tag: 'Closed'; readonly reason: string };

// ServerEventConnection 暴露一条活跃 WebSocket 连接的信号队列、发送命令与主动关闭能力。
export interface ServerEventConnection {
  readonly signals: Queue.Dequeue<RelaySignal>;
  readonly send: (command: ClientCommand) => Effect.Effect<void, WsConnectionError>;
  readonly close: Effect.Effect<void>;
}

const decodeServerEvent = Schema.decodeUnknownResult(ServerEventFromJsonSchema);

// openServerEventConnection 在当前 Scope 内建立单条聊天 WebSocket，Scope 结束时自动释放连接。
export function openServerEventConnection(
  baseURL: string,
  token: string | null,
  lastChangeCursor?: string,
): Effect.Effect<ServerEventConnection, WsConnectionError, Scope.Scope> {
  return Effect.gen(function* () {
    const signals = yield* Queue.unbounded<RelaySignal>();
    let intentionallyClosed = false;
    let closedEmitted = false;

    const emitClosed = (reason: string) => {
      if (closedEmitted) return;
      closedEmitted = true;
      Effect.runSync(Queue.offer(signals, { _tag: 'Closed', reason }));
    };

    const channel = yield* Effect.acquireRelease(
      Effect.try({
        try: () => new WebSocket(`${baseURL.replace(/^http/, 'ws')}/ws`),
        catch: () =>
          new WsConnectionError({ reason: 'connect', message: '连接已断开，请稍后重试' }),
      }),
      (ws) =>
        Effect.sync(() => {
          intentionallyClosed = true;
          ws.onopen = null;
          ws.onmessage = null;
          ws.onerror = null;
          ws.onclose = null;
          ws.close();
        }),
    );

    channel.onopen = () => {
      if (intentionallyClosed) return;
      try {
        channel.send(
          JSON.stringify({
            type: 'hello',
            ...(token ? { token } : {}),
            ...(lastChangeCursor ? { last_change_cursor: lastChangeCursor } : {}),
          }),
        );
      } catch {
        channel.close();
      }
    };

    channel.onmessage = (message: MessageEvent<unknown>) => {
      if (intentionallyClosed) return;
      if (typeof message.data !== 'string') {
        intentionallyClosed = true;
        channel.close();
        emitClosed('invalid');
        return;
      }
      const decoded = decodeServerEvent(message.data);
      if (Result.isFailure(decoded)) {
        intentionallyClosed = true;
        channel.close();
        emitClosed('invalid');
        return;
      }
      Effect.runSync(Queue.offer(signals, { _tag: 'Event', event: decoded.success }));
    };

    channel.onerror = () => {
      if (!intentionallyClosed) channel.close();
    };

    channel.onclose = (event) => {
      if (!intentionallyClosed) emitClosed(event.reason);
    };

    return {
      signals,
      send: (command) =>
        Effect.try({
          try: () => {
            if (channel.readyState !== WebSocket.OPEN) {
              throw new Error('socket not open');
            }
            channel.send(JSON.stringify(command));
          },
          catch: () =>
            new WsConnectionError({ reason: 'closed', message: '连接已断开，请稍后重试' }),
        }),
      close: Effect.sync(() => {
        if (!intentionallyClosed) channel.close();
      }),
    };
  });
}
