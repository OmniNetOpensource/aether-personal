// 本文件定义聊天首页路由，保留已有登录、聊天与 PWA 功能，并仅在浏览器中渲染聊天状态。

import { createFileRoute } from '@tanstack/react-router';
import { App } from '../App.js';

export const Route = createFileRoute('/')({
  ssr: false,
  component: App,
});
