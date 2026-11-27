// 本文件组装前端 Effect Runtime 与响应式 Atom 桥接层：把纯 Effect 服务暴露给 React 组件按需订阅与触发。

import { Effect, Layer, SubscriptionRef } from 'effect';
import { AsyncResult, Atom } from 'effect/reactivity';
import { ApiClientLive } from './api.js';
import { initialChatState, makeChatSession } from './ChatController.js';
import type { ChatSession, ChatState } from './ChatController.js';
import { fetchMemoryFileContentEffect, fetchMemoryFilesEffect } from './memories.js';
import { uploadAttachmentsEffect } from './messageAttachments.js';
import type { MessagePart } from './messageAttachments.js';
import { getModelSettingsEffect, saveModelSettingsEffect } from './modelSettings.js';
import type { ModelSelection } from './modelSettings.js';
import {
  fetchOAuthAuthorizeContextEffect,
  fetchOAuthConnectionsEffect,
  revokeOAuthConnectionEffect,
  submitOAuthAuthorizeDecisionEffect,
} from './oauth.js';
import type { OAuthAuthorizeParams } from './oauth.js';
import { SessionStorage, SessionStorageLive } from './platform.js';

// AppLive 汇总前端运行所需的全部基础设施服务层，不依赖任何 React 概念。
export const AppLive = Layer.mergeAll(ApiClientLive, SessionStorageLive);

// appRuntime 为 React 侧 Atom 与动作函数提供统一的共享 Layer 实例。
export const appRuntime = Atom.runtime(AppLive);

// chatSessionAtom 随订阅组件挂载自动创建聊天会话，在所有订阅卸载后自动关闭 Scope 释放 WebSocket。
export const chatSessionAtom = appRuntime.atom(makeChatSession);

// sessionStateAtom 为同一份 SubscriptionRef 缓存稳定的可读 Atom 引用，支持组件细粒度选择器订阅。
const sessionStateAtom = Atom.family((ref: SubscriptionRef.SubscriptionRef<ChatState>) =>
  Atom.subscriptionRef(ref),
);

// chatStateAtom 暴露当前聊天会话的实时不可变快照，未就绪时回退到 initialChatState。
export const chatStateAtom: Atom.Atom<ChatState> = Atom.make((get) => {
  const result = get(chatSessionAtom);
  if (!AsyncResult.isSuccess(result)) return initialChatState;
  return get(sessionStateAtom(result.value.stateRef));
});

// chatBrowserLifecycleAtom 将可视视口、在线/离线、前后台切换与跨标签会话变化纳入当前页面 Scope。
export const chatBrowserLifecycleAtom = appRuntime.atom((get) =>
  Effect.gen(function* () {
    const session = yield* get.result(chatSessionAtom);
    const runSessionTask = (task: Effect.Effect<void>) => {
      Effect.runFork(task);
    };

    yield* Effect.acquireRelease(
      Effect.sync(() => {
        const resize = () => {
          const viewport = window.visualViewport;
          document.documentElement.style.setProperty(
            '--app-height',
            `${viewport?.height ?? innerHeight}px`,
          );
          document.documentElement.style.setProperty('--app-top', `${viewport?.offsetTop ?? 0}px`);
        };
        const resume = () => {
          if (navigator.onLine) runSessionTask(session.reconnect);
        };
        const suspend = () => {
          runSessionTask(session.suspend);
        };
        const visibility = () => {
          if (!document.hidden) resume();
        };
        const sessionChanged = (event: StorageEvent) => {
          if (event.key !== 'aether_session') return;
          const currentToken = Effect.runSync(session.getState).token;
          if (event.newValue !== currentToken) location.reload();
        };

        window.visualViewport?.addEventListener('resize', resize);
        window.visualViewport?.addEventListener('scroll', resize);
        window.addEventListener('resize', resize);
        window.addEventListener('online', resume);
        window.addEventListener('offline', suspend);
        window.addEventListener('pageshow', resume);
        window.addEventListener('pagehide', suspend);
        window.addEventListener('storage', sessionChanged);
        document.addEventListener('visibilitychange', visibility);
        resize();

        return () => {
          window.visualViewport?.removeEventListener('resize', resize);
          window.visualViewport?.removeEventListener('scroll', resize);
          window.removeEventListener('resize', resize);
          window.removeEventListener('online', resume);
          window.removeEventListener('offline', suspend);
          window.removeEventListener('pageshow', resume);
          window.removeEventListener('pagehide', suspend);
          window.removeEventListener('storage', sessionChanged);
          document.removeEventListener('visibilitychange', visibility);
        };
      }),
      (cleanup) => Effect.sync(cleanup),
    );
  }),
);

