// 本文件是管理看板的独立服务入口：托管看板静态页面，并把管理 API 请求代理到后端。

import http from 'node:http';
import https from 'node:https';
import { readFile } from 'node:fs/promises';

const host = process.env.HOST || '127.0.0.1';
const port = Number(process.env.PORT || 5174);
const upstream = new URL(process.env.API_UPSTREAM || 'http://127.0.0.1:8080');
if (!['http:', 'https:'].includes(upstream.protocol) || upstream.username || upstream.password || upstream.pathname !== '/' || upstream.search || upstream.hash) {
  throw new Error('API_UPSTREAM must be an HTTP(S) origin without credentials, path, query or fragment');
}
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('PORT must be between 1 and 65535');
const staticFiles = new Map([
  ['/', ['index.html', 'text/html; charset=utf-8']],
  ['/app.js', ['app.js', 'text/javascript; charset=utf-8']],
  ['/style.css', ['style.css', 'text/css; charset=utf-8']],
  ['/app-icon.png', ['app-icon.png', 'image/png']],
]);
const apiMethods = new Map([
  ['/admin/login', 'POST'], ['/admin/logout', 'POST'],
  ['/admin/me', 'GET'], ['/admin/snapshot', 'GET'], ['/admin/calls', 'GET'], ['/admin/timeline', 'GET'],
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
  const serviceConfig = /^\/admin\/services\/(openrouter|ikun_gemini|resend|serper|parallel|everyinfra)$/.test(path);
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
const server = http.createServer(async (request, response) => {
  response.setHeader('Cache-Control', 'no-store');
  response.setHeader('X-Content-Type-Options', 'nosniff');
  response.setHeader('Referrer-Policy', 'no-referrer');
  response.setHeader('Content-Security-Policy', "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'");
  try {
    const url = new URL(request.url, 'http://localhost');
    if (url.pathname.startsWith('/api/')) return await proxy(request, response, url);
    if (request.method !== 'GET' && request.method !== 'HEAD') return fail(response, 405, 'Method not allowed');
    const file = staticFiles.get(url.pathname);
    if (!file) return fail(response, 404, 'Not found');
    const data = await readFile(new URL(`./public/${file[0]}`, import.meta.url));
    response.writeHead(200, { 'Content-Type': file[1] });
    response.end(request.method === 'HEAD' ? undefined : data);
  } catch { fail(response, 502, 'Request failed'); }
});
server.requestTimeout = 20_000;
server.headersTimeout = 10_000;
server.listen(port, host, () => console.log(`Admin console: http://${host}:${port}`));
