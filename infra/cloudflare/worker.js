// 本文件是 Cloudflare Worker 入口：把聊天、模型设置、记忆查看、远程记忆 MCP 与 OAuth 2.1、公开分享读取、只读桌面流和图片文件请求安全转发到后端源站，其余路径交给静态资源托管。

const apiMethods = new Map([
  // 聊天认证与消息读写走标准 HTTP；/ws 仅用于已登录用户的实时推送。
  ['/auth/config', 'GET'],
  ['/auth/request-code', 'POST'],
  ['/auth/verify-code', 'POST'],
  ['/auth/dev-login', 'POST'],
  ['/auth/session', 'GET'],
  ['/auth/logout', 'POST'],
  ['/messages', 'GET, POST'],
  ['/messages/search', 'GET'],
  ['/shares', 'GET, POST'],
  ['/ws', 'GET'],
  ['/sandbox/desktop', 'GET'],
  ['/images', 'POST'],
  ['/files', 'POST'],
  ['/healthz', 'GET'],
  ['/model-settings', 'GET, PUT'],
  ['/memory-files', 'GET'],
  ['/memory-files/content', 'GET'],
  ['/.well-known/oauth-protected-resource', 'GET, OPTIONS'],
  ['/.well-known/oauth-protected-resource/mcp/memory', 'GET, OPTIONS'],
  ['/.well-known/oauth-authorization-server', 'GET, OPTIONS'],
  ['/mcp/memory', 'GET, POST, DELETE'],
  ['/oauth/register', 'POST'],
  ['/oauth/authorize/context', 'GET'],
  ['/oauth/authorize/decision', 'POST'],
  ['/oauth/token', 'POST'],
  ['/oauth/revoke', 'POST'],
  ['/oauth/connections', 'GET'],
]);

// resolveApiMethod 识别固定 API 路径与动态消息上下文、分享和授权连接路径对应的允许 HTTP 方法。
function resolveApiMethod(pathname) {
  const exact = apiMethods.get(pathname);
  if (exact) return exact;
  if (/^\/messages\/[^/]+\/context$/.test(pathname)) return 'GET';
  if (/^\/shares\/[^/]+$/.test(pathname)) return 'GET, DELETE';
  if (/^\/oauth\/connections\/[^/]+$/.test(pathname)) return 'DELETE';
  return undefined;
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const method = resolveApiMethod(url.pathname);
    if (!method) {
      // 公开分享阅读页 /s/<token>、只读记忆查看页 /memories 与 OAuth 授权确认页 /oauth/authorize 复用 SPA 根外壳，缺失的静态资源路径仍原样返回 404。
      if (
        (url.pathname === '/memories' ||
          url.pathname === '/oauth/authorize' ||
          /^\/s\/[^/]+$/.test(url.pathname)) &&
        (request.method === 'GET' || request.method === 'HEAD')
      ) {
        return env.ASSETS.fetch(new Request(new URL('/', url.origin), request));
      }
      if (/^\/(auth|admin|sandbox|shares|mcp|oauth|\.well-known)(\/|$)/.test(url.pathname)) {
        return new Response('Not found', { status: 404 });
      }
      return env.ASSETS.fetch(request);
    }
    if (!method.split(', ').includes(request.method)) {
      return new Response('Method not allowed', {
        status: 405,
        headers: { Allow: method, 'Cache-Control': 'no-store' },
      });
    }

    let origin;
    try {
      origin = new URL(env.ORIGIN_URL);
    } catch {
      return new Response('Origin is not configured', { status: 503 });
    }
    const local = ['localhost', '127.0.0.1', '[::1]'].includes(origin.hostname);
    if (
      (origin.protocol !== 'https:' && !(local && origin.protocol === 'http:')) ||
      origin.username || origin.password || origin.pathname !== '/' ||
      origin.search || origin.hash || !env.ORIGIN_AUTH_SECRET ||
      origin.host === url.host
    ) {
      return new Response('Origin is not configured', { status: 503 });
    }

    origin.pathname = url.pathname;
    origin.search = url.search;
    const forwarded = new Request(origin, request);
    forwarded.headers.set('X-Aether-Origin-Key', env.ORIGIN_AUTH_SECRET);
    // 只信任 Cloudflare 提供的客户端地址，覆盖浏览器提交的同名头。
    forwarded.headers.delete('X-Aether-Client-IP');
    const clientIP = request.headers.get('CF-Connecting-IP');
    if (clientIP) forwarded.headers.set('X-Aether-Client-IP', clientIP);
    forwarded.headers.set('Cache-Control', 'no-store');
    try {
      const response = await fetch(forwarded, { redirect: 'manual' });
      if (response.status === 101) return response;
      if (response.status >= 300 && response.status < 400) {
        return new Response('Unexpected origin redirect', {
          status: 502, headers: { 'Cache-Control': 'no-store' },
        });
      }
      const headers = new Headers(response.headers);
      headers.set('Cache-Control', 'no-store');
      headers.set('X-Content-Type-Options', 'nosniff');
      return new Response(response.body, { status: response.status, headers });
    } catch {
      return new Response('Backend unavailable', {
        status: 502, headers: { 'Cache-Control': 'no-store' },
      });
    }
  },
};
