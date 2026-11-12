// 本文件配置 TanStack Start 与 Rsbuild，生成静态 SPA 外壳并代理同源聊天、模型设置、公开分享、图片文件与只读桌面连接。

import { defineConfig } from '@rsbuild/core';
import { pluginReact } from '@rsbuild/plugin-react';
import { tanstackStart } from '@tanstack/react-start/plugin/rsbuild';

export default defineConfig({
  plugins: [
    pluginReact(),
    tanstackStart({
      router: { semicolons: true },
      spa: { enabled: true, prerender: { outputPath: '/index.html' } },
    }),
  ],
  server: {
    port: 3000,
    proxy: {
      '/ws': { target: 'http://127.0.0.1:8080', ws: true },
      '/sandbox/desktop': { target: 'http://127.0.0.1:8080', ws: true },
      '/model-settings': { target: 'http://127.0.0.1:8080' },
      '/images': { target: 'http://127.0.0.1:8080' },
      '/files': { target: 'http://127.0.0.1:8080' },
      '/shares': { target: 'http://127.0.0.1:8080' },
    },
  },
  // 服务端产物仅供构建时预渲染，不进入 Go 或 Cloudflare 分发的静态目录。
  environments: {
    client: { output: { distPath: { root: 'build/web' } } },
    ssr: { output: { distPath: { root: 'build/server' } } },
  },
});
