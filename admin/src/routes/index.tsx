// 本文件定义管理看板首页路由 /，直接挂载看板应用组件。

import { createFileRoute } from '@tanstack/react-router';
import { AdminApp } from '../AdminApp.js';

export const Route = createFileRoute('/')({
  component: AdminApp,
});
