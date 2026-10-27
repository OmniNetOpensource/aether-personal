const apiMethods = new Map([
  ['/auth/config', 'GET'],
  ['/auth/code', 'POST'],
  ['/auth/verify', 'POST'],
  ['/auth/logout', 'POST'],
  ['/messages', 'GET'],
  ['/sandbox', 'GET'],
  ['/sandbox/desktop', 'GET'],
  ['/ws', 'GET'],
  ['/healthz', 'GET'],
]);

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const method = apiMethods.get(url.pathname);
    if (!method) {
      if (/^\/(auth|admin|sandbox)(\/|$)/.test(url.pathname)) {
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
