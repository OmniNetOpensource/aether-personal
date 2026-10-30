// 本文件集中定义后端 WebSocket 服务器事件的校验结构：与后端 events.go 一一对应，业务代码只处理校验后的事件。

import { z } from 'zod';

// 后端通过聊天 WebSocket 发来的全部事件类型集中定义在本文件，与后端 events.go 一一对应。
// 原始帧只在 ServerEventRelay.ts 中解析，业务代码只处理这里校验后的事件。

export const chatMessageRowSchema = z.object({
  id: z.string().min(1),
  text: z.string(),
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
  read_up_to: z.string().nullable().optional(),
});

export const requestFailedCodeSchema = z.enum([
  'invalid_email',
  'too_frequent',
  'invalid_code',
  'dev_login_unavailable',
  'unauthorized',
  'already_authenticated',
  'internal',
]);

export const serverEventSchema = z.discriminatedUnion('type', [
  z.object({
    type: z.literal('ready'),
    authenticated: z.boolean(),
    dev_login: z.boolean(),
    email: z.string().optional(),
    history: chatPageSchema.optional(),
  }),
  z.object({
    type: z.literal('authenticated'),
    request_id: z.uuid(),
    token: z.string().min(1),
    email: z.string().min(1),
    history: chatPageSchema,
  }),
  z.object({ type: z.literal('code_sent'), request_id: z.uuid() }),
  chatPageSchema.extend({ type: z.literal('history'), request_id: z.uuid() }),
  chatPageSchema.extend({
    type: z.literal('messages'),
    main_thinking: z.boolean().optional(),
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
