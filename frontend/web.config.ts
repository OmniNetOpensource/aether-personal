// 本文件配置 Rsbuild 构建与本地开发代理，生成纯静态 SPA 外壳并代理同源聊天、模型设置、记忆查看、远程 MCP 与 OAuth、图片文件及只读桌面连接。

import { defineConfig } from '@rsbuild/core';
import { pluginReact } from '@rsbuild/plugin-react';

export default defineConfig({
  plugins: [pluginReact()],
  source: {
    entry: {
      index: './src/main.tsx',
    },
  },
  html: {
    template: './index.html',
    title: 'aether',
  },
  output: {
    cleanDistPath: true,
    distPath: { root: 'build/web' },
    assetPrefix: '/',
  },
  server: {
    port: 3000,
    historyApiFallback: true,
    proxy: {
      '/ws': { target: 'http://127.0.0.1:8080', ws: true },
      '/sandbox/desktop': { target: 'http://127.0.0.1:8080', ws: true },
      '/model-settings': { target: 'http://127.0.0.1:8080' },
      '/memory-files': { target: 'http://127.0.0.1:8080' },
      '/.well-known': { target: 'http://127.0.0.1:8080' },
      '/mcp': { target: 'http://127.0.0.1:8080' },
      '/oauth/register': { target: 'http://127.0.0.1:8080' },
      '/oauth/token': { target: 'http://127.0.0.1:8080' },
      '/oauth/revoke': { target: 'http://127.0.0.1:8080' },
      '/oauth/authorize/context': { target: 'http://127.0.0.1:8080' },
      '/oauth/authorize/decision': { target: 'http://127.0.0.1:8080' },
      '/oauth/connections': { target: 'http://127.0.0.1:8080' },
      '/images': { target: 'http://127.0.0.1:8080' },
      '/files': { target: 'http://127.0.0.1:8080' },
    },
  },
});
