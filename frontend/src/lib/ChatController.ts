// 本文件是纯 Effect 聊天会话控制器：用 Scope、FiberHandle、Deferred 与 SubscriptionRef 管理登录、重连、命令回执与消息窗口。

import { Deferred, Effect, FiberHandle, Queue, Scope, SubscriptionRef } from 'effect';
import {
  StorageError,
  WsConnectionError,
  WsRequestError,
  formatUserError,
  requestFailedMessage,
} from './errors.js';
import type { MessagePart } from './messageAttachments.js';
import { SessionStorage } from './platform.js';
import { openServerEventConnection } from './ServerEventRelay.js';
import type { ClientCommand, ServerEventConnection } from './ServerEventRelay.js';
import type { ChatMessageRow, ChatPage, ServerEvent } from './serverEvents.js';

// ChatMessage 是服务器已保存的聊天消息，发送中的内容留在输入框里。
export interface ChatMessage {
  readonly id: string;
  readonly text: string;
  readonly parts: readonly MessagePart[];
  readonly isUser: boolean;
  readonly createdAt: Date;
  readonly position: string;
}

// ChatState 是对外发布的不可变界面状态快照，供 SubscriptionRef 与细粒度 Atom 订阅。
export interface ChatState {
  readonly messages: readonly ChatMessage[];
  readonly initializing: boolean;
  readonly connected: boolean;
  readonly mainThinking: boolean;
  readonly mainFailed: boolean;
  readonly loadingHistory: boolean;
  readonly hasMoreHistory: boolean;
  readonly email: string;
  readonly iconURL: string;
  readonly devLoginAvailable: boolean;
  readonly desktopAvailable: boolean;
  readonly turnstileSiteKey: string;
  readonly error: string | null;
  readonly token: string | null;
}

// initialChatState 作为首帧加载前的默认状态，保持各字段不可变引用稳定。
export const initialChatState: ChatState = {
  messages: [],
  initializing: true,
  connected: false,
  mainThinking: false,
  mainFailed: false,
  loadingHistory: false,
  hasMoreHistory: true,
  email: '',
  iconURL: '/app-icon.png',
  devLoginAvailable: false,
  desktopAvailable: false,
  turnstileSiteKey: '',
  error: null,
  token: null,
};

// 位于最新底部时主列表保留的最近消息条数；超出部分的旧历史会从头部回收，再次上翻时按游标向后端重新拉取。
export const latestMessageWindow = 30;

// 断线自动重连退避阶梯（秒），连接完成 ready 握手后重置为首项。
const reconnectDelaysSeconds = [1, 2, 4, 8, 15] as const;

// chatMessageFromRow 在服务器消息进入界面时统一转换时间和多段内容，正常聊天与历史窗口共用。
export function chatMessageFromRow(row: ChatMessageRow): ChatMessage {
  return {
    id: row.id,
    text: row.text,
    parts: row.parts ?? (row.text ? [{ type: 'text', text: row.text }] : []),
    isUser: row.sender === 'user',
    createdAt: new Date(row.created_at),
    position: row.position,
  };
}

export type ChatCommandError = WsRequestError | WsConnectionError | StorageError;

// ChatSession 对外暴露只读状态流及所有聊天命令，完全不依赖 React。
export interface ChatSession {
  readonly stateRef: SubscriptionRef.SubscriptionRef<ChatState>;
  readonly getState: Effect.Effect<ChatState>;
  readonly reconnect: Effect.Effect<void>;
  readonly suspend: Effect.Effect<void>;
  readonly requestCode: (
    email: string,
    turnstileToken: string,
  ) => Effect.Effect<void, ChatCommandError>;
  readonly signIn: (email: string, code: string) => Effect.Effect<void, ChatCommandError>;
  readonly devSignIn: Effect.Effect<void, ChatCommandError>;
  readonly signOut: Effect.Effect<void>;
  readonly loadHistory: (older?: boolean) => Effect.Effect<void>;
  readonly searchMessages: (
    keyword: string,
    before?: string,
  ) => Effect.Effect<Extract<ServerEvent, { readonly type: 'search_results' }>, ChatCommandError>;
  readonly loadMessageContext: (
    messageID: string,
    direction: 'around' | 'older' | 'newer',
  ) => Effect.Effect<Extract<ServerEvent, { readonly type: 'message_context' }>, ChatCommandError>;
  readonly trimOlderMessages: (maxCount?: number) => Effect.Effect<number>;
  readonly send: (
    parts: readonly MessagePart[],
    clientMessageID: string,
  ) => Effect.Effect<boolean, ChatCommandError>;
}

