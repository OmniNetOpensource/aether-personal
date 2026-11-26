// 本文件定义管理看板的根路由容器，负责承载子路由出口。

import { Outlet, createRootRoute } from '@tanstack/react-router';

// RootLayout 保持最简壳层，页面标题、元信息与挂载节点由 index.html 维护。
function RootLayout() {
  return <Outlet />;
}

export const Route = createRootRoute({
  component: RootLayout,
});
