// 本文件用 Virtuoso 展示聊天历史，负责翻页、一次性回底、长消息展开与引用回看。

import { useCallback, useEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { Virtuoso } from 'react-virtuoso';
import type { VirtuosoHandle } from 'react-virtuoso';
import type { ChatController, ChatMessage } from '../../lib/ChatController.js';
import type { MessageQuote } from '../../lib/messageAttachments.js';
import { AssistantWorkingStatus } from './AssistantWorkingStatus.js';
import { ChatMessageEntry } from './ChatMessageEntry.js';
import { QuoteSelection, highlightQuote } from './QuoteSelection.js';

// 页头、页尾始终使用相同组件，翻页或思考状态改变时只更新内容。
type ListContext = { header: ReactNode; footer: ReactNode };
const components = {
  Header: ({ context }: { context: ListContext }) => (
    <div className="chat-list-header">{context.header}</div>
  ),
  Footer: ({ context }: { context: ListContext }) => (
    <div className="chat-list-footer">{context.footer}</div>
  ),
};

// Virtuoso 向上翻页需要递减 firstItemIndex；服务端按游标分页，提前留出历史索引空间。
const initialFirstItemIndex = 1_000_000_000;

// ChatMessageList 把尺寸与可见行交给 Virtuoso，只保存用户的导航和展开状态。
export function ChatMessageList({
  controller,
  onQuote,
  latestRequest,
}: {
  controller: ChatController;
  onQuote: (quote: MessageQuote) => void;
  latestRequest: number;
}) {
  const list = useRef<VirtuosoHandle>(null);
  const viewport = useRef<HTMLDivElement | null>(null);
  // 稳定的回调让 Virtuoso 只在滚动容器挂载和卸载时绑定监听。
  const bindViewport = useCallback((element: HTMLElement | Window | null) => {
    viewport.current = element instanceof HTMLDivElement ? element : null;
  }, []);
  const lastLatestRequest = useRef(latestRequest);
  const browsingHistory = useRef(false);
  const navigation = useRef<MessageQuote | null>(null);
  const clearHighlight = useRef<() => void>(() => {});
  const [quoteTarget, setQuoteTarget] = useState<MessageQuote | null>(null);
  const [quoteError, setQuoteError] = useState('');
  const [expandedMessages, setExpandedMessages] = useState(() => new Set<string>());
  const [atBottom, setAtBottom] = useState(true);

  // 控制器就地更新数组。快照让 Virtuoso 收到新数据，首批的锚点区分顶部插入与尾部追加。
  const messages = controller.messages.slice();
  const initialMessage = useRef(messages[0]?.id);
  initialMessage.current ??= messages[0]?.id;
  const prepended = messages.findIndex((message) => message.id === initialMessage.current);
  const firstItemIndex = initialFirstItemIndex - Math.max(0, prepended);

  // 回到底部只执行一次，并取消尚在翻页或等待高亮的引用。
  function scrollToBottom() {
    browsingHistory.current = false;
    navigation.current = null;
    setQuoteTarget(null);
    clearHighlight.current();
    list.current?.scrollToIndex({ index: 'LAST', align: 'end' });
    if (document.activeElement?.classList.contains('scroll-to-bottom')) {
      viewport.current?.focus({ preventScroll: true });
    }
  }

  // 从搜索返回最新聊天时，复用回底按钮的行为。
  useEffect(() => {
    if (latestRequest !== lastLatestRequest.current) {
      lastLatestRequest.current = latestRequest;
      scrollToBottom();
    }
  }, [latestRequest]);

  useEffect(
    () => () => {
      navigation.current = null;
      clearHighlight.current();
    },
    [],
  );

  // 引用可能指向更早的一页；先取到消息并展开正文，再滚到被引用的文字。
  async function navigateQuote(quote: MessageQuote) {
    const target = { ...quote };
    navigation.current = target;
    browsingHistory.current = false;
    clearHighlight.current();
    setQuoteError('');
    while (!controller.messages.some((message) => message.id === quote.message_id)) {
      const count = controller.messages.length;
      if (controller.hasMoreHistory && !controller.loadingHistory)
        await controller.loadHistory(true);
      if (navigation.current !== target) return;
      if (controller.messages.length === count) {
        setQuoteError('暂时找不到引用原文，请加载历史后重试');
        return;
      }
    }
    setExpandedMessages((current) => new Set(current).add(quote.message_id));
    setQuoteTarget(target);
  }

  // 滚动完成后目标行已挂载，此时可按引用的字符范围定位与高亮。
  useEffect(() => {
    if (!quoteTarget) return;
    const quote = quoteTarget;
    const index = controller.messages.findIndex((message) => message.id === quote.message_id);
    list.current?.scrollIntoView({
      index,
      align: 'center',
      done: () => {
        if (navigation.current !== quote) return;
        const element = viewport.current;
        const body = element?.querySelector<HTMLElement>(
          `[data-quote-body][data-message-id="${quote.message_id}"]`,
        );
        if (!element || !body) {
          setQuoteError('暂时找不到引用原文，请加载历史后重试');
          return;
        }
        const clear = highlightQuote(body, quote, element);
        const timer = setTimeout(clear, 3000);
        clearHighlight.current = () => {
          clearTimeout(timer);
          clear();
        };
      },
    });
  }, [controller, quoteTarget]);

  // 收起正在阅读的长消息时保留消息开头，其他尺寸变化由列表处理。
  function changeExpanded(message: ChatMessage, index: number, expanded: boolean) {
    const element = viewport.current;
    const row = element?.querySelector<HTMLElement>(`[data-chat-row="${message.id}"]`);
    if (
      !expanded &&
      element &&
      row &&
      row.getBoundingClientRect().top < element.getBoundingClientRect().top
    ) {
      list.current?.scrollToIndex({ index, align: 'start' });
    }
    setExpandedMessages((current) => {
      const next = new Set(current);
      if (expanded) next.add(message.id);
      else next.delete(message.id);
      return next;
    });
  }

  // 消息行只包含真实消息，页头和页尾状态不占用消息索引。
  const context: ListContext = {
    header: (
      <>
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
            onClick={() => void controller.loadHistory(true)}
          >
            {controller.loadingHistory ? '正在加载…' : '加载更早的消息'}
          </button>
        )}
      </>
    ),
    footer:
      controller.connected && controller.mainThinking ? (
        <AssistantWorkingStatus />
      ) : controller.mainFailed ? (
        <p className="error" role="alert">
          回复失败，请再发一条消息重试。
        </p>
      ) : null,
  };
  return (
    <>
      <Virtuoso
        ref={list}
        id="messages"
        className="messages"
        aria-label="聊天消息"
        scrollerRef={bindViewport}
        data={messages}
        components={components}
        context={context}
        computeItemKey={(_, message) => message.id}
        firstItemIndex={firstItemIndex}
        initialTopMostItemIndex={{ index: 'LAST', align: 'end' }}
        atBottomThreshold={160}
        atBottomStateChange={setAtBottom}
        onWheel={() => {
          browsingHistory.current = true;
        }}
        onTouchStart={() => {
          browsingHistory.current = true;
        }}
        onKeyDown={() => {
          browsingHistory.current = true;
        }}
        startReached={() => {
          if (browsingHistory.current) void controller.loadHistory(true);
        }}
        itemContent={(itemIndex, message) => {
          const index = itemIndex - firstItemIndex;
          return (
            <div className="chat-list-row" data-chat-row={message.id}>
              <ChatMessageEntry
                messages={messages}
                index={index}
                expanded={expandedMessages.has(message.id)}
                onExpandedChange={(expanded) => changeExpanded(message, index, expanded)}
                onQuoteNavigate={(quote) => void navigateQuote(quote)}
              />
            </div>
          );
        }}
      />
      <QuoteSelection container={viewport} onQuote={onQuote} />
      {!atBottom && (
        <button
          type="button"
          className="scroll-to-bottom"
          aria-label="滚动到底部"
          title="滚动到底部"
          onPointerDown={(event) => event.preventDefault()}
          onClick={scrollToBottom}
        >
          <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <path
              d="M12 5v14m-6-6 6 6 6-6"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </button>
      )}
    </>
  );
}
