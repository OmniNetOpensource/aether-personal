// 本文件配置管理看板的 TanStack Start 与 Rsbuild，生成静态 SPA 外壳，并为本地开发代理管理 API。

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
    port: 5174,
    proxy: {
      // 开发时绕过 server.mjs，直接把 /api 前缀去掉后转给 Go 后端；生产仍由 server.mjs 做白名单代理。
      '/api': {
        target: 'http://127.0.0.1:8080',
        pathRewrite: { '^/api': '' },
      },
    },
  },
  // 服务端产物仅供构建时预渲染外壳，不进入 server.mjs 托管的静态目录。
  environments: {
    client: { output: { distPath: { root: 'build/web' } } },
    ssr: { output: { distPath: { root: 'build/server' } } },
  },
});
