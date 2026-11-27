// 本文件用 Effect Schema 与 ApiClient 封装认证和聊天 HTTP 接口：登录配置、验证码、会话校验、消息读写与历史搜索。

import { Effect, Schema } from 'effect';
import { HttpBody } from 'effect/http';
import { ApiClient } from './api.js';
import type { ApiError } from './api.js';
import { AuthError, DecodeError, HttpApiError, NetworkError } from './errors.js';
import type { MessagePart } from './messageAttachments.js';
import { ChatMessageRowSchema, ChatPageSchema, ChatSearchHitSchema } from './serverEvents.js';

// AuthConfigSchema 描述未登录页所需的开发登录开关与生产 Turnstile 站点密钥。
export const AuthConfigSchema = Schema.Struct({
  dev_login: Schema.Boolean,
  turnstile_site_key: Schema.optional(Schema.String),
});
export type AuthConfig = typeof AuthConfigSchema.Type;

// AuthTokenSchema 描述验证码登录或开发登录成功后的新会话凭证。
export const AuthTokenSchema = Schema.Struct({
  token: Schema.String,
  email: Schema.String,
});
export type AuthToken = typeof AuthTokenSchema.Type;

// AuthSessionSchema 描述当前会话对应的账号信息与只读桌面可用状态。
export const AuthSessionSchema = Schema.Struct({
  email: Schema.String,
  desktop_available: Schema.Boolean,
});
export type AuthSession = typeof AuthSessionSchema.Type;

// InitialMessagesSchema 描述首屏拉取的最新一页历史、实时推送游标与 Main 运行态。
export const InitialMessagesSchema = Schema.Struct({
  messages: Schema.NullOr(Schema.Array(ChatMessageRowSchema)),
  change_cursor: Schema.String,
  main_thinking: Schema.Boolean,
  main_failed: Schema.Boolean,
});
export type InitialMessages = typeof InitialMessagesSchema.Type;

// CreateMessageResponseSchema 描述 POST /messages 成功落库后立即返还的消息行与变更游标。
export const CreateMessageResponseSchema = Schema.Struct({
  message: ChatMessageRowSchema,
  change_cursor: Schema.String,
});
export type CreateMessageResponse = typeof CreateMessageResponseSchema.Type;

// ChatSearchResultsSchema 描述一页历史搜索命中结果及下一页游标。
export const ChatSearchResultsSchema = Schema.Struct({
  results: Schema.Array(ChatSearchHitSchema),
  next_before: Schema.optional(Schema.String),
});
export type ChatSearchResults = typeof ChatSearchResultsSchema.Type;

// MessageContextSchema 描述锚点消息前后的独立历史上下文窗口。
export const MessageContextSchema = Schema.Struct({
  messages: Schema.NullOr(Schema.Array(ChatMessageRowSchema)),
  has_more_before: Schema.Boolean,
  has_more_after: Schema.Boolean,
});
export type MessageContextResult = typeof MessageContextSchema.Type;

type PublicJsonError = HttpApiError | NetworkError | DecodeError;

// getAuthConfigEffect 获取当前环境的登录方式配置，无需登录态。
export function getAuthConfigEffect(): Effect.Effect<AuthConfig, PublicJsonError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    const result = yield* api.requestPublicJson('/auth/config', AuthConfigSchema, {
      fallbackError: '无法读取登录配置，请刷新重试',
    });
    if (!result.ok) {
      return yield* Effect.fail(
        new HttpApiError({ status: result.status, message: '无法读取登录配置，请刷新重试' }),
      );
    }
    return result.data;
  });
}

// requestLoginCodeEffect 校验安全令牌并发送 6 位邮箱验证码。
export function requestLoginCodeEffect(
  email: string,
  turnstileToken: string,
): Effect.Effect<void, HttpApiError | NetworkError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    yield* api.requestPublicVoid('/auth/request-code', {
      method: 'POST',
      body: HttpBody.jsonUnsafe({ email, turnstile_token: turnstileToken }),
      timeout: '30 seconds',
      fallbackError: '验证码发送失败，请稍后重试',
    });
  });
}

