// 本文件是聊天页布局组件：组合消息列表与输入框，并持有“是否贴底滚动”的共享标记。

import { useRef } from 'react';
import type { ChatController } from './ChatController.js';
import { ChatMessageList } from './ChatMessageList.js';
import { MessageComposer } from './MessageComposer.js';

export function ChatPage({ controller }: { controller: ChatController }) {
  const stickToBottom = useRef(true);
  return (
    <main className="chat-page">
      <ChatMessageList controller={controller} stickToBottom={stickToBottom} />
      <MessageComposer
        controller={controller}
        onMessageSent={() => {
          stickToBottom.current = true;
        }}
      />
    </main>
  );
}
