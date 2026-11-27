// 本文件是浏览器端应用入口：加载全局样式、挂载 Effect Atom RegistryProvider 与 TanStack Router 实例。

import { parse } from 'gpu-lexer';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { RegistryProvider } from '@effect/atom-react';
import { RouterProvider, createRouter } from '@tanstack/react-router';
import { LoadingScreen } from './components/LoadingScreen.js';
import { routeTree } from './routeTree.gen';
import './styles/style.css';

// 全局路由实例复用 LoadingScreen 作为路由分包切换期间的占位界面。
const router = createRouter({
  routeTree,
  // 在入口加载模型，代码块通过路由上下文复用同一解析器。
  context: { parseCode: parse },
  defaultPendingComponent: LoadingScreen,
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
    <RegistryProvider>
      <RouterProvider router={router} />
    </RegistryProvider>
  </StrictMode>,
);