// verifyLoginCodeEffect 提交邮箱与 6 位验证码以换取新会话令牌。
export function verifyLoginCodeEffect(
  email: string,
  code: string,
): Effect.Effect<AuthToken, PublicJsonError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    const result = yield* api.requestPublicJson('/auth/verify-code', AuthTokenSchema, {
      method: 'POST',
      body: HttpBody.jsonUnsafe({ email, code }),
      fallbackError: '登录失败，请稍后重试',
    });
    if (!result.ok) {
      return yield* Effect.fail(
        new HttpApiError({ status: result.status, message: '验证码不正确或已过期' }),
      );
    }
    return result.data;
  });
}

// devLoginEffect 请求 dev 环境固定测试账号会话。
export function devLoginEffect(): Effect.Effect<AuthToken, PublicJsonError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    const result = yield* api.requestPublicJson('/auth/dev-login', AuthTokenSchema, {
      method: 'POST',
      fallbackError: '开发测试登录失败，请重试',
    });
    if (!result.ok) {
      return yield* Effect.fail(
        new HttpApiError({ status: result.status, message: '当前环境不开放开发测试登录' }),
      );
    }
    return result.data;
  });
}

// getAuthSessionEffect 验证当前令牌并返回邮箱与工作区桌面入口状态。
export function getAuthSessionEffect(
  token: string,
): Effect.Effect<AuthSession, ApiError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    return yield* api.requestJson('/auth/session', token, AuthSessionSchema, {
      headers: { Accept: 'application/json' },
      fallbackError: '会话检查失败，请稍后重试',
    });
  });
}

// logoutAuthSessionEffect 撤销当前设备的登录会话。
export function logoutAuthSessionEffect(
  token: string,
): Effect.Effect<void, AuthError | HttpApiError | NetworkError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    yield* api.requestVoid('/auth/logout', token, {
      method: 'POST',
      fallbackError: '退出登录失败，请稍后重试',
    });
  });
}

// fetchInitialMessagesEffect 拉取首屏历史、当前实时同步游标与 Main 状态。
export function fetchInitialMessagesEffect(
  token: string,
): Effect.Effect<InitialMessages, ApiError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    return yield* api.requestJson('/messages', token, InitialMessagesSchema, {
      headers: { Accept: 'application/json' },
      fallbackError: '历史消息加载失败，点击重试',
    });
  });
}

// fetchOlderMessagesEffect 按消息位置游标向前翻一页更早的可见历史。
export function fetchOlderMessagesEffect(
  token: string,
  before: string,
): Effect.Effect<typeof ChatPageSchema.Type, ApiError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    const query = new URLSearchParams({ before });
    return yield* api.requestJson(`/messages?${query.toString()}`, token, ChatPageSchema, {
      headers: { Accept: 'application/json' },
      fallbackError: '历史消息加载失败，点击重试',
    });
  });
}

// sendMessageEffect 通过 HTTP POST 提交新消息并直接拿到落库后的展示行，发送不依赖 WS 在线。
export function sendMessageEffect(
  token: string,
  clientMessageID: string,
  parts: readonly MessagePart[],
): Effect.Effect<CreateMessageResponse, ApiError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    return yield* api.requestJson('/messages', token, CreateMessageResponseSchema, {
      method: 'POST',
      headers: { Accept: 'application/json' },
      body: HttpBody.jsonUnsafe({ client_message_id: clientMessageID, parts }),
      fallbackError: '发送失败，请稍后重试',
    });
  });
}

// searchMessagesEffect 按关键词与可选分页游标搜索当前用户的可见历史聊天。
export function searchMessagesEffect(
  token: string,
  keyword: string,
  before?: string,
): Effect.Effect<ChatSearchResults, ApiError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    const query = new URLSearchParams({ q: keyword });
    if (before) query.set('before', before);
    return yield* api.requestJson(
      `/messages/search?${query.toString()}`,
      token,
      ChatSearchResultsSchema,
      {
        headers: { Accept: 'application/json' },
        fallbackError: '搜索失败，请重试',
      },
    );
  });
}

// fetchMessageContextEffect 读取指定锚点消息两侧或单向翻页的历史上下文窗口。
export function fetchMessageContextEffect(
  token: string,
  messageID: string,
  direction: 'around' | 'older' | 'newer',
): Effect.Effect<MessageContextResult, ApiError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    const query = new URLSearchParams({ direction });
    return yield* api.requestJson(
      `/messages/${encodeURIComponent(messageID)}/context?${query.toString()}`,
      token,
      MessageContextSchema,
      {
        headers: { Accept: 'application/json' },
        fallbackError: '历史消息加载失败，请重试',
      },
    );
  });
}
