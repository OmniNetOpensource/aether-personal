// 本文件集中定义后端 WebSocket 服务器事件的类型结构，与后端 events.go 一一对应。

import type { MessagePart } from './messageAttachments.js';

// ChatMessageRow 表示后端返回的一条用户或助手聊天消息行。
export interface ChatMessageRow {
  id: string;
  text: string;
  parts?: MessagePart[];
  sender: 'user' | 'main';
  created_at: string;
  position: string;
  client_message_id?: string | null;
}

// ChatPage 承载一页消息列表，后端空切片可能序列化为 null。
export interface ChatPage {
  messages: ChatMessageRow[] | null;
}

// RequestFailedCode 对应后端可重试请求返回的错误码。
export type RequestFailedCode =
  | 'invalid_email'
  | 'too_frequent'
  | 'invalid_code'
  | 'dev_login_unavailable'
  | 'unauthorized'
  | 'already_authenticated'
  | 'internal'
  | 'verification_required'
  | 'login_unavailable'
  | 'invalid_request'
  | 'not_found';

// ChatSearchHit 表示历史搜索列表中的单条命中摘要。
export interface ChatSearchHit {
  id: string;
  sender: 'user' | 'main';
  created_at: string;
  excerpt: string;
}

// ServerEvent 联合后端聊天 WebSocket 下发的全部下行事件类型。
export type ServerEvent =
  | {
      type: 'ready';
      change_cursor?: string;
      authenticated: boolean;
      dev_login: boolean;
      desktop_available?: boolean;
      turnstile_site_key?: string;
      email?: string;
      history?: ChatPage;
    }
  | {
      type: 'authenticated';
      change_cursor?: string;
      request_id: string;
      token: string;
      email: string;
      desktop_available?: boolean;
      history: ChatPage;
    }
  | { type: 'code_sent'; request_id: string }
  | (ChatPage & { type: 'history'; request_id: string })
  | {
      type: 'search_results';
      request_id: string;
      results: ChatSearchHit[];
      next_before?: string;
    }
  | (ChatPage & {
      type: 'message_context';
      request_id: string;
      has_more_before: boolean;
      has_more_after: boolean;
    })
  | (ChatPage & {
      type: 'messages';
      change_cursor?: string;
      main_thinking?: boolean;
      main_failed?: boolean;
    })
  | { type: 'delivered'; client_message_id: string }
  | { type: 'logged_out'; request_id: string }
  | { type: 'request_failed'; request_id: string; code: RequestFailedCode };
