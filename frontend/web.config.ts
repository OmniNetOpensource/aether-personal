import { defineConfig } from '@rsbuild/core';

export default defineConfig({
  server: {
    port: 3000,
    proxy: {
      '/auth': 'http://127.0.0.1:8080',
      '/messages': 'http://127.0.0.1:8080',
      '/sandbox': { target: 'http://127.0.0.1:8080', ws: true },
      '/ws': { target: 'http://127.0.0.1:8080', ws: true },
    },
  },
  source: {
    entry: { index: './host/index.ts' },
    define: {
      'process.env.PUBLIC_API_BASE_URL': JSON.stringify(process.env.PUBLIC_API_BASE_URL ?? ''),
    },
  },
  html: {
    title: '阿芒',
    tags: [
      { tag: 'link', attrs: { rel: 'icon', type: 'image/png', href: '/app-icon.png' } },
      { tag: 'link', attrs: { rel: 'apple-touch-icon', href: '/app-icon.png' } },
    ],
    meta: {
      viewport: 'width=device-width, initial-scale=1, viewport-fit=cover',
      'theme-color': '#204b32',
    },
  },
  output: {
    distPath: { root: 'build/web' },
    copy: [
      { from: './dist', to: '.' },
      { from: './public', to: '.' },
      { from: './assets/fonts/OFL.txt', to: 'fonts/OFL.txt' },
    ],
  },
});
