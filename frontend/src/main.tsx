// 本文件是浏览器端应用入口：加载全局样式并按当前路径渲染聊天主页、只读记忆页或 OAuth 授权确认页。

import { StrictMode, Suspense, lazy } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App.js';
import { LoadingScreen } from './components/LoadingScreen.js';
import { usePathname } from './lib/navigation.js';
import './styles/style.css';

// 只读记忆页及其样式单独分包加载，避免拖慢主聊天首屏。
const MemoriesPage = lazy(() =>
  import('./components/memories/MemoriesPage.js').then((module) => ({
    default: module.MemoriesPage,
  })),
);

// 远程记忆 MCP 授权页单独分包加载，仅外部应用跳入时按需拉取。
const OAuthAuthorizePage = lazy(() =>
  import('./components/oauth/OAuthAuthorizePage.js').then((module) => ({
    default: module.OAuthAuthorizePage,
  })),
);

// RootRouter 根据当前地址栏路径分发到聊天应用、只读记忆浏览页或 OAuth 授权确认页。
function RootRouter() {
  const pathname = usePathname();
  if (pathname === '/memories') {
    return (
      <Suspense fallback={<LoadingScreen />}>
        <MemoriesPage />
      </Suspense>
    );
  }
  if (pathname === '/oauth/authorize') {
    return (
      <Suspense fallback={<LoadingScreen />}>
        <OAuthAuthorizePage />
      </Suspense>
    );
  }
  return <App />;
}

const rootElement = document.getElementById('root');
if (!rootElement) throw new Error('缺少应用挂载节点 #root');

createRoot(rootElement).render(
  <StrictMode>
    <RootRouter />
  </StrictMode>,
);
