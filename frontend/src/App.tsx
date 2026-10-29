// 本文件是应用根组件：持有全局聊天控制器，按登录状态切换登录页与聊天页，并处理应用的前后台重连。

import { useEffect, useState } from 'react';
import { ChatController } from './ChatController.js';
import { ChatPage } from './ChatPage.js';
import { LoginPage } from './LoginPage.js';
import { PwaControls } from './PwaControls.js';

const controller = new ChatController();

export function App() {
  const [, refresh] = useState(0);
  useEffect(() => {
    const unsubscribe = controller.subscribe(() => refresh((value) => value + 1));
    void controller.initialize();
    const resume = () => {
      if (navigator.onLine && !document.hidden) controller.reconnect();
    };
    const suspend = () => {
      controller.suspend();
    };
    const visibility = () => (document.hidden ? suspend() : resume());
    const sessionChanged = (event: StorageEvent) => {
      if (event.key === 'aether_session' && event.newValue !== controller.token) location.reload();
    };
    window.addEventListener('online', resume);
    window.addEventListener('offline', suspend);
    window.addEventListener('pageshow', resume);
    window.addEventListener('pagehide', suspend);
    window.addEventListener('storage', sessionChanged);
    document.addEventListener('visibilitychange', visibility);
    return () => {
      unsubscribe();
      controller.dispose();
      window.removeEventListener('online', resume);
      window.removeEventListener('offline', suspend);
      window.removeEventListener('pageshow', resume);
      window.removeEventListener('pagehide', suspend);
      window.removeEventListener('storage', sessionChanged);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, []);
  return (
    <>
      {controller.initializing ? (
        <main className="loading">
          <p className="muted">正在加载…</p>
        </main>
      ) : controller.token ? (
        <ChatPage controller={controller} />
      ) : (
        <LoginPage controller={controller} />
      )}
      <PwaControls />
    </>
  );
}
