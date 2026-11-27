// 本文件定义前端应用的根路由容器，负责承载各子页面的出口渲染。

import { Outlet, createRootRouteWithContext } from '@tanstack/react-router';

import type { parse } from 'gpu-lexer';

// RootLayout 保持最简壳层，页面标题、元信息与挂载节点由 index.html 维护。
function RootLayout() {
  return <Outlet />;
}

export const Route = createRootRouteWithContext<{ parseCode: typeof parse }>()({
  component: RootLayout,
});
