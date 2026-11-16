// 本文件定义 /memories 记忆浏览路由，仅在浏览器端加载当前用户的只读记忆文件视图。

import { createFileRoute } from '@tanstack/react-router';
import { MemoriesPage } from '../components/memories/MemoriesPage.js';

export const Route = createFileRoute('/memories')({
  ssr: false,
  component: MemoriesPage,
});
