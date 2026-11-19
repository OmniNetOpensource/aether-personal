// 本文件用 Virtuoso 展示聊天历史，负责翻页、贴底回收旧历史、一次性回底、长消息展开、引用回看与分享多选勾选。

import { useCallback, useEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { Virtuoso } from 'react-virtuoso';
import type { VirtuosoHandle } from 'react-virtuoso';
import { latestMessageWindow } from '../../lib/ChatController.js';
import type { ChatController, ChatMessage } from '../../lib/ChatController.js';
import type { MessageQuote } from '../../lib/messageAttachments.js';
import { AssistantWorkingStatus } from './AssistantWorkingStatus.js';
import { ChatMessageEntry } from './ChatMessageEntry.js';
import { QuoteSelection, highlightQuote } from './QuoteSelection.js';
import { useVirtuosoFirstItemIndex } from './useVirtuosoFirstItemIndex.js';

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

// ChatMessageList 把尺寸与可见行交给 Virtuoso，只保存用户的导航、贴底回收和展开状态。
export function ChatMessageList({
  controller,
  onQuote,
  latestRequest,
  selectionMode = false,
  selectedIDs,
  onToggleSelect,
}: {
  controller: ChatController;
  onQuote: (quote: MessageQuote) => void;
  latestRequest: number;
  selectionMode?: boolean;
  selectedIDs?: ReadonlySet<string>;
  onToggleSelect?: (messageID: string, shiftKey: boolean) => void;
}) {
  const list = useRef<VirtuosoHandle>(null);
  const viewport = useRef<HTMLDivElement | null>(null);
  // 稳定的回调让 Virtuoso 只在滚动容器挂载和卸载时绑定监听。
  const bindViewport = useCallback((element: HTMLElement | Window | null) => {
    viewport.current = element instanceof HTMLDivElement ? element : null;
  }, []);
  const lastLatestRequest = useRef(latestRequest);
  const browsingHistory = useRef(false);
  const atBottomRef = useRef(true);
  const navigation = useRef<MessageQuote | null>(null);
  const clearHighlight = useRef<() => void>(() => {});
  const [quoteTarget, setQuoteTarget] = useState<MessageQuote | null>(null);
  const [quoteError, setQuoteError] = useState('');
  const [navigatingQuote, setNavigatingQuote] = useState(false);
  const [pinOlderHistory, setPinOlderHistory] = useState(false);
  const [expandedMessages, setExpandedMessages] = useState(() => new Set<string>());
  const [atBottom, setAtBottom] = useState(true);
  const [isScrolling, setIsScrolling] = useState(false);

  // 控制器就地更新数组。快照让 Virtuoso 收到新数据，firstItemIndex 兼顾向上翻页与头部裁剪。
  const messages = controller.messages.slice();
  const firstItemIndex = useVirtuosoFirstItemIndex(messages);

  // 离开底部后解除“贴底手动翻页”保护，后续回到底部时即可回收之前翻出的旧历史。
  const handleAtBottomChange = useCallback((nextAtBottom: boolean) => {
    atBottomRef.current = nextAtBottom;
    setAtBottom(nextAtBottom);
    if (!nextAtBottom) setPinOlderHistory(false);
  }, []);

  // 屏幕足够高时点击翻页可能仍在底部阈值内，临时锁定避免刚拉回的历史被立即裁掉。
  function loadOlderHistory() {
    if (atBottomRef.current) setPinOlderHistory(true);
    void controller.loadHistory(true);
  }

  // 停在最新底部且不处于翻页、引用跳转或分享多选时，回收头部超出窗口的旧历史及对应展开记录。
  useEffect(() => {
    if (
      !atBottom ||
      isScrolling ||
      selectionMode ||
      navigatingQuote ||
      pinOlderHistory ||
      controller.loadingHistory ||
      messages.length <= latestMessageWindow
    ) {
      return;
    }
    const removed = controller.trimOlderMessages(latestMessageWindow);
    if (removed === 0) return;
    const keptIDs = new Set(controller.messages.map((message) => message.id));
    setExpandedMessages((current) => {
      if (current.size === 0) return current;
      let changed = false;
      const next = new Set<string>();
      for (const id of current) {
        if (keptIDs.has(id)) next.add(id);
        else changed = true;
      }
      return changed ? next : current;
    });
  }, [
    atBottom,
    isScrolling,
    selectionMode,
    navigatingQuote,
    pinOlderHistory,
    controller,
    controller.loadingHistory,
    messages.length,
  ]);

  // 回到底部只执行一次，并取消尚在翻页或等待高亮的引用。
  function scrollToBottom() {
    browsingHistory.current = false;
    navigation.current = null;
    setNavigatingQuote(false);
    setPinOlderHistory(false);
    setQuoteTarget(null);
    clearHighlight.current();
    if (messages.length === 0) return;
    // 先挂载末条消息，等它量完高度再对齐实际页底。用户开始上翻或回看引用时让出滚动控制。
    list.current?.scrollIntoView({
      index: messages.length - 1,
      calculateViewLocation: () => ({ index: 'LAST', align: 'end' }),
      done: () => {
        const element = viewport.current;
        if (element && !browsingHistory.current && !navigation.current) {
          element.scrollTop = element.scrollHeight;
        }
      },
    });
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
    setNavigatingQuote(true);
    clearHighlight.current();
    setQuoteError('');
    while (!controller.messages.some((message) => message.id === quote.message_id)) {
      const count = controller.messages.length;
      if (controller.hasMoreHistory && !controller.loadingHistory)
        await controller.loadHistory(true);
      if (navigation.current !== target) return;
      if (controller.messages.length === count) {
        setNavigatingQuote(false);
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
        setNavigatingQuote(false);
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
            onClick={loadOlderHistory}
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
        atBottomStateChange={handleAtBottomChange}
        isScrolling={setIsScrolling}
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
          if (browsingHistory.current) loadOlderHistory();
        }}
        itemContent={(itemIndex, message) => {
          const index = itemIndex - firstItemIndex;
          const entry = (
            <ChatMessageEntry
              messages={messages}
              index={index}
              expanded={expandedMessages.has(message.id)}
              onExpandedChange={(expanded) => changeExpanded(message, index, expanded)}
              onQuoteNavigate={(quote) => void navigateQuote(quote)}
            />
          );
          if (!selectionMode) {
            return (
              <div className="chat-list-row" data-chat-row={message.id}>
                {entry}
              </div>
            );
          }
          const selected = selectedIDs?.has(message.id) === true;
          return (
            <div
              className={`chat-list-row is-selectable${selected ? ' is-selected' : ''}`}
              data-chat-row={message.id}
              onClick={(event) => {
                if (event.target instanceof Element && event.target.closest('button, a, dialog')) {
                  return;
                }
                onToggleSelect?.(message.id, event.shiftKey);
              }}
            >
              <button
                type="button"
                className="share-select-check"
                role="checkbox"
                aria-checked={selected}
                aria-label={selected ? '取消选中此消息' : '选中此消息用于分享'}
                onClick={(event) => {
                  event.stopPropagation();
                  onToggleSelect?.(message.id, event.shiftKey);
                }}
              >
                {selected && (
                  <svg viewBox="0 0 24 24" aria-hidden="true">
                    <path d="m5 12.5 4.5 4.5L19 7.5" />
                  </svg>
                )}
              </button>
              <div className="chat-row-content">{entry}</div>
            </div>
          );
        }}
      />
      {!selectionMode && <QuoteSelection container={viewport} onQuote={onQuote} />}
      {!atBottom && (
        <button
          type="button"
          className="scroll-to-bottom"
          aria-label="滚动到底部"
          title="滚动到底部"
          onPointerDown={(event) => {
            // 鼠标按下时保留输入焦点；WebKit 需要触摸的默认行为才能随后发出 click。
            if (event.pointerType === 'mouse') event.preventDefault();
          }}
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
