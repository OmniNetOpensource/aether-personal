// 本文件定义聊天主页路由 /，直接挂载登录与实时聊天应用。

import { createFileRoute } from '@tanstack/react-router';
import { App } from '../App.js';

export const Route = createFileRoute('/')({
  component: App,
});
