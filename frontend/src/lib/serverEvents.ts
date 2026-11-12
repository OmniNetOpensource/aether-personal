// 本文件集中定义后端 WebSocket 服务器事件的校验结构：与后端 events.go 一一对应，业务代码只处理校验后的事件。

import { z } from 'zod';
import { messagePartSchema } from './messageAttachments.js';

// 后端通过聊天 WebSocket 发来的全部事件类型集中定义在本文件，与后端 events.go 一一对应。
// 原始帧只在 ServerEventRelay.ts 中解析，业务代码只处理这里校验后的事件。

export const chatMessageRowSchema = z.object({
  id: z.string().min(1),
  text: z.string(),
  parts: z.array(messagePartSchema).optional(),
  sender: z.enum(['user', 'main']),
  created_at: z.iso.datetime({ offset: true }),
  position: z.uuid(),
  client_message_id: z.uuid().nullable().optional(),
});

export const chatPageSchema = z.object({
  messages: z
    .array(chatMessageRowSchema)
    .nullable()
    .transform((value) => value ?? []),
});

export const requestFailedCodeSchema = z.enum([
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
  'invalid_share',
  'share_not_found',
]);

// 搜索只携带列表片段；上下文窗口复用完整聊天消息结构。
export const chatSearchHitSchema = z.object({
  id: z.uuid(),
  sender: z.enum(['user', 'main']),
  created_at: z.iso.datetime({ offset: true }),
  excerpt: z.string(),
});

// 分享记录摘要用于创建回执与「已分享列表」展示。
export const messageShareSummarySchema = z.object({
  id: z.uuid(),
  token: z.string().min(22),
  created_at: z.iso.datetime({ offset: true }),
  message_count: z.number().int().nonnegative(),
  preview: z.string(),
});

// 公开分享载荷供免登录访客在 /s/<token> 页面校验后端 HTTP 响应。
export const publicMessageShareSchema = z.object({
  token: z.string().min(22),
  created_at: z.iso.datetime({ offset: true }),
  messages: z.array(chatMessageRowSchema),
});

// 任务快照随连接就绪、登录和消息事件整表同步，前端只读展示。
export const taskStatusSchema = z.enum([
  'in_progress',
  'queued',
  'waiting',
  'completed',
  'cancelled',
]);

export const chatTaskSchema = z.object({
  id: z.uuid(),
  title: z.string(),
  description: z.string(),
  status: taskStatusSchema,
  created_at: z.iso.datetime({ offset: true }),
  updated_at: z.iso.datetime({ offset: true }),
});

export const serverEventSchema = z.discriminatedUnion('type', [
  z.object({
    type: z.literal('ready'),
    change_cursor: z.string().regex(/^\d+$/).optional(),
    authenticated: z.boolean(),
    dev_login: z.boolean(),
    desktop_available: z.boolean().optional(),
    turnstile_site_key: z.string().optional(),
    email: z.string().optional(),
    history: chatPageSchema.optional(),
    tasks: z.array(chatTaskSchema).optional(),
  }),
  z.object({
    type: z.literal('authenticated'),
    change_cursor: z.string().regex(/^\d+$/).optional(),
    request_id: z.uuid(),
    token: z.string().min(1),
    email: z.string().min(1),
    desktop_available: z.boolean().optional(),
    history: chatPageSchema,
    tasks: z.array(chatTaskSchema).optional(),
  }),
  z.object({ type: z.literal('code_sent'), request_id: z.uuid() }),
  chatPageSchema.extend({ type: z.literal('history'), request_id: z.uuid() }),
  z.object({
    type: z.literal('search_results'),
    request_id: z.uuid(),
    results: z.array(chatSearchHitSchema),
    next_before: z.uuid().optional(),
  }),
  chatPageSchema.extend({
    type: z.literal('message_context'),
    request_id: z.uuid(),
    has_more_before: z.boolean(),
    has_more_after: z.boolean(),
  }),
  z.object({
    type: z.literal('share_created'),
    request_id: z.uuid(),
    share: messageShareSummarySchema,
  }),
  z.object({
    type: z.literal('share_list'),
    request_id: z.uuid(),
    shares: z.array(messageShareSummarySchema),
  }),
  z.object({
    type: z.literal('share_deleted'),
    request_id: z.uuid(),
    share_id: z.uuid(),
  }),
  chatPageSchema.extend({
    type: z.literal('messages'),
    change_cursor: z.string().regex(/^\d+$/).optional(),
    main_thinking: z.boolean().optional(),
    main_failed: z.boolean().optional(),
    tasks: z.array(chatTaskSchema).optional(),
  }),
  z.object({ type: z.literal('delivered'), client_message_id: z.uuid() }),
  z.object({ type: z.literal('logged_out'), request_id: z.uuid() }),
  z.object({
    type: z.literal('request_failed'),
    request_id: z.uuid(),
    code: requestFailedCodeSchema,
  }),
]);

export type ServerEvent = z.infer<typeof serverEventSchema>;
export type ChatPage = z.infer<typeof chatPageSchema>;
export type RequestFailedCode = z.infer<typeof requestFailedCodeSchema>;
export type ChatSearchHit = z.infer<typeof chatSearchHitSchema>;
export type MessageShareSummary = z.infer<typeof messageShareSummarySchema>;
export type PublicMessageShare = z.infer<typeof publicMessageShareSchema>;
export type TaskStatus = z.infer<typeof taskStatusSchema>;
export type ChatTask = z.infer<typeof chatTaskSchema>;
