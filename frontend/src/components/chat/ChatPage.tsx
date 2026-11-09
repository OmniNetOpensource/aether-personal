// 本文件组合聊天列表、输入框和历史搜索入口；搜索与上下文浏览覆盖在原页面上，保留草稿和阅读位置。

import { useRef, useState } from 'react';
import type { ChatController } from '../../lib/ChatController.js';
import type { MessageComposerHandle } from './MessageComposer.js';
import { ChatMessageList } from './ChatMessageList.js';
import { MessageComposer } from './MessageComposer.js';
import { ChatHistorySearch } from './ChatHistorySearch.js';

export function ChatPage({ controller }: { controller: ChatController }) {
  const composer = useRef<MessageComposerHandle | null>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [latestRequest, setLatestRequest] = useState(0);
  // 只有人已经停在底部时才跟着新内容走。发送本身不把正在看更早消息的人拉回去。
  const stickToBottom = useRef(true);
  return (
    <main className="chat-page">
      <ChatMessageList
        controller={controller}
        stickToBottom={stickToBottom}
        latestRequest={latestRequest}
        onQuote={(quote) => {
          composer.current?.insertQuote(quote);
        }}
      />
      <MessageComposer controller={controller} composerRef={composer} />
      <button
        type="button"
        className="chat-search-launch"
        aria-label="搜索历史聊天"
        title="搜索历史聊天"
        onClick={() => setSearchOpen(true)}
      >
        <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <circle cx="10.5" cy="10.5" r="6.5" />
          <path d="m16 16 4 4" />
        </svg>
      </button>
      {searchOpen && (
        <ChatHistorySearch
          controller={controller}
          onClose={() => setSearchOpen(false)}
          onLatest={() => {
            setSearchOpen(false);
            setLatestRequest((value) => value + 1);
          }}
        />
      )}
    </main>
  );
}
