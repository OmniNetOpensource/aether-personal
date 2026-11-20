// 本文件用 Virtuoso 展示聊天历史，负责翻页、贴底跟随与回收旧历史、平滑回底、长消息展开、引用回看与分享多选勾选。

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { Virtuoso } from 'react-virtuoso';
import type { VirtuosoHandle } from 'react-virtuoso';
import { latestMessageWindow } from '../../lib/ChatController.js';
import type { ChatController, ChatMessage } from '../../lib/ChatController.js';
import type { MessageQuote } from '../../lib/messageAttachments.js';
import { AssistantWorkingStatus } from './AssistantWorkingStatus.js';
import { ChatMessageEntry, getMessageGroupState } from './ChatMessageEntry.js';
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

// 固定引用避免 Virtuoso 因内联对象或函数在每次渲染时重算列表状态。
const initialBottomLocation = { index: 'LAST' as const, align: 'end' as const };
const viewportOverscan = { top: 800, bottom: 800 };
const defaultMessageRowHeight = 120;
const computeMessageKey = (_: number, message: ChatMessage) => message.id;

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
  const viewportObserver = useRef<ResizeObserver | null>(null);
  const lastLatestRequest = useRef(latestRequest);
  const browsingHistory = useRef(false);
  const atBottomRef = useRef(true);
  const isScrollingRef = useRef(false);
  const bottomScrollFrame = useRef(0);
  const bottomScrollActive = useRef(false);
  const trimTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const scheduleTrimRef = useRef<() => void>(() => {});
  const navigation = useRef<MessageQuote | null>(null);
  const clearHighlight = useRef<() => void>(() => {});
  const [quoteTarget, setQuoteTarget] = useState<MessageQuote | null>(null);
  const [quoteError, setQuoteError] = useState('');
  const [navigatingQuote, setNavigatingQuote] = useState(false);
  const [pinOlderHistory, setPinOlderHistory] = useState(false);
  const [expandedMessages, setExpandedMessages] = useState(() => new Set<string>());
  const [atBottom, setAtBottom] = useState(true);

  // 控制器在消息变更时替换数组引用；未变更时直接复用同一引用，firstItemIndex 兼顾向上翻页与头部裁剪。
  const messages = controller.messages;
  const firstItemIndex = useVirtuosoFirstItemIndex(messages);
  const previousFirstItemIndex = useRef(firstItemIndex);

  // Virtuoso 裁剪头部旧消息时同步调用了 scrollBy，但浏览器 scroll 事件要到下一帧才触发；
  // 在绘制前通过微任务补发一次 scroll 事件，让 Virtuoso 立即更新 visibleRange，消除单帧空白闪动。
  useLayoutEffect(() => {
    const previous = previousFirstItemIndex.current;
    previousFirstItemIndex.current = firstItemIndex;
    if (firstItemIndex > previous) {
      queueMicrotask(() => {
        viewport.current?.dispatchEvent(new Event('scroll'));
      });
    }
  }, [firstItemIndex]);

  // 输入框多行增高或移动键盘弹出缩窄视口时，若原本已在底部则同步保持贴底。
  const bindViewport = useCallback((element: HTMLElement | Window | null) => {
    viewportObserver.current?.disconnect();
    viewportObserver.current = null;
    const scroller = element instanceof HTMLDivElement ? element : null;
    viewport.current = scroller;
    if (!scroller) return;
    let lastClientHeight = scroller.clientHeight;
    let lastFooterHeight =
      scroller.querySelector<HTMLElement>('.chat-list-footer')?.offsetHeight ?? 0;
    const observer = new ResizeObserver(() => {
      const nextClientHeight = scroller.clientHeight;
      const footer = scroller.querySelector<HTMLElement>('.chat-list-footer');
      const nextFooterHeight = footer?.offsetHeight ?? 0;
      const viewportShrank = nextClientHeight < lastClientHeight;
      const footerGrew = nextFooterHeight > lastFooterHeight;
      lastClientHeight = nextClientHeight;
      lastFooterHeight = nextFooterHeight;
      if (
        (viewportShrank || footerGrew) &&
        atBottomRef.current &&
        !isScrollingRef.current &&
        !bottomScrollActive.current &&
        !navigation.current
      ) {
        scroller.scrollTop = scroller.scrollHeight;
      }
    });
    observer.observe(scroller);
    const footer = scroller.querySelector<HTMLElement>('.chat-list-footer');
    if (footer) observer.observe(footer);
    viewportObserver.current = observer;
  }, []);

  // 停在最新底部且滚动与回底动画均静止后，延后回收头部超出窗口的旧历史及对应展开记录。
  const scheduleTrim = useCallback(() => {
    clearTimeout(trimTimer.current);
    if (
      !atBottomRef.current ||
      isScrollingRef.current ||
      bottomScrollActive.current ||
      selectionMode ||
      navigatingQuote ||
      pinOlderHistory ||
      controller.loadingHistory ||
      controller.messages.length <= latestMessageWindow
    ) {
      return;
    }
    trimTimer.current = setTimeout(() => {
      if (
        !atBottomRef.current ||
        isScrollingRef.current ||
        bottomScrollActive.current ||
        selectionMode ||
        navigatingQuote ||
        pinOlderHistory ||
        controller.loadingHistory ||
        controller.messages.length <= latestMessageWindow
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
    }, 180);
  }, [controller, controller.loadingHistory, navigatingQuote, pinOlderHistory, selectionMode]);
  scheduleTrimRef.current = scheduleTrim;

  useEffect(() => {
    scheduleTrim();
    return () => clearTimeout(trimTimer.current);
  }, [scheduleTrim, messages.length, atBottom]);

  // 离开底部后解除“贴底手动翻页”保护，后续回到底部时即可回收之前翻出的旧历史。
  const handleAtBottomChange = useCallback((nextAtBottom: boolean) => {
    if (bottomScrollActive.current && !nextAtBottom) return;
    atBottomRef.current = nextAtBottom;
    setAtBottom(nextAtBottom);
    if (!nextAtBottom) setPinOlderHistory(false);
    else scheduleTrimRef.current();
  }, []);

  // Virtuoso 滚动启停只更新 ref 并触发空闲裁剪检查，不在每次滚动首尾触发整表 React 重渲染。
  const handleIsScrolling = useCallback((scrolling: boolean) => {
    isScrollingRef.current = scrolling;
    if (!scrolling) scheduleTrimRef.current();
  }, []);

  // 用户主动滚轮、触摸或按键时立即接管滚动控制，中止进行中的平滑回底动画。
  const handleUserScrollStart = useCallback(() => {
    browsingHistory.current = true;
    if (bottomScrollFrame.current !== 0) {
      cancelAnimationFrame(bottomScrollFrame.current);
      bottomScrollFrame.current = 0;
      bottomScrollActive.current = false;
    }
  }, []);

  // 屏幕足够高时点击翻页可能仍在底部阈值内，临时锁定避免刚拉回的历史被立即裁掉。
  const loadOlderHistory = useCallback(() => {
    if (atBottomRef.current) setPinOlderHistory(true);
    void controller.loadHistory(true);
  }, [controller]);

  const handleStartReached = useCallback(() => {
    if (browsingHistory.current) loadOlderHistory();
  }, [loadOlderHistory]);

  // animateToBottom 使用单段 ease-out 动画滑向实时页底：远距离先跳到贴近底部的缓冲区间再平滑落底，避免沿途挂载大量旧消息或两段式停顿。
  const animateToBottom = useCallback(
    (allowJumpFromFar: boolean) => {
      cancelAnimationFrame(bottomScrollFrame.current);
      bottomScrollFrame.current = 0;
      if (controller.messages.length === 0) return;

      const element = viewport.current;
      if (!element || matchMedia('(prefers-reduced-motion: reduce)').matches) {
        bottomScrollActive.current = false;
        atBottomRef.current = true;
        setAtBottom(true);
        list.current?.scrollToIndex(initialBottomLocation);
        requestAnimationFrame(() => {
          const scroller = viewport.current;
          if (scroller && !browsingHistory.current && !navigation.current) {
            scroller.scrollTop = scroller.scrollHeight;
          }
          scheduleTrimRef.current();
        });
        return;
      }

      bottomScrollActive.current = true;
      atBottomRef.current = true;
      setAtBottom(true);

      const initialMaxTop = Math.max(0, element.scrollHeight - element.clientHeight);
      const initialDistance = initialMaxTop - element.scrollTop;
      if (allowJumpFromFar && initialDistance > element.clientHeight * 1.5) {
        const glideSpan = Math.min(element.clientHeight * 0.85, 640);
        element.scrollTop = Math.max(0, initialMaxTop - glideSpan);
      }

      const startTop = element.scrollTop;
      const durationMs = allowJumpFromFar ? 260 : 220;
      const startTime = performance.now();

      const step = (now: number) => {
        if (!viewport.current || browsingHistory.current || navigation.current) {
          bottomScrollFrame.current = 0;
          bottomScrollActive.current = false;
          scheduleTrimRef.current();
          return;
        }
        const liveTarget = Math.max(0, element.scrollHeight - element.clientHeight);
        const progress = Math.min(1, (now - startTime) / durationMs);
        const eased = 1 - Math.pow(1 - progress, 3);
        element.scrollTop = startTop + (liveTarget - startTop) * eased;
        if (progress < 1) {
          bottomScrollFrame.current = requestAnimationFrame(step);
        } else {
          bottomScrollFrame.current = requestAnimationFrame(() => {
            bottomScrollFrame.current = 0;
            bottomScrollActive.current = false;
            if (!browsingHistory.current && !navigation.current) {
              element.scrollTop = element.scrollHeight;
            }
            scheduleTrimRef.current();
          });
        }
      };

      bottomScrollFrame.current = requestAnimationFrame(step);
    },
    [controller],
  );

  // 回到底部按钮取消尚在翻页或等待高亮的引用，并平滑滑向最新消息底部。
  const scrollToBottom = useCallback(() => {
    browsingHistory.current = false;
    navigation.current = null;
    setNavigatingQuote(false);
    setPinOlderHistory(false);
    setQuoteTarget(null);
    clearHighlight.current();
    animateToBottom(true);
    if (document.activeElement?.classList.contains('scroll-to-bottom')) {
      viewport.current?.focus({ preventScroll: true });
    }
  }, [animateToBottom]);

  // 人已经贴在底部时，新消息插入或思考气泡出现自动平滑跟到底部；正在看更早消息时保持原地不动。
  const tailMessageID = messages.at(-1)?.id;
  const footerActive = (controller.connected && controller.mainThinking) || controller.mainFailed;
  const previousTailMessageID = useRef<string | undefined>(tailMessageID);
  const previousFooterActive = useRef(footerActive);
  useEffect(() => {
    const previousTail = previousTailMessageID.current;
    const previousFooter = previousFooterActive.current;
    previousTailMessageID.current = tailMessageID;
    previousFooterActive.current = footerActive;

    const tailAppended = Boolean(previousTail && tailMessageID && tailMessageID !== previousTail);
    const footerAppeared = footerActive && !previousFooter;
    if (
      (tailAppended || footerAppeared) &&
      atBottomRef.current &&
      !navigation.current &&
      !selectionMode
    ) {
      browsingHistory.current = false;
      animateToBottom(false);
    }
  }, [tailMessageID, footerActive, selectionMode, animateToBottom]);

  // 从搜索返回最新聊天时，复用回底按钮的行为。
  useEffect(() => {
    if (latestRequest !== lastLatestRequest.current) {
      lastLatestRequest.current = latestRequest;
      scrollToBottom();
    }
  }, [latestRequest, scrollToBottom]);

  useEffect(
    () => () => {
      navigation.current = null;
      clearHighlight.current();
      clearTimeout(trimTimer.current);
      cancelAnimationFrame(bottomScrollFrame.current);
      viewportObserver.current?.disconnect();
    },
    [],
  );

  // 引用可能指向更早的一页；先取到消息并展开正文，再滚到被引用的文字。
  const navigateQuote = useCallback(
    async (quote: MessageQuote) => {
      const target = { ...quote };
      navigation.current = target;
      browsingHistory.current = false;
      cancelAnimationFrame(bottomScrollFrame.current);
      bottomScrollFrame.current = 0;
      bottomScrollActive.current = false;
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
    },
    [controller],
  );

  const handleQuoteNavigate = useCallback(
    (quote: MessageQuote) => {
      void navigateQuote(quote);
    },
    [navigateQuote],
  );

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

  // 展开状态按消息 ID 记录；收起长消息时的滚动锚定由 CollapsibleMessage 通过 handleScrollBy 同帧闭环完成。
  const handleExpandedChange = useCallback((messageID: string, expanded: boolean) => {
    setExpandedMessages((current) => {
      if (current.has(messageID) === expanded) return current;
      const next = new Set(current);
      if (expanded) next.add(messageID);
      else next.delete(messageID);
      return next;
    });
  }, []);

  const handleScrollBy = useCallback((top: number) => {
    list.current?.scrollBy({ top });
  }, []);

  // 消息行只包含真实消息，页头和页尾状态不占用消息索引。
  const hasMessages = messages.length > 0;
  const context = useMemo<ListContext>(
    () => ({
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
          {controller.hasMoreHistory && hasMessages && (
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
    }),
    [
      controller,
      controller.connected,
      controller.error,
      controller.hasMoreHistory,
      controller.loadingHistory,
      controller.mainFailed,
      controller.mainThinking,
      hasMessages,
      loadOlderHistory,
      quoteError,
    ],
  );

  const renderItemContent = useCallback(
    (itemIndex: number, message: ChatMessage) => {
      const index = itemIndex - firstItemIndex;
      const { showTime, joinedWithPrevious, endsGroup } = getMessageGroupState(messages, index);
      const entry = (
        <ChatMessageEntry
          message={message}
          showTime={showTime}
          joinedWithPrevious={joinedWithPrevious}
          endsGroup={endsGroup}
          expanded={expandedMessages.has(message.id)}
          onExpandedChange={handleExpandedChange}
          onQuoteNavigate={handleQuoteNavigate}
          onScrollBy={handleScrollBy}
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
    },
    [
      expandedMessages,
      firstItemIndex,
      handleExpandedChange,
      handleQuoteNavigate,
      handleScrollBy,
      messages,
      onToggleSelect,
      selectedIDs,
      selectionMode,
    ],
  );

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
        computeItemKey={computeMessageKey}
        firstItemIndex={firstItemIndex}
        initialTopMostItemIndex={initialBottomLocation}
        defaultItemHeight={defaultMessageRowHeight}
        increaseViewportBy={viewportOverscan}
        atBottomThreshold={160}
        atBottomStateChange={handleAtBottomChange}
        isScrolling={handleIsScrolling}
        onWheel={handleUserScrollStart}
        onTouchStart={handleUserScrollStart}
        onKeyDown={handleUserScrollStart}
        startReached={handleStartReached}
        itemContent={renderItemContent}
      />
      {!selectionMode && <QuoteSelection container={viewport} onQuote={onQuote} />}
      <button
        type="button"
        className={`scroll-to-bottom${atBottom ? ' is-hidden' : ''}`}
        aria-label="滚动到底部"
        title="滚动到底部"
        aria-hidden={atBottom}
        tabIndex={atBottom ? -1 : 0}
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
    </>
  );
}
