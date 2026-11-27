// 本文件是纯 Effect 聊天会话控制器：登录与消息读写走 HTTP，已登录连接仅通过 WebSocket 接收实时推送与运行状态。

import { Effect, FiberHandle, Queue, Scope, SubscriptionRef } from 'effect';
import { ApiClient } from './api.js';
import type { ApiError } from './api.js';
import {
  devLoginEffect,
  fetchInitialMessagesEffect,
  fetchMessageContextEffect,
  fetchOlderMessagesEffect,
  getAuthConfigEffect,
  getAuthSessionEffect,
  logoutAuthSessionEffect,
  requestLoginCodeEffect,
  searchMessagesEffect,
  sendMessageEffect,
  verifyLoginCodeEffect,
} from './chatApi.js';
import type { AuthToken, ChatSearchResults, MessageContextResult } from './chatApi.js';
import { AuthError, StorageError, WsConnectionError, formatUserError } from './errors.js';
import type { MessagePart } from './messageAttachments.js';
import { SessionStorage } from './platform.js';
import { openServerEventConnection } from './ServerEventRelay.js';
import type { ServerEventConnection } from './ServerEventRelay.js';
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

// 断线自动重连退避阶梯（秒），推送连接完成 ready 握手后重置为首项。
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

export type ChatCommandError = ApiError | WsConnectionError | StorageError;

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
  ) => Effect.Effect<ChatSearchResults, ChatCommandError>;
  readonly loadMessageContext: (
    messageID: string,
    direction: 'around' | 'older' | 'newer',
  ) => Effect.Effect<MessageContextResult, ChatCommandError>;
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

// makeChatSession 在调用方 Scope 内创建独立聊天会话；Scope 关闭时自动中断后台推送连接与初始化协程。
export const makeChatSession: Effect.Effect<
  ChatSession,
  never,
  Scope.Scope | SessionStorage | ApiClient
