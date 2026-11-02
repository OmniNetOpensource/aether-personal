// 本文件是聊天应用组件：随页面生命周期持有聊天控制器，切换登录与聊天界面，并处理可视视口和前后台重连。

import { useEffect, useState } from 'react';
import { ChatController } from './ChatController.js';
import { ChatPage } from './ChatPage.js';
import { LoginPage } from './LoginPage.js';
import { PwaControls } from './PwaControls.js';

// 每次 effect 启动创建独立控制器，兼容 Strict Mode 的清理重跑和路由重挂载。
export function App() {
  const [controller, setController] = useState<ChatController | null>(null);
  const [, refresh] = useState(0);
  useEffect(() => {
    const controller = new ChatController();
    setController(controller);
    // 可视视口跟随移动键盘变化；路由卸载时一起移除监听。
    const resize = () => {
      const viewport = window.visualViewport;
      document.documentElement.style.setProperty(
        '--app-height',
        `${viewport?.height ?? innerHeight}px`,
      );
      document.documentElement.style.setProperty('--app-top', `${viewport?.offsetTop ?? 0}px`);
    };
    window.visualViewport?.addEventListener('resize', resize);
    window.visualViewport?.addEventListener('scroll', resize);
    window.addEventListener('resize', resize);
    resize();

    // 登录与前后台生命周期继续交给聊天控制器，不依赖 Start 服务端。
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
      window.visualViewport?.removeEventListener('resize', resize);
      window.visualViewport?.removeEventListener('scroll', resize);
      window.removeEventListener('resize', resize);
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
      {!controller || controller.initializing ? (
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
