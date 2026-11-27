// 本文件配置 Vite+ 构建、TanStack Router 文件路由、本地开发代理，以及统一的格式化与静态检查规则。

import { defineConfig } from 'vite-plus';
import { tanstackRouter } from '@tanstack/router-plugin/vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [
    // TanStack Router 插件必须在 React 插件之前运行，以完成文件路由生成与按路由自动拆包。
    tanstackRouter({
      target: 'react',
      autoCodeSplitting: true,
      semicolons: true,
    }),
    react(),
  ],
  build: {
    outDir: 'build/web',
    emptyOutDir: true,
  },
  server: {
    port: 3000,
    proxy: {
      '/auth': { target: 'http://127.0.0.1:8080' },
      '/messages': { target: 'http://127.0.0.1:8080' },
      '/shares': { target: 'http://127.0.0.1:8080' },
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
  fmt: {
    singleQuote: true,
    printWidth: 100,
    ignorePatterns: ['build/**', 'src/routeTree.gen.ts', 'README.md'],
  },
  lint: {
    ignorePatterns: ['build/**', 'src/routeTree.gen.ts', 'README.md'],
    plugins: ['typescript', 'react'],
    options: {
      typeAware: true,
      typeCheck: true,
    },
  },
});