// mergeRows 把服务器发来的一页消息合并到已有列表并按 position 排序；无变化时返回原数组引用。
function mergeRows(
  current: readonly ChatMessage[],
  page: ChatPage,
): { readonly messages: readonly ChatMessage[]; readonly changed: boolean } {
  const rows = page.messages ?? [];
  if (rows.length === 0) return { messages: current, changed: false };
  const next = current.slice();
  for (const row of rows) {
    const message = chatMessageFromRow(row);
    const index = next.findIndex((existing) => existing.id === row.id);
    if (index < 0) next.push(message);
    else next[index] = message;
  }
  next.sort((a, b) => a.position.localeCompare(b.position));
  return { messages: next, changed: true };
}

// makeChatSession 在调用方 Scope 内创建独立聊天会话；Scope 关闭时自动中断连接与所有挂起请求。
export const makeChatSession: Effect.Effect<ChatSession, never, Scope.Scope | SessionStorage> =
  Effect.gen(function* () {
    const storage = yield* SessionStorage;
    const saved = yield* storage.readSession;
    const stateRef = yield* SubscriptionRef.make<ChatState>({
      ...initialChatState,
      token: saved.token,
      email: saved.email,
      iconURL: saved.iconURL,
    });

    const baseURL = saved.baseURL;
    const connectionHandle = yield* FiberHandle.make<void, never>();
    const readyTimeoutHandle = yield* FiberHandle.make<void, never>();
    const devLoginHandle = yield* FiberHandle.make<void, never>();

    const pendingRequests = new Map<string, Deferred.Deferred<ServerEvent, ChatCommandError>>();
    let activeConnection: ServerEventConnection | null = null;
    let reconnectAttempt = 0;
    let historyLoaded = false;
    let suspended = false;
    let devLoginAttempted = false;
    // 重连按变更序号续推，历史分页只按已读取的历史页推进；迟到的旧回复不应跳过中间一页。
    let lastChangeCursor: string | undefined;
    let oldestHistoryPosition: string | undefined;

    const getState = SubscriptionRef.get(stateRef);
    const updateState = (f: (state: ChatState) => ChatState) => SubscriptionRef.update(stateRef, f);

    const rejectPendingRequests = (error: WsConnectionError) =>
      Effect.gen(function* () {
        const entries = [...pendingRequests.values()];
        pendingRequests.clear();
        for (const deferred of entries) {
          yield* Deferred.fail(deferred, error);
        }
      });

    const resolvePendingRequest = (requestID: string, event: ServerEvent) =>
      Effect.gen(function* () {
        const deferred = pendingRequests.get(requestID);
        if (!deferred) return;
        pendingRequests.delete(requestID);
        yield* Deferred.succeed(deferred, event);
      });

    const failPendingRequest = (requestID: string, error: ChatCommandError) =>
      Effect.gen(function* () {
        const deferred = pendingRequests.get(requestID);
        if (!deferred) return false;
        pendingRequests.delete(requestID);
        yield* Deferred.fail(deferred, error);
        return true;
      });

    const clearLocalSession = Effect.gen(function* () {
      historyLoaded = false;
      lastChangeCursor = undefined;
      oldestHistoryPosition = undefined;
      yield* storage.clearSession;
      yield* updateState((state) => ({
        ...state,
        token: null,
        desktopAvailable: false,
        mainThinking: false,
        mainFailed: false,
        loadingHistory: false,
        hasMoreHistory: true,
        messages: [],
      }));
    });

    // mergeHistory 记住连续翻过的最早位置；首屏超过窗口条数时只保留最新窗口并允许继续上翻。
    const applyHistory = (state: ChatState, page: ChatPage): ChatState => {
      const rows = page.messages ?? [];
      const initialLoad = !historyLoaded;
      const first = rows[0]?.position;
      if (first && (!oldestHistoryPosition || first < oldestHistoryPosition)) {
        oldestHistoryPosition = first;
      }
      const merged = mergeRows(state.messages, page).messages;
      historyLoaded = true;
      let messages = merged;
      let trimmed = false;
      if (initialLoad && messages.length > latestMessageWindow) {
        messages = messages.slice(messages.length - latestMessageWindow);
        oldestHistoryPosition = messages[0]?.position;
        trimmed = true;
      }
      return {
        ...state,
        messages,
        hasMoreHistory: trimmed || rows.length > 0,
      };
    };

    // sendRequest 发出命令并等待服务端回执：发送消息按 client_message_id 匹配，其余按 request_id 匹配。
    const sendRequest = (command: ClientCommand): Effect.Effect<ServerEvent, ChatCommandError> =>
      Effect.gen(function* () {
        const connection = activeConnection;
        if (!connection) {
          return yield* Effect.fail(
            new WsConnectionError({ reason: 'closed', message: '连接已断开，请稍后重试' }),
          );
        }
        const requestID =
          command.type === 'send_message' ? command.client_message_id : command.request_id;
        const deferred = yield* Deferred.make<ServerEvent, ChatCommandError>();
        pendingRequests.set(requestID, deferred);
        const timeout = command.type === 'request_code' ? '30 seconds' : '15 seconds';

        return yield* connection.send(command).pipe(
          Effect.andThen(
            Deferred.await(deferred).pipe(
              Effect.timeoutOrElse({
                duration: timeout,
                orElse: () =>
                  Effect.fail(
                    new WsConnectionError({
                      reason: 'timeout',
                      message: '连接超时，请检查网络后重试',
                    }),
                  ),
              }),
            ),
          ),
          Effect.ensuring(
            Effect.sync(() => {
              if (pendingRequests.get(requestID) === deferred) {
                pendingRequests.delete(requestID);
              }
            }),
          ),
        );
      });

    const devSignIn: Effect.Effect<void, ChatCommandError> = sendRequest({
      type: 'dev_login',
      request_id: crypto.randomUUID(),
    }).pipe(Effect.asVoid);

    // handleServerEvent 按序处理单条下行事件，驱动认证、历史合并、思考状态和挂起请求回执。
    const handleServerEvent = (event: ServerEvent): Effect.Effect<void> =>
      Effect.gen(function* () {
        switch (event.type) {
          case 'ready': {
            yield* FiberHandle.clear(readyTimeoutHandle);
            reconnectAttempt = 0;
            const current = yield* getState;
            if (event.authenticated) {
              lastChangeCursor = event.change_cursor;
              yield* updateState((state) => {
                let next: ChatState = {
                  ...state,
                  devLoginAvailable: event.dev_login,
                  turnstileSiteKey: event.turnstile_site_key ?? '',
                  error: null,
                  connected: true,
                  desktopAvailable: event.desktop_available === true,
                  email: event.email ?? state.email,
                  initializing: false,
                };
                if (event.history) next = applyHistory(next, event.history);
                return next;
              });
              return;
            }
            if (current.token) {
              yield* storage.clearSession;
            }
            const shouldDevLogin = event.dev_login && !devLoginAttempted;
            if (shouldDevLogin) devLoginAttempted = true;
            yield* updateState((state) => ({
              ...state,
              devLoginAvailable: event.dev_login,
              turnstileSiteKey: event.turnstile_site_key ?? '',
              error: null,
              connected: false,
              desktopAvailable: false,
              token: null,
              initializing: shouldDevLogin ? state.initializing : false,
            }));
            if (shouldDevLogin) {
              yield* FiberHandle.run(
                devLoginHandle,
                devSignIn.pipe(
                  Effect.catch((error) =>
                    updateState((state) => ({
                      ...state,
                      error: formatUserError(error, '开发测试登录失败，请重试'),
                      initializing: false,
                    })),
                  ),
                ),
              );
            }
            return;
          }
          case 'authenticated': {
            lastChangeCursor = event.change_cursor;
            const savedResult = yield* storage
              .saveSession(event.token, event.email)
              .pipe(Effect.result);
            if (savedResult._tag === 'Failure') {
              yield* updateState((state) => ({ ...state, initializing: false }));
              yield* failPendingRequest(event.request_id, savedResult.failure);
              return;
            }
            yield* updateState((state) =>
              applyHistory(
                {
                  ...state,
                  token: event.token,
                  email: event.email,
                  desktopAvailable: event.desktop_available === true,
                  connected: true,
                  error: null,
                  initializing: false,
                },
                event.history,
              ),
            );
            yield* resolvePendingRequest(event.request_id, event);
            return;
          }
          case 'code_sent':
          case 'search_results':
          case 'message_context':
            yield* resolvePendingRequest(event.request_id, event);
            return;
          case 'history':
            yield* updateState((state) => applyHistory({ ...state, error: null }, event));
            yield* resolvePendingRequest(event.request_id, event);
            return;
          case 'messages': {
            lastChangeCursor = event.change_cursor;
            const nextThinking = event.main_thinking === true;
            const nextFailed = event.main_failed === true;
            yield* updateState((state) => {
              const merged = mergeRows(state.messages, event);
              const statusChanged =
                !state.connected ||
                state.mainThinking !== nextThinking ||
                state.mainFailed !== nextFailed ||
                state.error !== null;
              if (!statusChanged && !merged.changed) return state;
              return {
                ...state,
                connected: true,
                mainThinking: nextThinking,
                mainFailed: nextFailed,
                error: null,
                messages: merged.messages,
              };
            });
            return;
          }
          case 'delivered':
            yield* resolvePendingRequest(event.client_message_id, event);
            return;
          case 'logged_out':
            yield* clearLocalSession;
            yield* resolvePendingRequest(event.request_id, event);
            return;
          case 'request_failed': {
            const matched = yield* failPendingRequest(
              event.request_id,
              WsRequestError.fromCode(event.code),
            );
            if (!matched) {
              yield* updateState((state) => ({
                ...state,
                error: requestFailedMessage(event.code),
              }));
            }
            return;
          }
        }
      });

    // runConnectionLoop 串行驱动单条 WebSocket 生命周期与断线指数退避重连，直到被挂起、鉴权失败或 Scope 释放。
    const runConnectionLoop: Effect.Effect<void> = Effect.gen(function* () {
      while (!suspended && navigator.onLine) {
        const current = yield* getState;
        const closeReason = yield* Effect.scoped(
          Effect.gen(function* () {
            const connection = yield* openServerEventConnection(
              baseURL,
              current.token,
              lastChangeCursor,
            ).pipe(Effect.option);
            if (connection._tag === 'None') return '';

            activeConnection = connection.value;
            yield* FiberHandle.run(
              readyTimeoutHandle,
              Effect.sleep('10 seconds').pipe(Effect.andThen(connection.value.close)),
            );

            return yield* Effect.gen(function* () {
              while (true) {
                const signal = yield* Queue.take(connection.value.signals);
                if (signal._tag === 'Closed') return signal.reason;
                yield* handleServerEvent(signal.event);
              }
            }).pipe(
              Effect.ensuring(
                Effect.gen(function* () {
                  activeConnection = null;
                  yield* FiberHandle.clear(readyTimeoutHandle);
                  yield* updateState((state) =>
                    state.connected || state.mainThinking
                      ? { ...state, connected: false, mainThinking: false }
                      : state,
                  );
                  yield* rejectPendingRequests(
                    new WsConnectionError({ reason: 'closed', message: '连接已断开，请稍后重试' }),
                  );
                }),
              ),
            );
          }),
        );

        if (closeReason === 'unauthorized') {
          yield* clearLocalSession;
          yield* updateState((state) => ({
            ...state,
            error: '登录已过期，请重新登录',
            initializing: false,
          }));
          return;
        }
        if (suspended || !navigator.onLine) return;

        const delaySeconds =
          reconnectDelaysSeconds[Math.min(reconnectAttempt++, reconnectDelaysSeconds.length - 1)] ??
          15;
        yield* Effect.sleep(`${delaySeconds} seconds`);
      }
    });

    const startConnectionLoop = (forceRestart: boolean) =>
      Effect.gen(function* () {
        if (suspended || !navigator.onLine) return;
        if (!forceRestart && activeConnection) return;
        yield* FiberHandle.run(connectionHandle, runConnectionLoop, {
          onlyIfMissing: !forceRestart,
        });
      });

    const reconnect = Effect.gen(function* () {
      suspended = false;
      yield* startConnectionLoop(false);
    });

    const suspend = Effect.gen(function* () {
      suspended = true;
      yield* FiberHandle.clear(connectionHandle);
      yield* FiberHandle.clear(readyTimeoutHandle);
    });

    // 初始化：联网时立即建立连接，离线冷启动直接开放界面编辑草稿或展示离线提示。
    if (navigator.onLine) {
      yield* startConnectionLoop(true);
    } else {
      yield* updateState((state) => ({
        ...state,
        error: state.token ? state.error : '当前处于离线状态，恢复网络后即可登录',
        initializing: false,
      }));
    }

    // Scope 释放时收尾关闭连接并通知所有尚未返回的挂起请求。
    yield* Effect.addFinalizer(() =>
      Effect.gen(function* () {
        suspended = true;
        yield* FiberHandle.clear(connectionHandle);
        yield* FiberHandle.clear(readyTimeoutHandle);
        yield* FiberHandle.clear(devLoginHandle);
        yield* rejectPendingRequests(
          new WsConnectionError({ reason: 'disposed', message: '连接已关闭' }),
        );
      }),
    );

    return {
      stateRef,
      getState,
      reconnect,
      suspend,
      requestCode: (email, turnstileToken) =>
        sendRequest({
          type: 'request_code',
          request_id: crypto.randomUUID(),
          email: email.trim(),
          turnstile_token: turnstileToken,
        }).pipe(Effect.asVoid),

      signIn: (email, code) =>
        sendRequest({
          type: 'verify_code',
          request_id: crypto.randomUUID(),
          email: email.trim(),
          code: code.trim(),
        }).pipe(Effect.asVoid),

      devSignIn,

      signOut: Effect.gen(function* () {
        const current = yield* getState;
        let loggedOut = false;
        if (current.token && activeConnection) {
          const result = yield* sendRequest({
            type: 'logout',
            request_id: crypto.randomUUID(),
          }).pipe(Effect.result);
          loggedOut = result._tag === 'Success';
        }
        yield* clearLocalSession;
        // 未收到 logged_out 时服务端仍把连接绑在旧会话上，重启连接以未登录身份重新握手。
        if (!loggedOut) {
          yield* FiberHandle.clear(connectionHandle);
          yield* startConnectionLoop(true);
        }
      }),

      loadHistory: (older = false) =>
        Effect.gen(function* () {
          const current = yield* getState;
          if (current.loadingHistory || !current.token || (older && !current.hasMoreHistory))
            return;
          const token = current.token;
          const before = older ? oldestHistoryPosition : undefined;
          yield* updateState((state) => ({ ...state, loadingHistory: true }));
          yield* sendRequest({
            type: 'load_history',
            request_id: crypto.randomUUID(),
            ...(before ? { before } : {}),
          }).pipe(
            Effect.catch((error) =>
              updateState((state) =>
                state.token === token
                  ? { ...state, error: formatUserError(error, '历史消息加载失败，点击重试') }
                  : state,
              ),
            ),
            Effect.ensuring(
              updateState((state) =>
                state.token === token ? { ...state, loadingHistory: false } : state,
              ),
            ),
          );
        }),

      searchMessages: (keyword, before) =>
        sendRequest({
          type: 'search_messages',
          request_id: crypto.randomUUID(),
          keyword,
          ...(before ? { before } : {}),
        }).pipe(
          Effect.flatMap((event) =>
            event.type === 'search_results'
              ? Effect.succeed(event)
              : Effect.fail(
                  new WsConnectionError({
                    reason: 'unexpected_event',
                    message: '搜索返回了错误的事件类型',
                  }),
                ),
          ),
        ),

      loadMessageContext: (messageID, direction) =>
        sendRequest({
          type: 'load_message_context',
          request_id: crypto.randomUUID(),
          message_id: messageID,
          direction,
        }).pipe(
          Effect.flatMap((event) =>
            event.type === 'message_context'
              ? Effect.succeed(event)
              : Effect.fail(
                  new WsConnectionError({
                    reason: 'unexpected_event',
                    message: '历史消息返回了错误的事件类型',
                  }),
                ),
          ),
        ),

      trimOlderMessages: (maxCount = latestMessageWindow) =>
        SubscriptionRef.modify(stateRef, (state) => {
          if (state.loadingHistory || state.messages.length <= maxCount) {
            return [0, state] as const;
          }
          const removed = state.messages.length - maxCount;
          const kept = state.messages.slice(removed);
          oldestHistoryPosition = kept[0]?.position;
          return [removed, { ...state, messages: kept, hasMoreHistory: true }] as const;
        }),

      send: (parts, clientMessageID) =>
        Effect.gen(function* () {
          const current = yield* getState;
          const text = parts.flatMap((part) => (part.type === 'text' ? [part.text] : [])).join('');
          const quotes = parts.flatMap((part) => (part.type === 'quote' ? [part.quote] : []));
          const images = parts.flatMap((part) => (part.type === 'image' ? [part.image] : []));
          const files = parts.flatMap((part) => (part.type === 'file' ? [part.file] : []));
          const trimmed = text.trim();
          if (
            (!trimmed && quotes.length === 0 && images.length === 0 && files.length === 0) ||
            Array.from(trimmed + quotes.map((quote) => quote.text).join('')).length > 64000 ||
            !current.token
          ) {
            return false;
          }
          if (!current.connected) {
            yield* reconnect;
            return yield* Effect.fail(
              new WsConnectionError({ reason: 'closed', message: '连接已断开，请稍后重试' }),
            );
          }
          yield* sendRequest({ type: 'send_message', client_message_id: clientMessageID, parts });
          return true;
        }),
    };
  });
