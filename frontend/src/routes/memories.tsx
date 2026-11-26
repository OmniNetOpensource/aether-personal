// 本文件定义 /memories 只读记忆浏览路由，由路由器按需分包加载。

import { createFileRoute } from '@tanstack/react-router';
import { MemoriesPage } from '../components/memories/MemoriesPage.js';

export const Route = createFileRoute('/memories')({
  component: MemoriesPage,
});
