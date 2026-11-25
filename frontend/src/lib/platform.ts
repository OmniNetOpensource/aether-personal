// 本文件是浏览器平台适配：读写与清除本地会话，并提供安全链接判断供 Markdown 渲染使用。

// readSession 从浏览器存储读取登录令牌与邮箱；存储不可用时仍允许打开登录界面。
export function readSession() {
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

// saveSession 将登录令牌与邮箱写入本地存储，浏览器拒绝写入时提示用户检查设置。
export function saveSession(token: string, email: string) {
  try {
    localStorage.setItem('aether_email', email);
    localStorage.setItem('aether_session', token);
  } catch {
    throw new Error('无法保存登录信息，请检查浏览器存储设置');
  }
}

// clearSession 清除本地登录令牌，浏览器存储禁用时静默跳过。
export function clearSession() {
  try {
    localStorage.removeItem('aether_session');
  } catch {
    // 控制器同时会清空内存中的会话状态。
  }
}

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
