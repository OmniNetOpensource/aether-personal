// 本文件是管理看板浏览器端入口：加载全局样式、创建 TanStack Router 实例并挂载根组件。

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { RouterProvider, createRouter } from '@tanstack/react-router';
import { routeTree } from './routeTree.gen';
import './style.css';

// 路由切换或按需分包加载期间显示统一的首屏加载提示。
function LoadingPage() {
  return (
    <main className="loading">
      <p className="muted">正在加载…</p>
    </main>
  );
}

// 看板路由实例挂载文件路由树，页签状态仍由 AdminApp 组件内部管理。
const router = createRouter({
  routeTree,
  defaultPendingComponent: LoadingPage,
});

// 向 TanStack Router 注册本应用生成的路由树，使 Link 与导航参数具备完整类型推导。
declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}

const rootElement = document.getElementById('root');
if (!rootElement) throw new Error('缺少应用挂载节点 #root');

createRoot(rootElement).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
);
