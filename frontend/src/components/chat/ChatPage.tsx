// 本文件组合聊天列表、输入框、历史搜索、只读桌面、模型设置与账号入口；浮层保留聊天草稿和阅读位置。

import { useEffect, useRef, useState } from 'react';
import type { ChatController } from '../../lib/ChatController.js';
import type { MessageComposerHandle } from './MessageComposer.js';
import { ChatMessageList } from './ChatMessageList.js';
import { MessageComposer } from './MessageComposer.js';
import { ChatHistorySearch } from './ChatHistorySearch.js';
import { DesktopViewer } from './DesktopViewer.js';
import { ChatModelSettings } from './ChatModelSettings.js';

// ChatPage 承载登录后的全屏对话界面与右上角悬浮操作入口。
export function ChatPage({ controller }: { controller: ChatController }) {
  const composer = useRef<MessageComposerHandle | null>(null);
  const accountWrap = useRef<HTMLDivElement>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [desktopOpen, setDesktopOpen] = useState(false);
  const [accountOpen, setAccountOpen] = useState(false);
  const [modelsOpen, setModelsOpen] = useState(false);
  const accountButton = useRef<HTMLButtonElement>(null);
  const [signingOut, setSigningOut] = useState(false);
  const [latestRequest, setLatestRequest] = useState(0);

  // 点击账号浮层外部或按下 Escape 时收起菜单，避免遮挡消息阅读。
  useEffect(() => {
    if (!accountOpen) return;
    const closeOnOutside = (event: PointerEvent) => {
      if (event.target instanceof Node && !accountWrap.current?.contains(event.target)) {
        setAccountOpen(false);
      }
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setAccountOpen(false);
    };
    document.addEventListener('pointerdown', closeOnOutside);
    document.addEventListener('keydown', closeOnEscape);
    return () => {
      document.removeEventListener('pointerdown', closeOnOutside);
      document.removeEventListener('keydown', closeOnEscape);
    };
  }, [accountOpen]);

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
      <div className="chat-floating-actions">
        {controller.desktopAvailable && (
          <button
            type="button"
            className="chat-desktop-launch"
            aria-label="查看电脑"
            title="查看电脑"
            onClick={() => {
              setAccountOpen(false);
              setDesktopOpen(true);
            }}
          >
            <svg viewBox="0 0 24 24" aria-hidden="true">
              <rect x="3" y="4" width="18" height="13" rx="2" />
              <path d="M8 21h8m-4-4v4" />
            </svg>
          </button>
        )}
        <button
          type="button"
          className="chat-search-launch"
          aria-label="搜索历史聊天"
          title="搜索历史聊天"
          onClick={() => {
            setAccountOpen(false);
            setSearchOpen(true);
          }}
        >
          <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <circle cx="10.5" cy="10.5" r="6.5" />
            <path d="m16 16 4 4" />
          </svg>
        </button>
        <div className="chat-account-wrap" ref={accountWrap}>
          <button
            type="button"
            className="chat-account-launch"
            ref={accountButton}
            aria-label="账号"
            aria-expanded={accountOpen}
            title="账号"
            onClick={() => setAccountOpen((open) => !open)}
          >
            <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
              <circle cx="12" cy="8.5" r="3.5" />
              <path d="M5.5 19.5a6.5 6.5 0 0 1 13 0" />
            </svg>
          </button>
          {accountOpen && (
            <div className="chat-account-popover" role="dialog" aria-label="当前账号">
              <p className="chat-account-email" title={controller.email}>
                {controller.email || '已登录账号'}
              </p>
              <button
                type="button"
                className="chat-account-models"
                onClick={() => {
                  setAccountOpen(false);
                  setModelsOpen(true);
                }}
              >
                模型设置
              </button>
              <button
                type="button"
                className="chat-account-logout"
                disabled={signingOut}
                onClick={async () => {
                  setSigningOut(true);
                  await controller.signOut();
                }}
              >
                {signingOut ? '正在退出…' : '退出登录'}
              </button>
            </div>
          )}
        </div>
      </div>
      {modelsOpen && (
        <ChatModelSettings
          controller={controller}
          onClose={() => {
            setModelsOpen(false);
            accountButton.current?.focus({ preventScroll: true });
          }}
        />
      )}
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
