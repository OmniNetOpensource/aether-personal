// 本文件定义管理看板首页路由，挂载看板应用；管理员身份与数据都在浏览器中恢复，不做服务端渲染。

import { createFileRoute } from '@tanstack/react-router';
import { AdminApp } from '../AdminApp.js';

export const Route = createFileRoute('/')({
  ssr: false,
  component: AdminApp,
});
