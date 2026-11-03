// 本文件是聊天页布局组件：组合消息列表与输入框，并持有待发送引用与“是否贴底滚动”的共享标记。

import { useRef } from 'react';
import type { ChatController } from './ChatController.js';
import type { MessageComposerHandle } from './MessageComposer.js';
import { ChatMessageList } from './ChatMessageList.js';
import { MessageComposer } from './MessageComposer.js';

export function ChatPage({ controller }: { controller: ChatController }) {
  const composer = useRef<MessageComposerHandle | null>(null);
  // 只有人已经停在底部时才跟着新内容走。发送本身不把正在看更早消息的人拉回去。
  const stickToBottom = useRef(true);
  return (
    <main className="chat-page">
      <ChatMessageList
        controller={controller}
        stickToBottom={stickToBottom}
        onQuote={(quote) => {
          composer.current?.insertQuote(quote);
        }}
      />
      <MessageComposer controller={controller} composerRef={composer} />
    </main>
  );
}
