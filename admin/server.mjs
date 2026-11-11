// 本文件是管理看板的独立服务入口：托管 TanStack Start 构建出的静态页面，并把管理 API 请求按白名单代理到后端。

import http from 'node:http';
import https from 'node:https';
import { readFile, stat } from 'node:fs/promises';
import path from 'node:path';

const host = process.env.HOST || '127.0.0.1';
const port = Number(process.env.PORT || 5174);
const upstream = new URL(process.env.API_UPSTREAM || 'http://127.0.0.1:8080');
if (!['http:', 'https:'].includes(upstream.protocol) || upstream.username || upstream.password || upstream.pathname !== '/' || upstream.search || upstream.hash) {
  throw new Error('API_UPSTREAM must be an HTTP(S) origin without credentials, path, query or fragment');
}
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('PORT must be between 1 and 65535');
// 静态产物来自 `npm run build` 的 build/web；带哈希的资源文件名由 Rsbuild 生成，这里按扩展名给类型。
const staticRoot = new URL('./build/web/', import.meta.url);
const contentTypes = new Map([
  ['.html', 'text/html; charset=utf-8'],
  ['.js', 'text/javascript; charset=utf-8'],
  ['.css', 'text/css; charset=utf-8'],
  ['.png', 'image/png'],
  ['.jpg', 'image/jpeg'],
  ['.svg', 'image/svg+xml'],
  ['.ico', 'image/x-icon'],
  ['.json', 'application/json; charset=utf-8'],
  ['.webmanifest', 'application/manifest+json; charset=utf-8'],
  ['.txt', 'text/plain; charset=utf-8'],
  ['.woff2', 'font/woff2'],
]);
const apiMethods = new Map([
  ['/admin/login', 'POST'], ['/admin/logout', 'POST'],
  ['/admin/me', 'GET'], ['/admin/snapshot', 'GET'], ['/admin/timeline', 'GET'],
 ['/admin/models', 'GET'], ['/admin/model-settings', 'GET PUT'],
 ['/admin/services', 'GET'], ['/admin/quotas', 'GET'],
].map(([path, methods]) => [path, new Set(methods.split(' '))]));
function fail(response, status, message) {
  if (response.headersSent) return response.destroy();
  response.writeHead(status, { 'Content-Type': 'text/plain; charset=utf-8' });
  response.end(message);
}
async function proxy(request, response, url) {
  const path = url.pathname.slice(4);
  const callDetail = /^\/admin\/(calls|timeline\/messages)\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(path);
  const serviceConfig = /^\/admin\/services\/(openrouter|ikun_gemini|ikun_claude|resend|serper|parallel|everyinfra|aliyun_oss|aliyun_fc)$/.test(path);
  const methods = apiMethods.get(path) || (callDetail ? new Set(['GET']) : serviceConfig ? new Set(['PUT']) : null);
  // 请求详情、时间线与消息全文可能超过缓冲上限，直接流式转发。
  const streamed = callDetail || path === '/admin/timeline';
  if (!methods) return fail(response, 404, 'Not found');
  if (!methods.has(request.method)) return fail(response, 405, 'Method not allowed');
  const chunks = [];
  let bytes = 0;
  for await (const chunk of request) {
    bytes += chunk.length;
    if (bytes > 16 * 1024) return fail(response, 413, 'Request too large');
    chunks.push(chunk);
  }
  const headers = { Accept: 'application/json' };
  if (request.headers.authorization) headers.Authorization = request.headers.authorization;
  if (request.headers['content-type']) headers['Content-Type'] = request.headers['content-type'];
  const target = new URL(path + url.search, upstream);
  const transport = target.protocol === 'https:' ? https : http;
  const forwarded = transport.request(target, { method: request.method, headers }, (received) => {
    if (streamed) {
      response.writeHead(received.statusCode || 502, { 'Content-Type': received.headers['content-type'] || 'application/json' });
      received.on('error', () => response.destroy());
      received.pipe(response);
      return;
    }
    const output = [];
    let size = 0;
    received.on('data', (chunk) => {
      size += chunk.length;
      if (size > 2 * 1024 * 1024) { received.destroy(); fail(response, 502, 'Upstream response too large'); return; }
      output.push(chunk);
    });
    received.on('end', () => {
      if (response.writableEnded || response.destroyed) return;
      response.writeHead(received.statusCode || 502, { 'Content-Type': received.headers['content-type'] || 'application/json', ...(received.headers['retry-after'] ? { 'Retry-After': received.headers['retry-after'] } : {}) });
      response.end(Buffer.concat(output));
    });
    received.on('error', () => fail(response, 502, 'Backend connection failed'));
  });
  forwarded.setTimeout(15_000, () => forwarded.destroy(new Error('timeout')));
  forwarded.on('error', () => fail(response, 502, 'Backend unavailable'));
  response.on('close', () => { if (!response.writableEnded) forwarded.destroy(); });
  forwarded.end(Buffer.concat(chunks));
}
// serveStatic 从 build/web 读取真实文件；不存在的无扩展名路径回退到预渲染的 index.html，让前端路由刷新也能打开。
async function serveStatic(request, response, url) {
  const relative = path.posix.normalize(`.${decodeURIComponent(url.pathname)}`);
  if (relative.startsWith('..')) return fail(response, 404, 'Not found');
  let fileURL = new URL(relative === '.' ? './index.html' : `.${relative.startsWith('/') ? '' : '/'}${relative}`, staticRoot);
  try {
    const info = await stat(fileURL);
    if (!info.isFile()) throw new Error('not a file');
  } catch {
    if (path.posix.extname(relative)) return fail(response, 404, 'Not found');
    fileURL = new URL('./index.html', staticRoot);
  }
  const data = await readFile(fileURL);
  response.writeHead(200, { 'Content-Type': contentTypes.get(path.posix.extname(fileURL.pathname)) || 'application/octet-stream' });
  response.end(request.method === 'HEAD' ? undefined : data);
}
const server = http.createServer(async (request, response) => {
  response.setHeader('Cache-Control', 'no-store');
  response.setHeader('X-Content-Type-Options', 'nosniff');
  response.setHeader('Referrer-Policy', 'no-referrer');
  // Start 预渲染外壳含内联脚本数据，script-src 需放行 'unsafe-inline'；时间线按需加载私有 OSS 签名预览图，img-src 放行阿里云 OSS。
  response.setHeader('Content-Security-Policy', "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self'; connect-src 'self'; img-src 'self' data: https://*.aliyuncs.com; object-src 'none'; base-uri 'none'; frame-ancestors 'none'");
  try {
    const url = new URL(request.url, 'http://localhost');
    if (url.pathname.startsWith('/api/')) return await proxy(request, response, url);
    if (request.method !== 'GET' && request.method !== 'HEAD') return fail(response, 405, 'Method not allowed');
    await serveStatic(request, response, url);
  } catch { fail(response, 502, 'Request failed'); }
});
server.requestTimeout = 20_000;
server.headersTimeout = 10_000;
server.listen(port, host, () => console.log(`Admin console: http://${host}:${port}`));
