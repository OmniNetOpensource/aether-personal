// 本文件是 PWA 构建后处理脚本：生成应用图标与带版本哈希的 Service Worker。联网导航读取当前页面，缓存用于离线启动。

import { createHash } from 'node:crypto';
import { readdir, readFile, writeFile, mkdir } from 'node:fs/promises';
import path from 'node:path';
import sharp from 'sharp';

const output = path.resolve('build/web');
await mkdir(path.join(output, 'icons'), { recursive: true });
for (const size of [192, 512, 180]) {
  await sharp('public/app-icon.png')
    .resize(size, size)
    .png()
    .toFile(path.join(output, 'icons', size === 180 ? 'apple-touch-icon.png' : `icon-${size}.png`));
}
await sharp('public/app-icon.png')
  .resize(320, 320)
  .extend({ top: 96, bottom: 96, left: 96, right: 96, background: '#204b32' })
  .png()
  .toFile(path.join(output, 'icons/maskable-512.png'));

async function filesIn(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = await Promise.all(
    entries.map((entry) => {
      const filename = path.join(directory, entry.name);
      return entry.isDirectory() ? filesIn(filename) : [filename];
    }),
  );
  return files.flat();
}
const files = (await filesIn(output))
  .filter(
    (file) =>
      !/\.(map|txt)$/.test(file) &&
      !path.basename(file).startsWith('_') &&
      !file.endsWith('/sw.js'),
  )
  .sort();
const digest = createHash('sha256');
digest.update(await readFile(new URL(import.meta.url)));
for (const file of files) digest.update(path.relative(output, file)).update(await readFile(file));
const version = digest.digest('hex').slice(0, 16);
const urls = files.map((file) =>
  file.endsWith('/index.html') ? '/' : '/' + path.relative(output, file).split(path.sep).join('/'),
);
const worker = `const CACHE = 'aether-shell-${version}';
const ASSETS = ${JSON.stringify(urls)};
const PATHS = new Set(ASSETS);

// 只有应用页面随版本切换刷新，静态资源与 API 不参与页面导航。
const isAppPage = (pathname) =>
  pathname === '/' || pathname === '/index.html' || /^\\/s\\/[^/]+$/.test(pathname);

self.addEventListener('install', (event) => {
  event.waitUntil((async () => {
    const cache = await caches.open(CACHE);
    await cache.addAll(ASSETS.map((url) => new Request(url, { cache: 'reload' })));
    await self.skipWaiting();
  })());
});
self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    const previous = (await caches.keys()).filter((key) => key.startsWith('aether-shell-') && key !== CACHE);
    await Promise.all(previous.map((key) => caches.delete(key)));
    await self.clients.claim();
    if (previous.length === 0) return;
    const clients = await self.clients.matchAll({ type: 'window' });
    for (const client of clients) {
      if (isAppPage(new URL(client.url).pathname)) {
        // 导航会等待激活结束，不能把导航结果纳入 activate 的 waitUntil。
        void client.navigate(client.url).catch(() => {});
      }
    }
  })());
});
self.addEventListener('fetch', (event) => {
  const request = event.request;
  const url = new URL(request.url);
  if (request.method !== 'GET' || url.origin !== self.location.origin || request.headers.has('Authorization')) return;
  // HTML 联网时请求最新版本，网络失败时读取缓存。其他缓存命中来自公开静态资源清单。
  if (request.mode === 'navigate' && isAppPage(url.pathname)) {
    event.respondWith(fetch(request, { cache: 'no-cache' }).catch(async () =>
      (await caches.match('/', { cacheName: CACHE })) ?? Response.error(),
    ));
  } else if (PATHS.has(url.pathname)) {
    event.respondWith(caches.open(CACHE).then(async (cache) => (await cache.match(url.pathname)) || fetch(request)));
  }
});
`;
await writeFile(path.join(output, 'sw.js'), worker);
console.info(`PWA shell ${version}: ${urls.length} assets`);
