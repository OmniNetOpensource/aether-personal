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

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(ASSETS.map((url) => new Request(url, { cache: 'reload' })))));
});
self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    for (const key of await caches.keys()) {
      if (key.startsWith('aether-shell-') && key !== CACHE) await caches.delete(key);
    }
    await self.clients.claim();
  })());
});
self.addEventListener('message', (event) => {
  if (event.data?.type === 'SKIP_WAITING') self.skipWaiting();
});
self.addEventListener('fetch', (event) => {
  const request = event.request;
  const url = new URL(request.url);
  if (request.method !== 'GET' || url.origin !== self.location.origin || request.headers.has('Authorization')) return;
  // The explicit asset list excludes all API calls, messages and authentication responses.
  if (url.pathname === '/' || url.pathname === '/index.html') {
    event.respondWith(caches.open(CACHE).then(async (cache) => (await cache.match('/')) || fetch(request)));
  } else if (PATHS.has(url.pathname)) {
    event.respondWith(caches.open(CACHE).then(async (cache) => (await cache.match(url.pathname)) || fetch(request)));
  }
});
`;
await writeFile(path.join(output, 'sw.js'), worker);
console.info(`PWA shell ${version}: ${urls.length} assets`);
