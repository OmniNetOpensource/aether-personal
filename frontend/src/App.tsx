// 本文件是聊天应用组件：挂载 Effect 聊天会话与浏览器生命周期，按细粒度状态切换登录与聊天界面。

import { useAtomMount, useAtomValue } from '@effect/atom-react';
import { chatBrowserLifecycleAtom, chatSessionAtom, chatStateAtom } from './lib/runtime.js';
import { ChatPage } from './components/chat/ChatPage.js';
import { LoadingScreen } from './components/LoadingScreen.js';
import { LoginPage } from './components/login/LoginPage.js';
import { PwaControls } from './components/PwaControls.js';

// App 通过挂载 scoped Atom 持有聊天连接和浏览器前后台监听，组件卸载时自动释放 Scope。
export function App() {
  useAtomMount(chatSessionAtom);
  useAtomMount(chatBrowserLifecycleAtom);
  const initializing = useAtomValue(chatStateAtom, (state) => state.initializing);
  const hasToken = useAtomValue(chatStateAtom, (state) => Boolean(state.token));

  return (
    <>
      {initializing ? <LoadingScreen /> : hasToken ? <ChatPage /> : <LoginPage />}
      <PwaControls />
    </>
  );
}
