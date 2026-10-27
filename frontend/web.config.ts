import { defineConfig } from '@rsbuild/core';
import { pluginReact } from '@rsbuild/plugin-react';

export default defineConfig({
  plugins: [pluginReact()],
  server: {
    port: 3000,
    proxy: {
      '/auth': 'http://127.0.0.1:8080',
      '/messages': 'http://127.0.0.1:8080',
      '/ws': { target: 'http://127.0.0.1:8080', ws: true },
    },
  },
  source: { entry: { index: './src/index.tsx' } },
  html: {
    title: '阿芒',
    template: './index.html',
    tags: [
      { tag: 'link', attrs: { rel: 'icon', type: 'image/png', href: '/app-icon.png' } },
      { tag: 'link', attrs: { rel: 'apple-touch-icon', href: '/icons/apple-touch-icon.png' } },
      { tag: 'link', attrs: { rel: 'manifest', href: '/manifest.webmanifest' } },
    ],
    meta: {
      viewport:
        'width=device-width, initial-scale=1, viewport-fit=cover, interactive-widget=resizes-content',
      'theme-color': '#204b32',
      'apple-mobile-web-app-capable': 'yes',
      'apple-mobile-web-app-status-bar-style': 'black-translucent',
      'apple-mobile-web-app-title': '阿芒',
      description: '和阿芒聊聊，你的个人 AI 助手。',
    },
  },
  output: {
    distPath: { root: 'build/web' },
  },
});
