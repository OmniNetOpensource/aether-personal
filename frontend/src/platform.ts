// 本文件是浏览器平台适配：读写与清除本地会话（兼容旧版本存储键），并提供安全链接判断供 Markdown 渲染使用。

import { z } from 'zod';

export async function readSession() {
  let token: string | null = null;
  let email = '';
  try {
    const readString = (key: string) => {
      const stored = localStorage.getItem(key);
      if (stored === null) return null;
      return stored.startsWith('"') ? z.string().parse(JSON.parse(stored)) : stored;
    };
    // Flutter used both JSON-encoded values and flutter.* keys before the Lynx migration.
    token = readString('aether_session') ?? readString('flutter.aether_session');
    email = readString('aether_email') ?? readString('flutter.aether_email') ?? '';
    if (token) await saveSession(token, email);
  } catch {
    // A disabled browser store must still allow the login screen to open.
  }
  return { baseURL: location.origin, token, email, iconURL: '/app-icon.png' };
}

export async function saveSession(token: string, email: string) {
  try {
    localStorage.setItem('aether_email', email);
    localStorage.setItem('aether_session', token);
  } catch {
    throw new Error('无法保存登录信息，请检查浏览器存储设置');
  }
}

export function clearSession() {
  try {
    localStorage.removeItem('aether_session');
    localStorage.removeItem('flutter.aether_session');
  } catch {
    // The controller also clears its in-memory session.
  }
}

export function safeLink(value: string): string | undefined {
  try {
    const url = new URL(value);
    if (['http:', 'https:'].includes(url.protocol) && !url.username && !url.password)
      return url.href;
  } catch {
    // Markdown may contain incomplete or unsafe links.
  }
}
