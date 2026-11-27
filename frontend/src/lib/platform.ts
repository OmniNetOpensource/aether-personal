// 本文件是浏览器平台适配层：以 Effect Service 封装本地会话存储，并提供安全链接判断供 Markdown 渲染使用。

import { Context, Effect, Layer } from 'effect';
import { StorageError } from './errors.js';

// BrowserSession 描述浏览器端缓存的同源地址、登录令牌、账号邮箱与产品图标路径。
export interface BrowserSession {
  readonly baseURL: string;
  readonly token: string | null;
  readonly email: string;
  readonly iconURL: string;
}

// SessionStorage 抽象浏览器会话持久化操作，便于在 Effect Layer 中统一注入。
export class SessionStorage extends Context.Service<
  SessionStorage,
  {
    readonly readSession: Effect.Effect<BrowserSession>;
    readonly saveSession: (token: string, email: string) => Effect.Effect<void, StorageError>;
    readonly clearSession: Effect.Effect<void>;
  }
>()('@aether/SessionStorage') {}

// readBrowserSession 从 localStorage 读取缓存的登录态；存储不可用时仍允许打开登录页。
function readBrowserSession(): BrowserSession {
  let token: string | null = null;
  let email = '';
  try {
    token = localStorage.getItem('aether_session');
    email = localStorage.getItem('aether_email') ?? '';
  } catch {
    // 浏览器禁用本地存储时仍允许展示登录页。
  }
  return { baseURL: location.origin, token, email, iconURL: '/app-icon.png' };
}

// SessionStorageLive 是基于浏览器 localStorage 的生产实现层。
export const SessionStorageLive = Layer.succeed(
  SessionStorage,
  SessionStorage.of({
    readSession: Effect.sync(readBrowserSession),
    saveSession: (token, email) =>
      Effect.try({
        try: () => {
          localStorage.setItem('aether_email', email);
          localStorage.setItem('aether_session', token);
        },
        catch: () => new StorageError({ message: '无法保存登录信息，请检查浏览器存储设置' }),
      }),
    clearSession: Effect.sync(() => {
      try {
        localStorage.removeItem('aether_session');
      } catch {
        // 控制器同时会清空内存中的会话状态。
      }
    }),
  }),
);

// safeLink 只放行不带账号密码的 HTTP(S) 地址，防止渲染可执行协议链接。
export function safeLink(value: string): string | undefined {
  try {
    const url = new URL(value);
    if (['http:', 'https:'].includes(url.protocol) && !url.username && !url.password)
      return url.href;
  } catch {
    // Markdown 正文中可能出现不完整或非法地址。
  }
}
