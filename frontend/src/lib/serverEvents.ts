// 本文件用 Effect Schema 集中定义聊天消息共享结构与 WebSocket 实时推送事件，与后端 events.go 一一对应。

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

// ChatSearchHitSchema 表示历史搜索列表中的单条命中摘要。
export const ChatSearchHitSchema = Schema.Struct({
  id: Schema.String,
  sender: Schema.Literals(['user', 'main']),
  created_at: Schema.String,
  excerpt: Schema.String,
});
export type ChatSearchHit = typeof ChatSearchHitSchema.Type;

// ServerEventSchema 联合后端聊天 WebSocket 下发的全部实时推送事件，用于运行时强校验解码。
export const ServerEventSchema = Schema.Union([
  Schema.Struct({
    type: Schema.Literal('ready'),
    change_cursor: Schema.optional(Schema.String),
  }),
  Schema.Struct({
    type: Schema.Literal('messages'),
    messages: Schema.NullOr(Schema.Array(ChatMessageRowSchema)),
    change_cursor: Schema.optional(Schema.String),
    main_thinking: Schema.optional(Schema.Boolean),
    main_failed: Schema.optional(Schema.Boolean),
  }),
]);
export type ServerEvent = typeof ServerEventSchema.Type;

// ServerEventFromJsonSchema 直接从 WebSocket 文本帧 JSON 解码为类型安全的 ServerEvent。
export const ServerEventFromJsonSchema = Schema.fromJsonString(ServerEventSchema);
