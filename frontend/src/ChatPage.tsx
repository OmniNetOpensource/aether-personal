// 本文件是聊天页布局组件：组合消息列表与输入框，并持有待发送引用与“是否贴底滚动”的共享标记。

import { useRef, useState } from 'react';
import type { ChatController } from './ChatController.js';
import type { MessageComposerHandle } from './MessageComposer.js';
import { ChatMessageList } from './ChatMessageList.js';
import { MessageComposer } from './MessageComposer.js';

export function ChatPage({ controller }: { controller: ChatController }) {
  const composer = useRef<MessageComposerHandle | null>(null);
  const stickToBottom = useRef(true);
  // 发送成功时消息往往已经进入列表。序号让列表在贴底标记置位后再滚到底。
  const [bottomPin, setBottomPin] = useState(0);
  return (
    <main className="chat-page">
      <ChatMessageList
        controller={controller}
        stickToBottom={stickToBottom}
        bottomPin={bottomPin}
        onQuote={(quote) => {
          composer.current?.insertQuote(quote);
        }}
      />
      <MessageComposer
        controller={controller}
        composerRef={composer}
        onMessageSent={() => {
          stickToBottom.current = true;
          setBottomPin((count) => count + 1);
        }}
      />
    </main>
  );
}
