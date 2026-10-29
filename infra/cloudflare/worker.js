// 本文件是 Cloudflare Worker 入口：把聊天 WebSocket 与健康检查请求安全转发到后端源站，其余路径交给静态资源托管。

const apiMethods = new Map([
  // 聊天登录、历史与收发全部走 /ws；旧 /auth、/messages 路径由下方保留路径检查返回 404。
  ['/ws', 'GET'],
  ['/healthz', 'GET'],
]);

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const method = apiMethods.get(url.pathname);
    if (!method) {
      if (/^\/(auth|admin)(\/|$)/.test(url.pathname)) {
        return new Response('Not found', { status: 404 });
      }
      return env.ASSETS.fetch(request);
    }
    if (request.method !== method) {
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