> = Effect.gen(function* () {
  const storage = yield* SessionStorage;
  const api = yield* ApiClient;
  const provideApi = <A, E>(effect: Effect.Effect<A, E, ApiClient>) =>
    Effect.provideService(effect, ApiClient, api);

  const saved = yield* storage.readSession;
  const stateRef = yield* SubscriptionRef.make<ChatState>({
    ...initialChatState,
    token: saved.token,
    email: saved.email,
    iconURL: saved.iconURL,
  });

  const baseURL = saved.baseURL;
  const bootstrapHandle = yield* FiberHandle.make<void, never>();
  const connectionHandle = yield* FiberHandle.make<void, never>();
  const readyTimeoutHandle = yield* FiberHandle.make<void, never>();

  let activeConnection: ServerEventConnection | null = null;
  let reconnectAttempt = 0;
  let historyLoaded = false;
  let authConfigLoaded = false;
  let suspended = false;
  let devLoginAttempted = false;
  // 重连按变更序号续推，历史分页只按已读取的历史页推进；迟到的旧回复不应跳过中间一页。
  let lastChangeCursor: string | undefined;
  let oldestHistoryPosition: string | undefined;

  const getState = SubscriptionRef.get(stateRef);
  const updateState = (f: (state: ChatState) => ChatState) => SubscriptionRef.update(stateRef, f);

  const clearLocalSession = Effect.gen(function* () {
    historyLoaded = false;
    lastChangeCursor = undefined;
    oldestHistoryPosition = undefined;
    yield* FiberHandle.clear(connectionHandle);
    yield* FiberHandle.clear(readyTimeoutHandle);
    yield* storage.clearSession;
    yield* updateState((state) => ({
      ...state,
      token: null,
      connected: false,
      desktopAvailable: false,
      mainThinking: false,
      mainFailed: false,
      loadingHistory: false,
      hasMoreHistory: true,
      messages: [],
    }));
  });

  // applyHistory 记住连续翻过的最早位置；首屏超过窗口条数时只保留最新窗口并允许继续上翻。
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

  // handleServerEvent 仅消费后端实时推送的 ready 与 messages 事件，驱动连接态、思考态和增量消息合并。
  const handleServerEvent = (event: ServerEvent): Effect.Effect<void> =>
    Effect.gen(function* () {
      switch (event.type) {
        case 'ready': {
          yield* FiberHandle.clear(readyTimeoutHandle);
          reconnectAttempt = 0;
          if (!lastChangeCursor && event.change_cursor) {
            lastChangeCursor = event.change_cursor;
          }
          yield* updateState((state) =>
            state.connected && state.error === null
              ? state
              : { ...state, connected: true, error: null },
          );
          return;
        }
        case 'messages': {
          if (event.change_cursor) {
            lastChangeCursor = event.change_cursor;
          }
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
      }
    });

  // loadAuthConfig 拉取未登录页所需的登录配置；仅在未登录且尚未获取时使用，不建立 WebSocket。
  const loadAuthConfig = (allowAutoDevLogin: boolean): Effect.Effect<void> =>
    Effect.gen(function* () {
      const result = yield* provideApi(getAuthConfigEffect()).pipe(Effect.result);
      if (result._tag === 'Failure') {
        yield* updateState((state) => ({
          ...state,
          error: formatUserError(result.failure, '无法读取登录配置，请刷新重试'),
          initializing: false,
        }));
        return;
      }
      authConfigLoaded = true;
      const config = result.success;
      const shouldDevLogin = allowAutoDevLogin && config.dev_login && !devLoginAttempted;
      if (shouldDevLogin) devLoginAttempted = true;
      yield* updateState((state) => ({
        ...state,
        devLoginAvailable: config.dev_login,
        turnstileSiteKey: config.turnstile_site_key ?? '',
        initializing: shouldDevLogin ? state.initializing : false,
      }));
      if (shouldDevLogin) {
        const loginResult = yield* completeLogin(devLoginEffect()).pipe(Effect.result);
        if (loginResult._tag === 'Failure') {
          yield* updateState((state) => ({
            ...state,
            error: formatUserError(loginResult.failure, '开发测试登录失败，请重试'),
            initializing: false,
          }));
        }
      }
    });

  // handleAuthExpired 统一处理 HTTP 401 或 WS unauthorized：清理本地令牌并切换回登录态。
  const handleAuthExpired = (message: string | null) =>
    Effect.gen(function* () {
      yield* clearLocalSession;
      yield* updateState((state) => ({
        ...state,
        error: message,
        initializing: false,
      }));
      yield* loadAuthConfig(true);
    });

  // runConnectionLoop 仅在已登录且联网时维持单向推送 WebSocket，并在断线后按退避阶梯自动重连。
  const runConnectionLoop: Effect.Effect<void> = Effect.gen(function* () {
    while (!suspended && navigator.onLine) {
      const current = yield* getState;
      const token = current.token;
      if (!token) return;

      const closeReason = yield* Effect.scoped(
        Effect.gen(function* () {
          const connection = yield* openServerEventConnection(
            baseURL,
            token,
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
              }),
            ),
          );
        }),
      );

      if (closeReason === 'unauthorized') {
        yield* handleAuthExpired('登录已过期，请重新登录');
        return;
      }
      if (suspended || !navigator.onLine) return;

      const latest = yield* getState;
      if (!latest.token) return;

      const delaySeconds =
        reconnectDelaysSeconds[Math.min(reconnectAttempt++, reconnectDelaysSeconds.length - 1)] ??
        15;
      yield* Effect.sleep(`${delaySeconds} seconds`);
    }
  });

  const startConnectionLoop = (forceRestart: boolean) =>
    Effect.gen(function* () {
      const current = yield* getState;
      if (suspended || !navigator.onLine || !current.token) return;
      if (!forceRestart && activeConnection) return;
      yield* FiberHandle.run(connectionHandle, runConnectionLoop, {
        onlyIfMissing: !forceRestart,
      });
    });

  // bootstrapAuthenticated 并行拉取会话信息与首屏历史，完成后立刻渲染聊天界面并启动 WS 实时推送。
  const bootstrapAuthenticated = (token: string, fallbackEmail?: string): Effect.Effect<void> =>
    Effect.gen(function* () {
      const result = yield* Effect.all(
        [provideApi(getAuthSessionEffect(token)), provideApi(fetchInitialMessagesEffect(token))],
        { concurrency: 'unbounded' },
      ).pipe(Effect.result);

      if (result._tag === 'Failure') {
        if (result.failure instanceof AuthError) {
          yield* handleAuthExpired(null);
          return;
        }
        yield* updateState((state) => ({
          ...state,
          token,
          email: fallbackEmail ?? state.email,
          error: formatUserError(result.failure, '历史消息加载失败，点击重试'),
          initializing: false,
        }));
        return;
      }

      const [session, initial] = result.success;
      lastChangeCursor = initial.change_cursor;
      yield* updateState((state) =>
        applyHistory(
          {
            ...state,
            token,
            email: session.email,
            desktopAvailable: session.desktop_available,
            mainThinking: initial.main_thinking,
            mainFailed: initial.main_failed,
            error: null,
            initializing: false,
          },
          initial,
        ),
      );
      yield* startConnectionLoop(true);
    });

  // completeLogin 统一处理验证码登录与开发登录：先持久化令牌，再拉齐首屏历史并建立推送通道。
  const completeLogin = (
    loginEffect: Effect.Effect<AuthToken, ApiError, ApiClient>,
  ): Effect.Effect<void, ChatCommandError> =>
    Effect.gen(function* () {
      const auth = yield* provideApi(loginEffect);
      yield* storage.saveSession(auth.token, auth.email);
      historyLoaded = false;
      lastChangeCursor = undefined;
      oldestHistoryPosition = undefined;
      yield* bootstrapAuthenticated(auth.token, auth.email);
    });

  const reconnect = Effect.gen(function* () {
    suspended = false;
    if (!navigator.onLine) return;
    const current = yield* getState;
    if (current.token) {
      if (!historyLoaded) {
        yield* FiberHandle.run(bootstrapHandle, bootstrapAuthenticated(current.token), {
          onlyIfMissing: true,
        });
      } else {
        yield* startConnectionLoop(false);
      }
      return;
    }
    if (!authConfigLoaded) {
      yield* FiberHandle.run(bootstrapHandle, loadAuthConfig(true), { onlyIfMissing: true });
    }
  });

  const suspend = Effect.gen(function* () {
    suspended = true;
    yield* FiberHandle.clear(connectionHandle);
    yield* FiberHandle.clear(readyTimeoutHandle);
  });

  // 初始化：已登录时通过 HTTP 并行拉取会话与首屏后再建 WS；未登录只拉取 HTTP 登录配置。
  if (navigator.onLine) {
    if (saved.token) {
      yield* FiberHandle.run(bootstrapHandle, bootstrapAuthenticated(saved.token));
    } else {
      yield* FiberHandle.run(bootstrapHandle, loadAuthConfig(true));
    }
  } else {
    yield* updateState((state) => ({
      ...state,
      error: state.token ? state.error : '当前处于离线状态，恢复网络后即可登录',
      initializing: false,
    }));
  }

  // Scope 释放时收尾取消初始化与推送连接。
  yield* Effect.addFinalizer(() =>
    Effect.gen(function* () {
      suspended = true;
      yield* FiberHandle.clear(bootstrapHandle);
      yield* FiberHandle.clear(connectionHandle);
      yield* FiberHandle.clear(readyTimeoutHandle);
    }),
  );

  return {
    stateRef,
    getState,
    reconnect,
    suspend,

    requestCode: (email, turnstileToken) =>
      provideApi(requestLoginCodeEffect(email.trim(), turnstileToken)),

    signIn: (email, code) => completeLogin(verifyLoginCodeEffect(email.trim(), code.trim())),

    devSignIn: completeLogin(devLoginEffect()),

    signOut: Effect.gen(function* () {
      const current = yield* getState;
      yield* FiberHandle.clear(bootstrapHandle);
      yield* FiberHandle.clear(connectionHandle);
      yield* FiberHandle.clear(readyTimeoutHandle);
      if (current.token) {
        yield* provideApi(logoutAuthSessionEffect(current.token)).pipe(Effect.ignore);
      }
      yield* clearLocalSession;
      if (!authConfigLoaded && navigator.onLine) {
        yield* loadAuthConfig(false);
      }
    }),

    loadHistory: (older = false) =>
      Effect.gen(function* () {
        const current = yield* getState;
        if (current.loadingHistory || !current.token || (older && !current.hasMoreHistory)) return;
        const token = current.token;
        yield* updateState((state) => ({ ...state, loadingHistory: true }));

        const task = Effect.gen(function* () {
          if (!older && !historyLoaded) {
            yield* bootstrapAuthenticated(token);
            return;
          }
          if (older && oldestHistoryPosition) {
            const page = yield* provideApi(fetchOlderMessagesEffect(token, oldestHistoryPosition));
            yield* updateState((state) =>
              state.token === token ? applyHistory({ ...state, error: null }, page) : state,
            );
            return;
          }
          const initial = yield* provideApi(fetchInitialMessagesEffect(token));
          lastChangeCursor = initial.change_cursor;
          yield* updateState((state) =>
            state.token === token
              ? applyHistory(
                  {
                    ...state,
                    mainThinking: initial.main_thinking,
                    mainFailed: initial.main_failed,
                    error: null,
                  },
                  initial,
                )
              : state,
          );
        });

        yield* task.pipe(
          Effect.catch((error) =>
            Effect.gen(function* () {
              if (error instanceof AuthError) {
                yield* handleAuthExpired('登录已过期，请重新登录');
                return;
              }
              yield* updateState((state) =>
                state.token === token
                  ? { ...state, error: formatUserError(error, '历史消息加载失败，点击重试') }
                  : state,
              );
            }),
          ),
          Effect.ensuring(
            updateState((state) =>
              state.token === token ? { ...state, loadingHistory: false } : state,
            ),
          ),
        );
      }),

    searchMessages: (keyword, before) =>
      Effect.gen(function* () {
        const current = yield* getState;
        if (!current.token) {
          return yield* Effect.fail(new AuthError({ message: '登录已过期，请重新登录' }));
        }
        return yield* provideApi(searchMessagesEffect(current.token, keyword, before)).pipe(
          Effect.tapError((error) =>
            error instanceof AuthError ? handleAuthExpired('登录已过期，请重新登录') : Effect.void,
          ),
        );
      }),

    loadMessageContext: (messageID, direction) =>
      Effect.gen(function* () {
        const current = yield* getState;
        if (!current.token) {
          return yield* Effect.fail(new AuthError({ message: '登录已过期，请重新登录' }));
        }
        return yield* provideApi(
          fetchMessageContextEffect(current.token, messageID, direction),
        ).pipe(
          Effect.tapError((error) =>
            error instanceof AuthError ? handleAuthExpired('登录已过期，请重新登录') : Effect.void,
          ),
        );
      }),

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
        const token = current.token;
        const created = yield* provideApi(sendMessageEffect(token, clientMessageID, parts)).pipe(
          Effect.tapError((error) =>
            error instanceof AuthError ? handleAuthExpired('登录已过期，请重新登录') : Effect.void,
          ),
        );
        // 仅合并刚落库的消息，不把 lastChangeCursor 直接跳到该值，避免跳过此前尚未推到的助手消息。
        yield* updateState((state) => {
          if (state.token !== token) return state;
          const merged = mergeRows(state.messages, { messages: [created.message] });
          return { ...state, messages: merged.messages, error: null };
        });
        if (!activeConnection && navigator.onLine && !suspended) {
          yield* startConnectionLoop(false);
        }
        return true;
      }),
  };
});
