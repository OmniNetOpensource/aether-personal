// 本文件是 Cloudflare Worker 入口：把聊天、模型设置、公开分享读取、只读桌面流和图片文件请求安全转发到后端源站，其余路径交给静态资源托管。

const apiMethods = new Map([
  // 聊天登录、历史与收发全部走 /ws；旧 /auth、/messages 路径由下方保留路径检查返回 404。
  ['/ws', 'GET'],
  ['/sandbox/desktop', 'GET'],
  ['/images', 'POST'],
  ['/files', 'POST'],
  ['/healthz', 'GET'],
  ['/model-settings', 'GET, PUT'],
]);

// resolveApiMethod 识别固定 API 路径与动态公开分享读取路径对应的允许 HTTP 方法。
function resolveApiMethod(pathname) {
  const exact = apiMethods.get(pathname);
  if (exact) return exact;
  if (/^\/shares\/[^/]+$/.test(pathname)) return 'GET';
  return undefined;
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const method = resolveApiMethod(url.pathname);
    if (!method) {
      if (/^\/(auth|admin|sandbox|shares)(\/|$)/.test(url.pathname)) {
        return new Response('Not found', { status: 404 });
      }
      // 公开分享阅读页 /s/<token> 复用 SPA 根外壳，缺失的脚本与静态资源路径仍原样返回 404。
      if (/^\/s\/[^/]+$/.test(url.pathname) && (request.method === 'GET' || request.method === 'HEAD')) {
        return env.ASSETS.fetch(new Request(new URL('/', url.origin), request));
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
