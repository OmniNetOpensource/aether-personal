// 本文件是聊天消息列表组件：渲染消息、时间分隔与处理中状态，并管理滚动贴底和上翻加载历史。

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { RefObject, UIEvent } from 'react';
import { AssistantMessageBubble } from './AssistantMessageBubble.js';
import { AssistantWorkingStatus } from './AssistantWorkingStatus.js';
import type { ChatController, ChatMessage } from './ChatController.js';
import { MessageTimeSeparator, sameDay } from './MessageTimeSeparator.js';
import { QuoteSelection, highlightQuote } from './QuoteSelection.js';
import type { MessageQuote } from './messageAttachments.js';
import { UserMessageBubble } from './UserMessageBubble.js';

export function ChatMessageList({
  controller,
  stickToBottom,
  onQuote,
}: {
  controller: ChatController;
  onQuote: (quote: MessageQuote) => void;
  stickToBottom: RefObject<boolean>;
}) {
  const viewport = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const userScrolling = useRef(false);
  const loadingOlder = useRef(false);
  const quoteCleanup = useRef<() => void>(() => {});
  const quoteTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const navigating = useRef(false);
  const [quoteError, setQuoteError] = useState('');
  useEffect(
    () => () => {
      clearTimeout(quoteTimer.current);
      quoteCleanup.current();
    },
    [],
  );
  // 原文不在已加载历史时继续翻页；用户仍停在当前位置，找到后才定位。
  const navigateQuote = async (quote: MessageQuote) => {
    if (navigating.current) return;
    navigating.current = true;
    stickToBottom.current = false;
    setQuoteError('');
    try {
      while (
        !controller.messages.some((message) => message.id === quote.message_id) &&
        controller.hasMoreHistory &&
        !controller.loadingHistory
      ) {
        const count = controller.messages.length;
        await controller.loadHistory(true);
        if (controller.messages.length === count || controller.error) break;
      }
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      const body = viewport.current?.querySelector<HTMLElement>(
        `[data-quote-body][data-message-id="${quote.message_id}"]`,
      );
      if (!body) {
        setQuoteError('暂时找不到引用原文，请加载历史后重试');
        return;
      }
      clearTimeout(quoteTimer.current);
      quoteCleanup.current();
      quoteCleanup.current = highlightQuote(body, quote);
      quoteTimer.current = setTimeout(() => quoteCleanup.current(), 3000);
    } finally {
      navigating.current = false;
    }
  };
  const messages = controller.messages;
  const thinking = controller.connected && controller.mainThinking;
  const scrollToBottom = () => {
    userScrolling.current = false;
    const element = viewport.current;
    if (element) element.scrollTop = element.scrollHeight;
  };
  useLayoutEffect(() => {
    if (stickToBottom.current && !loadingOlder.current) scrollToBottom();
  }, [messages, thinking, stickToBottom]);
  useEffect(() => {
    const resize = new ResizeObserver(() => {
      if (stickToBottom.current && !loadingOlder.current) scrollToBottom();
    });
    if (viewport.current) resize.observe(viewport.current);
    if (content.current) resize.observe(content.current);
    return () => resize.disconnect();
  }, [stickToBottom]);
  const loadOlderMessages = async () => {
    const element = viewport.current;
    if (!element || loadingOlder.current || controller.loadingHistory || !controller.hasMoreHistory)
      return;
    loadingOlder.current = true;
    stickToBottom.current = false;
    const height = element.scrollHeight,
      top = element.scrollTop;
    await controller.loadHistory(true);
    requestAnimationFrame(() => {
      element.scrollTop = top + element.scrollHeight - height;
      loadingOlder.current = false;
    });
  };
  const onScroll = (event: UIEvent<HTMLDivElement>) => {
    if (!userScrolling.current) return;
    const element = event.currentTarget;
    stickToBottom.current = element.scrollHeight - element.scrollTop - element.clientHeight < 160;
    if (element.scrollTop < 160 && !stickToBottom.current) void loadOlderMessages();
  };
  return (
    <div
      id="messages"
      className="messages"
      ref={viewport}
      onScroll={onScroll}
      onWheel={() => {
        userScrolling.current = true;
      }}
      onTouchStart={() => {
        userScrolling.current = true;
      }}
      onPointerDown={() => {
        userScrolling.current = true;
      }}
      onKeyDown={() => {
        userScrolling.current = true;
      }}
      onScrollEnd={() => {
        userScrolling.current = false;
      }}
      tabIndex={0}
      aria-label="聊天消息"
    >
      <QuoteSelection container={viewport} onQuote={onQuote} />
      <div className="message-content" ref={content}>
        {quoteError && (
          <p className="error" role="status">
            {quoteError}
          </p>
        )}
        {controller.error && (
          <button
            type="button"
            className="history-error error"
            onClick={() => void controller.loadHistory()}
          >
            {controller.error}
          </button>
        )}
        {controller.hasMoreHistory && messages.length > 0 && (
          <button
            type="button"
            className="load-older"
            disabled={controller.loadingHistory}
            onClick={() => void loadOlderMessages()}
          >
            {controller.loadingHistory ? '正在加载…' : '加载更早的消息'}
          </button>
        )}
        {messages.map((message, index) => {
          const previous = messages[index - 1],
            next = messages[index + 1];
          const showTime =
            !previous ||
            !sameDay(previous.createdAt, message.createdAt) ||
            Math.abs(message.createdAt.getTime() - previous.createdAt.getTime()) >= 30 * 60000;
          const joinedWithPrevious = Boolean(previous && sameGroup(previous, message));
          const endsGroup = !(next && sameGroup(message, next));
          return (
            <div key={message.id}>
              {showTime && <MessageTimeSeparator createdAt={message.createdAt} />}
              {message.isUser ? (
                <UserMessageBubble
                  message={message}
                  joinedWithPrevious={joinedWithPrevious}
                  endsGroup={endsGroup}
                  onQuoteNavigate={(quote) => void navigateQuote(quote)}
                />
              ) : (
                <AssistantMessageBubble
                  message={message}
                  joinedWithPrevious={joinedWithPrevious}
                  endsGroup={endsGroup}
                />
              )}
            </div>
          );
        })}
        {thinking && <AssistantWorkingStatus />}
      </div>
    </div>
  );
}

function sameGroup(a: ChatMessage, b: ChatMessage) {
  return (
    a.isUser === b.isUser &&
    sameDay(a.createdAt, b.createdAt) &&
    Math.abs(a.createdAt.getTime() - b.createdAt.getTime()) < 120000
  );
}
