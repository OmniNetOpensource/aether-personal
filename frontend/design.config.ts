// 本文件是本地设计画布的独立 Rsbuild 配置：单独编译与预览聊天端全状态画板，不进入线上构建与 PWA 缓存。

import { defineConfig } from '@rsbuild/core';
import { pluginReact } from '@rsbuild/plugin-react';

export default defineConfig({
  plugins: [pluginReact()],
  source: {
    entry: {
      index: './src/design/main.tsx',
    },
  },
  html: {
    title: 'aether · 设计画布',
    favicon: './public/app-icon.png',
    mountId: 'root',
  },
  server: {
    port: 3001,
  },
  output: {
    distPath: {
      root: 'build/design',
    },
  },
});
