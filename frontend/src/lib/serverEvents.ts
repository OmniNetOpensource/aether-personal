// 本文件用 Effect Schema 集中定义后端 WebSocket 服务器事件结构，与后端 events.go 一一对应。

import { Schema } from 'effect';
import { MessagePartSchema } from './messageAttachments.js';

// ChatMessageRowSchema 描述后端返回的一条用户或助手聊天消息行。
export const ChatMessageRowSchema = Schema.Struct({
  id: Schema.String,
  text: Schema.String,
  parts: Schema.optional(Schema.Array(MessagePartSchema)),
  sender: Schema.Literals(['user', 'main']),
  created_at: Schema.String,
  position: Schema.String,
  client_message_id: Schema.optional(Schema.NullOr(Schema.String)),
});
export type ChatMessageRow = typeof ChatMessageRowSchema.Type;

// ChatPageSchema 承载一页消息列表，后端空切片可能序列化为 null。
export const ChatPageSchema = Schema.Struct({
  messages: Schema.NullOr(Schema.Array(ChatMessageRowSchema)),
});
export type ChatPage = typeof ChatPageSchema.Type;

// RequestFailedCodeSchema 对应后端可重试请求返回的全部错误码。
export const RequestFailedCodeSchema = Schema.Literals([
  'invalid_email',
  'too_frequent',
  'invalid_code',
  'dev_login_unavailable',
  'unauthorized',
  'already_authenticated',
  'internal',
  'verification_required',
  'login_unavailable',
  'invalid_request',
  'not_found',
]);
export type RequestFailedCode = typeof RequestFailedCodeSchema.Type;

// ChatSearchHitSchema 表示历史搜索列表中的单条命中摘要。
export const ChatSearchHitSchema = Schema.Struct({
  id: Schema.String,
  sender: Schema.Literals(['user', 'main']),
  created_at: Schema.String,
  excerpt: Schema.String,
});
export type ChatSearchHit = typeof ChatSearchHitSchema.Type;

// ServerEventSchema 联合后端聊天 WebSocket 下发的全部下行事件，用于运行时强校验解码。
export const ServerEventSchema = Schema.Union([
  Schema.Struct({
    type: Schema.Literal('ready'),
    change_cursor: Schema.optional(Schema.String),
    authenticated: Schema.Boolean,
    dev_login: Schema.Boolean,
    desktop_available: Schema.optional(Schema.Boolean),
    turnstile_site_key: Schema.optional(Schema.String),
    email: Schema.optional(Schema.String),
    history: Schema.optional(ChatPageSchema),
  }),
  Schema.Struct({
    type: Schema.Literal('authenticated'),
    change_cursor: Schema.optional(Schema.String),
    request_id: Schema.String,
    token: Schema.String,
    email: Schema.String,
    desktop_available: Schema.optional(Schema.Boolean),
    history: ChatPageSchema,
  }),
  Schema.Struct({
    type: Schema.Literal('code_sent'),
    request_id: Schema.String,
  }),
  Schema.Struct({
    type: Schema.Literal('history'),
    request_id: Schema.String,
    messages: Schema.NullOr(Schema.Array(ChatMessageRowSchema)),
  }),
  Schema.Struct({
    type: Schema.Literal('search_results'),
    request_id: Schema.String,
    results: Schema.Array(ChatSearchHitSchema),
    next_before: Schema.optional(Schema.String),
  }),
  Schema.Struct({
    type: Schema.Literal('message_context'),
    request_id: Schema.String,
    messages: Schema.NullOr(Schema.Array(ChatMessageRowSchema)),
    has_more_before: Schema.Boolean,
    has_more_after: Schema.Boolean,
  }),
  Schema.Struct({
    type: Schema.Literal('messages'),
    messages: Schema.NullOr(Schema.Array(ChatMessageRowSchema)),
    change_cursor: Schema.optional(Schema.String),
    main_thinking: Schema.optional(Schema.Boolean),
    main_failed: Schema.optional(Schema.Boolean),
  }),
  Schema.Struct({
    type: Schema.Literal('delivered'),
    client_message_id: Schema.String,
  }),
  Schema.Struct({
    type: Schema.Literal('logged_out'),
    request_id: Schema.String,
  }),
  Schema.Struct({
    type: Schema.Literal('request_failed'),
    request_id: Schema.String,
    code: RequestFailedCodeSchema,
  }),
]);
export type ServerEvent = typeof ServerEventSchema.Type;

// ServerEventFromJsonSchema 直接从 WebSocket 文本帧 JSON 解码为类型安全的 ServerEvent。
export const ServerEventFromJsonSchema = Schema.fromJsonString(ServerEventSchema);
