// 本文件创建管理看板使用的 TanStack Start 路由器，把生成的文件路由接入应用并提供首屏加载提示。

import { createRouter } from '@tanstack/react-router';
import { routeTree } from './routeTree.gen';

// 路由实例由 Start 为每次应用启动创建，看板页签状态由应用组件自行管理。
export function getRouter() {
  return createRouter({
    routeTree,
    defaultPendingComponent: LoadingPage,
  });
}

// SPA 外壳与路由加载期间使用相同的首屏提示。
function LoadingPage() {
  return (
    <main className="loading">
      <p className="muted">正在加载…</p>
    </main>
  );
}

// 让 Link、路由参数与导航 API 使用本应用生成的路由类型。
declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof getRouter>;
  }
}
