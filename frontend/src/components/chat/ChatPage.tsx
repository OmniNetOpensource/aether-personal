// 本文件组合聊天列表、输入框、历史搜索和只读桌面入口；浮层保留聊天草稿和阅读位置。

import { useRef, useState } from 'react';
import type { ChatController } from '../../lib/ChatController.js';
import type { MessageComposerHandle } from './MessageComposer.js';
import { ChatMessageList } from './ChatMessageList.js';
import { MessageComposer } from './MessageComposer.js';
import { ChatHistorySearch } from './ChatHistorySearch.js';
import { DesktopViewer } from './DesktopViewer.js';

export function ChatPage({ controller }: { controller: ChatController }) {
  const composer = useRef<MessageComposerHandle | null>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [desktopOpen, setDesktopOpen] = useState(false);
  const [latestRequest, setLatestRequest] = useState(0);
  return (
    <main className="chat-page">
      <ChatMessageList
        controller={controller}
        latestRequest={latestRequest}
        onQuote={(quote) => {
          composer.current?.insertQuote(quote);
        }}
      />
      <MessageComposer controller={controller} composerRef={composer} />
      <button
        type="button"
        className="chat-desktop-launch"
        aria-label="查看电脑"
        title="查看电脑"
        onClick={() => setDesktopOpen(true)}
      >
        <svg viewBox="0 0 24 24" aria-hidden="true">
          <rect x="3" y="4" width="18" height="13" rx="2" />
          <path d="M8 21h8m-4-4v4" />
        </svg>
      </button>
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
      {desktopOpen && controller.token && (
        <DesktopViewer token={controller.token} onClose={() => setDesktopOpen(false)} />
      )}
    </main>
  );
}
