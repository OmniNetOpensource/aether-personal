// 本文件用 TanStack Virtual 展示聊天历史，负责翻页、贴底跟随与回收旧历史、回底、长消息展开收起锚定和引用回看。

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { useVirtualizer } from '@tanstack/react-virtual';
import { latestMessageWindow } from '../../lib/ChatController.js';
import type { ChatController, ChatMessage } from '../../lib/ChatController.js';
import type { MessageQuote } from '../../lib/messageAttachments.js';
import { ChatMessageEntry, getMessageGroupState } from './ChatMessageEntry.js';
import { QuoteSelection, highlightQuote } from './QuoteSelection.js';

const defaultMessageRowHeight = 120;
const virtualOverscanCount = 6;
const atBottomThreshold = 160;
const pinnedBottomThreshold = 4;

// ChatMessageList 由 TanStack Virtual 计算可见行与偏移，并按展开、收起与贴底场景显式控制尺寸变化的滚动补偿。
export function ChatMessageList({
  controller,
  onQuote,
  latestRequest,
}: {
  controller: ChatController;
  onQuote: (quote: MessageQuote) => void;
  latestRequest: number;
}) {
  const viewport = useRef<HTMLDivElement | null>(null);
  const viewportObserver = useRef<ResizeObserver | null>(null);
  const lastLatestRequest = useRef(latestRequest);
  const initialPositioned = useRef(false);
  const browsingHistory = useRef(false);
  const atBottomRef = useRef(true);
  const isScrollingRef = useRef(false);
  const lastStartReachedID = useRef<string | null>(null);
  const trimTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const scheduleTrimRef = useRef<() => void>(() => {});
  const loadOlderRef = useRef<() => void>(() => {});
  const scrollToEndRef = useRef<() => void>(() => {});
  const navigation = useRef<MessageQuote | null>(null);
  const clearHighlight = useRef<() => void>(() => {});
  const pendingToggle = useRef<{ id: string; action: 'expand' | 'collapse' } | null>(null);
  const [headerHeight, setHeaderHeight] = useState(0);
  const [quoteTarget, setQuoteTarget] = useState<MessageQuote | null>(null);
  const [quoteError, setQuoteError] = useState('');
  const [navigatingQuote, setNavigatingQuote] = useState(false);
  const [pinOlderHistory, setPinOlderHistory] = useState(false);
  const [expandedMessages, setExpandedMessages] = useState(() => new Set<string>());
  const [atBottom, setAtBottom] = useState(true);

  const messages = controller.messages;
  const messagesRef = useRef(messages);
  messagesRef.current = messages;

  // anchorTo: 'end' 自动按消息 key 保持头部翻页与头部回收的阅读位置；
  // directDomUpdates 在 ResizeObserver 同帧直接更新容器高度与各行 top，避免展开时和下一条消息重叠一帧；
  // scrollEndThreshold: -1 把全部高度变化的滚动补偿决策收口到下方的 shouldAdjustScrollPositionOnItemSizeChange。
  const virtualizer = useVirtualizer({
    count: messages.length,
    getScrollElement: () => viewport.current,
    estimateSize: () => defaultMessageRowHeight,
    getItemKey: (index) => messages[index].id,
    overscan: virtualOverscanCount,
    scrollMargin: headerHeight,
    anchorTo: 'end',
    followOnAppend: false,
    scrollEndThreshold: -1,
    directDomUpdates: true,
    directDomUpdatesMode: 'position',
    onChange: (instance) => {
      isScrollingRef.current = instance.isScrolling;
      if (!instance.isScrolling) scheduleTrimRef.current();
      if (!instance.scrollElement || !initialPositioned.current) return;
      const nextAtBottom = instance.isAtEnd(atBottomThreshold);
      if (nextAtBottom !== atBottomRef.current) {
        atBottomRef.current = nextAtBottom;
        setAtBottom(nextAtBottom);
        if (!nextAtBottom) setPinOlderHistory(false);
        else scheduleTrimRef.current();
      }
      const range = instance.range;
      if (!range) return;
      if (range.startIndex > 0) {
        lastStartReachedID.current = null;
        return;
      }
      const firstMessage = messagesRef.current[0];
      if (
        browsingHistory.current &&
        firstMessage &&
        lastStartReachedID.current !== firstMessage.id
      ) {
        lastStartReachedID.current = firstMessage.id;
        loadOlderRef.current();
      }
    },
  });

  // 显式控制单条消息尺寸变化时是否调整 scrollTop：
  // 展开锁行顶（不补偿）、收起锁按钮与下方内容（同步补偿）、停在最新底部时继续贴底，其余沿用视口上方补偿规则。
  useLayoutEffect(() => {
    virtualizer.shouldAdjustScrollPositionOnItemSizeChange = (item, _delta, instance) => {
      if (pendingToggle.current?.id === item.key) {
        const { action } = pendingToggle.current;
        pendingToggle.current = null;
        return action === 'collapse';
      }
      if (
        instance.isAtEnd(pinnedBottomThreshold) &&
        !browsingHistory.current &&
        !navigation.current
      ) {
        return true;
      }
      const scrollOffset = instance.scrollOffset ?? 0;
      const isFirstMeasure = !instance.itemSizeCache.has(item.key);
      return isFirstMeasure
        ? item.start < scrollOffset
        : item.end <= scrollOffset && instance.scrollDirection !== 'backward';
    };
  }, [virtualizer]);

  // 首次有消息（或清空后重新加载）时直接定位到最新底部。
  useLayoutEffect(() => {
    if (messages.length === 0) {
      initialPositioned.current = false;
      return;
    }
    if (initialPositioned.current) return;
    initialPositioned.current = true;
    virtualizer.scrollToEnd({ behavior: 'auto' });
  }, [messages.length, virtualizer]);

  // 追踪页头高度作为虚拟列表 scrollMargin；输入框增高或移动键盘缩窄视口时，若原本已在底部则同步保持贴底。
  const bindViewport = useCallback((scroller: HTMLDivElement | null) => {
    viewportObserver.current?.disconnect();
    viewportObserver.current = null;
    viewport.current = scroller;
    if (!scroller) return;
    const header = scroller.querySelector<HTMLElement>('.chat-list-header');
    const footer = scroller.querySelector<HTMLElement>('.chat-list-footer');
    let lastClientHeight = scroller.clientHeight;
    let lastHeaderHeight = header?.offsetHeight ?? 0;
    let lastFooterHeight = footer?.offsetHeight ?? 0;
    setHeaderHeight(lastHeaderHeight);
    const observer = new ResizeObserver(() => {
      const nextClientHeight = scroller.clientHeight;
      const nextHeaderHeight = header?.offsetHeight ?? 0;
      const nextFooterHeight = footer?.offsetHeight ?? 0;
      const viewportShrank = nextClientHeight < lastClientHeight;
      const footerGrew = nextFooterHeight > lastFooterHeight;
      lastClientHeight = nextClientHeight;
      lastFooterHeight = nextFooterHeight;
      if (nextHeaderHeight !== lastHeaderHeight) {
        lastHeaderHeight = nextHeaderHeight;
        setHeaderHeight(nextHeaderHeight);
      }
      if (
        (viewportShrank || footerGrew) &&
        atBottomRef.current &&
        !isScrollingRef.current &&
        !navigation.current
      ) {
        scrollToEndRef.current();
      }
    });
    observer.observe(scroller);
    if (header) observer.observe(header);
    if (footer) observer.observe(footer);
    viewportObserver.current = observer;
  }, []);

  // 停在最新底部且滚动静止后，延后回收头部超出窗口的旧历史及对应展开记录。
  const scheduleTrim = useCallback(() => {
    clearTimeout(trimTimer.current);
    const canTrim = () =>
      atBottomRef.current &&
      !isScrollingRef.current &&
      !navigatingQuote &&
      !pinOlderHistory &&
      !controller.loadingHistory &&
      controller.messages.length > latestMessageWindow;
    if (!canTrim()) return;
    trimTimer.current = setTimeout(() => {
      if (!canTrim()) return;
      const removed = controller.trimOlderMessages(latestMessageWindow);
      if (removed === 0) return;
      lastStartReachedID.current = null;
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
  }, [controller, controller.loadingHistory, navigatingQuote, pinOlderHistory]);
  scheduleTrimRef.current = scheduleTrim;

  useEffect(() => {
    scheduleTrim();
    return () => clearTimeout(trimTimer.current);
  }, [scheduleTrim, messages.length, atBottom]);

  // 用户主动滚轮、触摸或按键时标记正在浏览历史，允许顶部的向上加载触发。
  const handleUserScrollStart = useCallback(() => {
    browsingHistory.current = true;
  }, []);

  // 屏幕足够高时点击翻页可能仍在底部阈值内，临时锁定避免刚拉回的历史被立即裁掉。
  const loadOlderHistory = useCallback(() => {
    if (atBottomRef.current) setPinOlderHistory(true);
    void controller.loadHistory(true);
  }, [controller]);
  loadOlderRef.current = loadOlderHistory;

  // scrollToEnd 直接跳到最新消息底部，由虚拟列表在底部行测高收敛时保持贴底。
  const scrollToEnd = useCallback(() => {
    if (controller.messages.length === 0) return;
    virtualizer.scrollToEnd({ behavior: 'auto' });
  }, [controller, virtualizer]);
  scrollToEndRef.current = scrollToEnd;

  // 回到底部按钮取消尚在翻页或等待高亮的引用，并立即跳到最新消息底部。
  const scrollToBottom = useCallback(() => {
    browsingHistory.current = false;
    navigation.current = null;
    setNavigatingQuote(false);
    setPinOlderHistory(false);
    setQuoteTarget(null);
    clearHighlight.current();
    scrollToEnd();
    if (document.activeElement?.classList.contains('scroll-to-bottom')) {
      viewport.current?.focus({ preventScroll: true });
    }
  }, [scrollToEnd]);

  // 人已经贴在底部时，新消息插入或思考气泡出现立即跟到底部；正在看更早消息时保持原地不动。
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
    if ((tailAppended || footerAppeared) && atBottomRef.current && !navigation.current) {
      browsingHistory.current = false;
      scrollToEnd();
    }
  }, [tailMessageID, footerActive, scrollToEnd]);

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

  // 先将引用所在消息滚进视口居中，再等待该行挂载并完成高亮定位。
  useEffect(() => {
    if (!quoteTarget) return;
    const index = controller.messages.findIndex((message) => message.id === quoteTarget.message_id);
    if (index < 0) {
      setNavigatingQuote(false);
      setQuoteTarget(null);
      setQuoteError('暂时找不到引用原文，请加载历史后重试');
      return;
    }
    virtualizer.scrollToIndex(index, { align: 'center', behavior: 'auto' });
  }, [controller, quoteTarget, virtualizer]);

  const virtualItems = virtualizer.getVirtualItems();
  const quoteTargetMounted = Boolean(
    quoteTarget && virtualItems.some((item) => item.key === quoteTarget.message_id),
  );

  // 目标行挂载并完成首帧测高后停止虚拟列表的自动收敛，再按引用字符范围精确居中与高亮。
  useEffect(() => {
    if (!quoteTarget || !quoteTargetMounted) return;
    const quote = quoteTarget;
    const frame = requestAnimationFrame(() => {
      if (navigation.current !== quote) return;
      virtualizer.cancelScroll();
      setNavigatingQuote(false);
      setQuoteTarget(null);
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
    });
    return () => cancelAnimationFrame(frame);
  }, [quoteTarget, quoteTargetMounted, virtualizer]);

  // 记录用户主动切换展开或收起的目标消息，供 TanStack Virtual 同帧决定是否补偿 scrollTop。
  const handleExpandedChange = useCallback((messageID: string, expanded: boolean) => {
    const toggle = { id: messageID, action: expanded ? ('expand' as const) : ('collapse' as const) };
    pendingToggle.current = toggle;
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        if (pendingToggle.current === toggle) pendingToggle.current = null;
      });
    });
    setExpandedMessages((current) => {
      if (current.has(messageID) === expanded) return current;
      const next = new Set(current);
      if (expanded) next.add(messageID);
      else next.delete(messageID);
      return next;
    });
  }, []);

  const hasMessages = messages.length > 0;

  return (
    <>
      <div
        ref={bindViewport}
        id="messages"
        className="messages"
        aria-label="聊天消息"
        tabIndex={0}
        onWheel={handleUserScrollStart}
        onTouchStart={handleUserScrollStart}
        onKeyDown={handleUserScrollStart}
      >
        <div className="chat-list-header">
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
        </div>
        <div
          ref={virtualizer.containerRef}
          className="chat-list-items"
          style={{ position: 'relative', width: '100%' }}
        >
          {virtualItems.map((virtualItem) => {
            const message = messages[virtualItem.index];
            const { showTime, joinedWithPrevious, endsGroup } = getMessageGroupState(
              messages,
              virtualItem.index,
            );
            return (
              <div
                key={virtualItem.key}
                data-index={virtualItem.index}
                ref={virtualizer.measureElement}
                style={{
                  position: 'absolute',
                  left: 0,
                  width: '100%',
                }}
              >
                <div className="chat-list-row" data-chat-row={message.id}>
                  <ChatMessageEntry
                    message={message}
                    showTime={showTime}
                    joinedWithPrevious={joinedWithPrevious}
                    endsGroup={endsGroup}
                    expanded={expandedMessages.has(message.id)}
                    onExpandedChange={handleExpandedChange}
                    onQuoteNavigate={handleQuoteNavigate}
                  />
                </div>
              </div>
            );
          })}
        </div>
        <div className="chat-list-footer">
          {controller.connected && controller.mainThinking ? (
            <div className="thinking" role="status" aria-label="aether 正在处理">
              <div className="thinking-dots" aria-hidden="true">
                <span className="dot" />
                <span className="dot" />
                <span className="dot" />
              </div>
            </div>
          ) : controller.mainFailed ? (
            <p className="error" role="alert">
              回复失败，请再发一条消息重试。
            </p>
          ) : null}
        </div>
      </div>
      <QuoteSelection container={viewport} onQuote={onQuote} />
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
