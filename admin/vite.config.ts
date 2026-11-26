// 本文件配置管理看板的 Vite+ 构建、TanStack Router 文件路由、本地开发 API 代理，以及格式化与静态检查规则。

import { defineConfig } from 'vite-plus';
import { tanstackRouter } from '@tanstack/router-plugin/vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [
    // TanStack Router 插件必须在 React 插件之前运行，以生成文件路由并启用按路由拆包。
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
    port: 5174,
    proxy: {
      // 开发时绕过 server.mjs，直接把 /api 前缀去掉后转给 Go 后端；生产仍由 server.mjs 做白名单代理。
      '/api': {
        target: 'http://127.0.0.1:8080',
        rewrite: (requestPath) => requestPath.replace(/^\/api/, ''),
      },
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
