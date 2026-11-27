// 本文件是前端唯一接触聊天 WebSocket 的中转：通过 Effect Scope 管理已登录用户的单向推送连接，并用 Schema 校验解码下行事件。

import { Effect, Queue, Result, Schema, Scope } from 'effect';
import { WsConnectionError } from './errors.js';
import { ServerEventFromJsonSchema } from './serverEvents.js';
import type { ServerEvent } from './serverEvents.js';

// RelaySignal 将单条 WebSocket 的下行事件与关闭原因统一排入队列，供控制器按序消费。
export type RelaySignal =
  | { readonly _tag: 'Event'; readonly event: ServerEvent }
  | { readonly _tag: 'Closed'; readonly reason: string };

// ServerEventConnection 暴露一条活跃推送连接的信号队列与主动关闭能力。
export interface ServerEventConnection {
  readonly signals: Queue.Dequeue<RelaySignal>;
  readonly close: Effect.Effect<void>;
}

const decodeServerEvent = Schema.decodeUnknownResult(ServerEventFromJsonSchema);

// openServerEventConnection 在当前 Scope 内为已登录会话建立聊天推送 WebSocket，Scope 结束时自动释放连接。
export function openServerEventConnection(
  baseURL: string,
  token: string,
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
            token,
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
      close: Effect.sync(() => {
        if (!intentionallyClosed) channel.close();
      }),
    };
  });
}
