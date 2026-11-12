// 本文件创建 TanStack Start 使用的路由器，把生成的文件路由接入应用并提供首屏加载提示。

import { createRouter } from '@tanstack/react-router';
import { LoadingScreen } from './components/LoadingScreen.js';
import { routeTree } from './routeTree.gen';

// 路由实例由 Start 为每次应用启动创建，聊天列表继续自行管理滚动位置。
export function getRouter() {
  return createRouter({
    routeTree,
    defaultPendingComponent: LoadingScreen,
  });
}

// 让 Link、路由参数与导航 API 使用本应用生成的路由类型。
declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof getRouter>;
  }
}