// withChatSession 在动作 Atom 中获取当前活跃的聊天会话并执行对应 Effect。
function withChatSession<A, E>(
  get: Atom.FnContext,
  run: (session: ChatSession) => Effect.Effect<A, E>,
): Effect.Effect<A, E> {
  return Effect.gen(function* () {
    const session = yield* get.result(chatSessionAtom);
    return yield* run(session);
  });
}

// ---- 聊天会话动作 Atom ----

export const requestCodeAtom = appRuntime.fn<{
  readonly email: string;
  readonly turnstileToken: string;
}>()((args, get) =>
  withChatSession(get, (session) => session.requestCode(args.email, args.turnstileToken)),
);

export const signInAtom = appRuntime.fn<{
  readonly email: string;
  readonly code: string;
}>()((args, get) => withChatSession(get, (session) => session.signIn(args.email, args.code)));

export const devSignInAtom = appRuntime.fn((_arg: void, get) =>
  withChatSession(get, (session) => session.devSignIn),
);

export const signOutAtom = appRuntime.fn((_arg: void, get) =>
  withChatSession(get, (session) => session.signOut),
);

export const loadHistoryAtom = appRuntime.fn<boolean>()((older, get) =>
  withChatSession(get, (session) => session.loadHistory(older)),
);

export const trimOlderMessagesAtom = appRuntime.fn<number | undefined>()((maxCount, get) =>
  withChatSession(get, (session) => session.trimOlderMessages(maxCount)),
);

export const sendMessageAtom = appRuntime.fn<{
  readonly parts: readonly MessagePart[];
  readonly clientMessageID: string;
}>()((args, get) =>
  withChatSession(get, (session) => session.send(args.parts, args.clientMessageID)),
);

export const uploadAttachmentsAtom = appRuntime.fn<{
  readonly token: string;
  readonly files: readonly File[];
  readonly existing: readonly MessagePart[];
  readonly workspaceEnabled: boolean;
}>()((args) =>
  uploadAttachmentsEffect(args.token, args.files, args.existing, args.workspaceEnabled),
);

export const searchMessagesAtom = appRuntime.fn<{
  readonly keyword: string;
  readonly before?: string;
}>()((args, get) =>
  withChatSession(get, (session) => session.searchMessages(args.keyword, args.before)),
);

export const loadMessageContextAtom = appRuntime.fn<{
  readonly messageID: string;
  readonly direction: 'around' | 'older' | 'newer';
}>()((args, get) =>
  withChatSession(get, (session) => session.loadMessageContext(args.messageID, args.direction)),
);

// ---- 模型设置 Atom ----

export const modelSettingsAtom = Atom.family((token: string) =>
  appRuntime.atom(getModelSettingsEffect(token)),
);

export const saveModelSettingsAtom = appRuntime.fn<{
  readonly token: string;
  readonly selection: ModelSelection;
}>()((args) => saveModelSettingsEffect(args.token, args.selection));

// ---- 只读记忆与 OAuth 2.1 Atom ----

export const browserSessionAtom = appRuntime.atom(
  Effect.gen(function* () {
    const storage = yield* SessionStorage;
    return yield* storage.readSession;
  }),
);

export const clearBrowserSessionAtom = appRuntime.fn(() =>
  Effect.gen(function* () {
    const storage = yield* SessionStorage;
    yield* storage.clearSession;
  }),
);

export const memoryFilesAtom = Atom.family((token: string) =>
  appRuntime.atom(fetchMemoryFilesEffect(token)),
);

export const memoryFileContentAtom = Atom.family((key: string) =>
  appRuntime.atom(() => {
    const separator = key.indexOf('\0');
    const token = key.slice(0, separator);
    const path = key.slice(separator + 1);
    return fetchMemoryFileContentEffect(token, path);
  }),
);

export const oauthAuthorizeContextAtom = Atom.family((search: string) =>
  appRuntime.atom(fetchOAuthAuthorizeContextEffect(search)),
);

export const submitOAuthDecisionAtom = appRuntime.fn<{
  readonly token: string;
  readonly params: OAuthAuthorizeParams;
  readonly approve: boolean;
}>()((args) => submitOAuthAuthorizeDecisionEffect(args.token, args.params, args.approve));

export const oauthConnectionsAtom = Atom.family((token: string) =>
  appRuntime.atom(fetchOAuthConnectionsEffect(token)),
);

export const revokeOAuthConnectionAtom = appRuntime.fn<{
  readonly token: string;
  readonly connectionID: string;
}>()((args) => revokeOAuthConnectionEffect(args.token, args.connectionID));
