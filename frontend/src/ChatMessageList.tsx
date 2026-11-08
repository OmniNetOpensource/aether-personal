// 本文件是聊天消息列表：管理虚拟滚动、长消息展开与阅读位置，提供一键回底、引用回看和 Main 状态提示。

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { RefObject, UIEvent } from 'react';
import { AssistantMessageBubble } from './AssistantMessageBubble.js';
import { AssistantWorkingStatus } from './AssistantWorkingStatus.js';
import type { ChatController, ChatMessage } from './ChatController.js';
import { MessageTimeSeparator, sameDay } from './MessageTimeSeparator.js';
import { QuoteSelection, highlightQuote } from './QuoteSelection.js';
import type { MessageQuote } from './messageAttachments.js';
import { UserMessageBubble } from './UserMessageBubble.js';

// 没量过的行先占这么高；短消息会在第一次出现时改成真实高度。
const estimatedRowHeight = { status: 64, message: 96, thinking: 48, failed: 48 };
// 视口外多挂一段，快滚时新行来得及在画出来之前量高。
const overscan = 1000;
// 距底部小于这段就继续贴底；距顶部小于这段就加载更早的消息。
const edgeDistance = 160;

type VirtualRow =
  | { key: string; kind: 'status' }
  | { key: string; kind: 'thinking' }
  | { key: string; kind: 'failed' }
  | { key: string; kind: 'message'; messageIndex: number };

// 视口顶边附近那条消息，以及视口顶边离它行顶的距离。行顶在视口上方时为正。
interface TopMessage {
  key: string;
  fromViewportTop: number;
}

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
  const inner = useRef<HTMLDivElement>(null);
  const userScrolling = useRef(false);
  const loadingOlder = useRef(false);
  const quoteCleanup = useRef<() => void>(() => {});
  const quoteTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const quoteNavigation = useRef<symbol | null>(null);
  const highlightedQuote = useRef<MessageQuote | null>(null);
  const heights = useRef(new Map<string, number>());
  const rowsRef = useRef<VirtualRow[]>([]);
  const startsRef = useRef<number[]>([]);
  const topMessage = useRef<TopMessage | null>(null);
  const rowsBeforeTopMessage = useRef<string | undefined>(undefined);
  const rowElements = useRef(new Map<string, HTMLElement>());
  const rowObserver = useRef<ResizeObserver | null>(null);
  const rowRefCallbacks = useRef(new Map<string, (element: HTMLElement | null) => void>());
  const scrollFrame = useRef(0);
  const seenContent = useRef('');
  const pendingShift = useRef(0);
  const resizedMessage = useRef<TopMessage | null>(null);
  const measureRowsRef = useRef<() => boolean>(() => false);
  const rememberTopMessageRef = useRef(() => {});
  const publishScrollRef = useRef(() => {});
  const [quoteError, setQuoteError] = useState('');
  const [quoteTarget, setQuoteTarget] = useState<MessageQuote | null>(null);
  const [expandedMessages, setExpandedMessages] = useState(() => new Set<string>());
  const [, setHeightsVersion] = useState(0);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportHeight, setViewportHeight] = useState(0);
  const [listOrigin, setListOrigin] = useState(0);
  const [showScrollToBottom, setShowScrollToBottom] = useState(false);
  useEffect(
    () => () => {
      clearTimeout(quoteTimer.current);
      quoteCleanup.current();
      cancelAnimationFrame(scrollFrame.current);
    },
    [],
  );
  // 原文不在已加载历史时继续翻页；用户先停在原地，找到后再定位。
  const navigateQuote = async (quote: MessageQuote) => {
    if (quoteNavigation.current) return;
    const navigation = Symbol();
    quoteNavigation.current = navigation;
    stickToBottom.current = false;
    userScrolling.current = false;
    setQuoteError('');
    try {
      while (
        !controller.messages.some((message) => message.id === quote.message_id) &&
        controller.hasMoreHistory &&
        !controller.loadingHistory
      ) {
        const count = controller.messages.length;
        await controller.loadHistory(true);
        // 回底会撤销这次定位；迟到的历史结果仍并入列表，但不再把人带回原文。
        if (quoteNavigation.current !== navigation) return;
        if (controller.messages.length === count || controller.error) break;
      }
      if (!controller.messages.some((message) => message.id === quote.message_id)) {
        setQuoteError('暂时找不到引用原文，请加载历史后重试');
        return;
      }
      // 先展开原文，再交给布局阶段测量和定位；引用可能在原先被裁切的部分。
      setExpandedMessages((current) => new Set(current).add(quote.message_id));
      resizedMessage.current = null;
      highlightedQuote.current = null;
      setQuoteTarget(quote);
    } finally {
      if (quoteNavigation.current === navigation) quoteNavigation.current = null;
    }
  };
  const messages = controller.messages;
  const thinking = controller.connected && controller.mainThinking;
  const showStatus = Boolean(
    quoteError || controller.error || (controller.hasMoreHistory && messages.length > 0),
  );
  const failed = controller.mainFailed && !thinking;
  const rows = buildRows(messages, showStatus, thinking, failed);
  const { starts, total } = rowOffsets(rows, heights.current);
  rowsRef.current = rows;
  startsRef.current = starts;

  // 手动展开时保留原阅读位置；收起时若消息开头已在屏幕上方，就把它带回视口顶边。
  function changeMessageExpanded(messageId: string, expanded: boolean) {
    const viewportElement = viewport.current;
    const element = rowElements.current.get(messageId);
    if (viewportElement && element) {
      const offset =
        viewportElement.getBoundingClientRect().top - element.getBoundingClientRect().top;
      resizedMessage.current = {
        key: messageId,
        fromViewportTop: expanded ? offset : Math.min(0, offset),
      };
    }
    stickToBottom.current = false;
    userScrolling.current = false;
    setExpandedMessages((current) => {
      const next = new Set(current);
      if (expanded) next.add(messageId);
      else next.delete(messageId);
      return next;
    });
  }

  // 把已挂上的行量进高度表。视口上方的高度差先记下来，等总高度更新后再补到滚动位置上。
  function measureRows() {
    const viewportElement = viewport.current;
    const innerElement = inner.current;
    if (!viewportElement || !innerElement) return false;
    const origin = elementOrigin(viewportElement, innerElement);
    const currentRows = rowsRef.current;
    const currentStarts = startsRef.current;
    let changed = false;
    let shift = 0;
    for (const [key, element] of rowElements.current) {
      if (!element.isConnected) continue;
      const height = element.getBoundingClientRect().height;
      if (height < 1) continue;
      const index = currentRows.findIndex((row) => row.key === key);
      const previous =
        heights.current.get(key) ??
        estimatedRowHeight[index >= 0 ? currentRows[index].kind : 'message'];
      if (Math.abs(previous - height) < 1) {
        if (!heights.current.has(key)) heights.current.set(key, height);
        continue;
      }
      if (
        !stickToBottom.current &&
        key !== resizedMessage.current?.key &&
        index >= 0 &&
        origin + currentStarts[index] < viewportElement.scrollTop
      ) {
        shift += height - previous;
      }
      heights.current.set(key, height);
      changed = true;
    }
    // 滚动位置要等下一帧行高写进总高度后再加，否则会被旧的最大滚动值夹掉。
    if (shift !== 0) pendingShift.current += shift;
    return changed;
  }
  measureRowsRef.current = measureRows;

  // 记下视口顶边落在哪条消息上，后面有消息插到它前面时用这份距离把画面留住。
  function rememberTopMessage() {
    const viewportElement = viewport.current;
    const innerElement = inner.current;
    if (!viewportElement || !innerElement) return;
    const next = topMessageAt(
      viewportElement.scrollTop,
      elementOrigin(viewportElement, innerElement),
      rowsRef.current,
      startsRef.current,
    );
    topMessage.current = next;
    rowsBeforeTopMessage.current = next ? keysBefore(rowsRef.current, next.key) : '';
  }
  rememberTopMessageRef.current = rememberTopMessage;

  // 同步虚拟列表窗口与回底按钮，程序定位和内容尺寸变化也走这里。
  function publishScroll() {
    const viewportElement = viewport.current;
    const innerElement = inner.current;
    if (!viewportElement || !innerElement) return;
    const top = viewportElement.scrollTop;
    const height = viewportElement.clientHeight;
    const origin = elementOrigin(viewportElement, innerElement);
    setScrollTop((current) => (Math.abs(current - top) < 1 ? current : top));
    setViewportHeight((current) => (current === height ? current : height));
    setListOrigin((current) => (Math.abs(current - origin) < 1 ? current : origin));
    setShowScrollToBottom(viewportElement.scrollHeight - top - height >= edgeDistance);
  }
  publishScrollRef.current = publishScroll;

  // 用户主动回底优先于历史阅读和引用定位；后续行高测量继续由现有贴底逻辑处理。
  function scrollToBottom() {
    const element = viewport.current;
    if (!element) return;
    quoteNavigation.current = null;
    setQuoteTarget(null);
    clearTimeout(quoteTimer.current);
    quoteCleanup.current();
    highlightedQuote.current = null;
    resizedMessage.current = null;
    pendingShift.current = 0;
    loadingOlder.current = false;
    userScrolling.current = false;
    stickToBottom.current = true;
    element.scrollTop = element.scrollHeight;
    rememberTopMessage();
    publishScroll();
    // 键盘激活后按钮会隐藏，把焦点交回消息区；指针操作保留输入框焦点。
    if (document.activeElement?.classList.contains('scroll-to-bottom')) {
      element.focus({ preventScroll: true });
    }
  }

  useLayoutEffect(() => {
    const observer = new ResizeObserver(() => {
      if (measureRowsRef.current()) setHeightsVersion((version) => version + 1);
    });
    rowObserver.current = observer;
    for (const element of rowElements.current.values()) observer.observe(element);
    return () => {
      observer.disconnect();
      if (rowObserver.current === observer) rowObserver.current = null;
    };
  }, []);

  // 控制器就地改消息数组，引用不变。每次绘制后按当前列表安放滚动位置。
  useLayoutEffect(() => {
    const viewportElement = viewport.current;
    const innerElement = inner.current;
    if (!viewportElement || !innerElement) return;
    const anchor = topMessage.current;
    const anchorIndex = anchor ? rows.findIndex((row) => row.key === anchor.key) : -1;
    const prefix = anchorIndex >= 0 && anchor ? keysBefore(rows, anchor.key) : undefined;
    const prefixChanged =
      rowsBeforeTopMessage.current !== undefined &&
      prefix !== undefined &&
      prefix !== rowsBeforeTopMessage.current;
    const distanceFromBottom =
      viewportElement.scrollHeight - viewportElement.scrollTop - viewportElement.clientHeight;
    // 人已经贴底且内容变了才跟着走。发送不把人拉回底部；滑到接近底部只记下贴底，不立刻拽下去。
    const contentKey = `${messages.length}\0${messages[0]?.id ?? ''}\0${messages.at(-1)?.id ?? ''}\0${thinking}\0${failed}\0${Math.round(total)}`;
    const followBottom =
      stickToBottom.current &&
      !loadingOlder.current &&
      !quoteTarget &&
      contentKey !== seenContent.current;
    if (followBottom && distanceFromBottom > 1) {
      pendingShift.current = 0;
      viewportElement.scrollTop = viewportElement.scrollHeight;
      seenContent.current = contentKey;
      rememberTopMessage();
      publishScroll();
      return;
    }
    seenContent.current = contentKey;
    // 顶边那条消息前面多了行（更早的历史或顶部状态）时，按新增高度下移，画面仍停在那条消息。
    if (prefixChanged && anchor && anchorIndex >= 0 && !stickToBottom.current && !quoteTarget) {
      const origin = elementOrigin(viewportElement, innerElement);
      viewportElement.scrollTop = origin + starts[anchorIndex] + anchor.fromViewportTop;
    }
    if (prefix !== undefined) rowsBeforeTopMessage.current = prefix;
    if (measureRows()) {
      setHeightsVersion((version) => version + 1);
      publishScroll();
      return;
    }
    // 行高已经反映到总高度上，再补上视口上方的高度差，阅读位置才不会被夹断。
    if (pendingShift.current !== 0 && !stickToBottom.current && !quoteTarget) {
      viewportElement.scrollTop += pendingShift.current;
      pendingShift.current = 0;
    }
    // 等总高度更新后再恢复被点击消息的位置，避免折叠时被旧行高或自动贴底拉走。
    if (resizedMessage.current && !quoteTarget) {
      const { key, fromViewportTop } = resizedMessage.current;
      const index = rows.findIndex((row) => row.key === key);
      if (index >= 0) {
        viewportElement.scrollTop =
          elementOrigin(viewportElement, innerElement) + starts[index] + fromViewportTop;
      }
      resizedMessage.current = null;
      pendingShift.current = 0;
    }
    if (quoteTarget && highlightedQuote.current !== quoteTarget) {
      const index = rows.findIndex((row) => row.key === quoteTarget.message_id);
      if (index < 0) {
        setQuoteError('暂时找不到引用原文，请加载历史后重试');
        setQuoteTarget(null);
      } else {
        const origin = elementOrigin(viewportElement, innerElement);
        const height = heights.current.get(rows[index].key) ?? estimatedRowHeight[rows[index].kind];
        const next = origin + starts[index] - (viewportElement.clientHeight - height) / 2;
        const maxScroll = Math.max(0, viewportElement.scrollHeight - viewportElement.clientHeight);
        viewportElement.scrollTop = Math.min(Math.max(0, next), maxScroll);
        const body = viewportElement.querySelector<HTMLElement>(
          `[data-quote-body][data-message-id="${quoteTarget.message_id}"]`,
        );
        if (!body) {
          setQuoteError('暂时找不到引用原文，请加载历史后重试');
        } else {
          clearTimeout(quoteTimer.current);
          quoteCleanup.current();
          quoteCleanup.current = highlightQuote(body, quoteTarget, viewportElement);
          quoteTimer.current = setTimeout(() => quoteCleanup.current(), 3000);
        }
        highlightedQuote.current = quoteTarget;
        setQuoteTarget(null);
        pendingShift.current = 0;
      }
    }
    rememberTopMessage();
    publishScroll();
    if (!controller.loadingHistory) loadingOlder.current = false;
  });

  useEffect(() => {
    const observer = new ResizeObserver(() => {
      const element = viewport.current;
      if (!element) return;
      // 输入框变高或新消息把内容撑高时，贴底的人继续停在底部。
      if (stickToBottom.current && !loadingOlder.current) {
        element.scrollTop = element.scrollHeight;
        rememberTopMessageRef.current();
      }
      publishScrollRef.current();
    });
    if (viewport.current) observer.observe(viewport.current);
    if (content.current) observer.observe(content.current);
    return () => observer.disconnect();
  }, [stickToBottom]);

  const loadOlderMessages = async () => {
    if (loadingOlder.current || controller.loadingHistory || !controller.hasMoreHistory) return;
    loadingOlder.current = true;
    stickToBottom.current = false;
    await controller.loadHistory(true);
  };
  const onScroll = (event: UIEvent<HTMLDivElement>) => {
    const element = event.currentTarget;
    if (userScrolling.current) {
      rememberTopMessage();
      stickToBottom.current =
        element.scrollHeight - element.scrollTop - element.clientHeight < edgeDistance;
      if (element.scrollTop < edgeDistance && !stickToBottom.current) void loadOlderMessages();
    }
    if (scrollFrame.current) return;
    scrollFrame.current = requestAnimationFrame(() => {
      scrollFrame.current = 0;
      publishScroll();
    });
  };
  const viewHeight = viewportHeight || viewport.current?.clientHeight || 0;
  const viewTop = scrollTop - listOrigin - overscan;
  const viewBottom = scrollTop - listOrigin + viewHeight + overscan;
  const [firstVisible, lastVisible] = visibleRowRange(starts, total, viewTop, viewBottom);
  const visibleIndexes: number[] = [];
  for (let index = firstVisible; index < lastVisible; index += 1) visibleIndexes.push(index);
  if (quoteTarget) {
    const quoteIndex = rows.findIndex((row) => row.key === quoteTarget.message_id);
    if (quoteIndex >= 0 && (quoteIndex < firstVisible || quoteIndex >= lastVisible))
      visibleIndexes.push(quoteIndex);
  }
  // 收起后当前 scrollTop 可能暂时指向其他行，恢复锚点前仍保留被点击的消息。
  if (resizedMessage.current) {
    const index = rows.findIndex((row) => row.key === resizedMessage.current?.key);
    if (index >= 0 && !visibleIndexes.includes(index)) visibleIndexes.push(index);
  }
  visibleIndexes.sort((left, right) => left - right);
  // 同一条行的 ref 保持不变，避免每次绘制都解除测量。
  const bindRow = (key: string) => {
    const existing = rowRefCallbacks.current.get(key);
    if (existing) return existing;
    const callback = (element: HTMLElement | null) => {
      const current = rowElements.current.get(key);
      if (current && current !== element) rowObserver.current?.unobserve(current);
      if (element) {
        rowElements.current.set(key, element);
        rowObserver.current?.observe(element);
      } else if (current) {
        rowObserver.current?.unobserve(current);
        rowElements.current.delete(key);
      }
    };
    rowRefCallbacks.current.set(key, callback);
    return callback;
  };
  return (
    <>
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
          <div className="message-virtual" ref={inner} style={{ height: total }}>
            {visibleIndexes.map((index) => {
              const row = rows[index];
              return (
                <div
                  key={row.key}
                  ref={bindRow(row.key)}
                  className="message-virtual-row"
                  style={{ transform: `translateY(${starts[index]}px)` }}
                >
                  {row.kind === 'status' ? (
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
                          onClick={() => void loadOlderMessages()}
                        >
                          {controller.loadingHistory ? '正在加载…' : '加载更早的消息'}
                        </button>
                      )}
                    </>
                  ) : row.kind === 'thinking' ? (
                    <AssistantWorkingStatus />
                  ) : row.kind === 'failed' ? (
                    <p className="error" role="alert">
                      回复失败，请再发一条消息重试。
                    </p>
                  ) : (
                    <MessageEntry
                      messages={messages}
                      index={row.messageIndex}
                      onQuoteNavigate={(quote) => void navigateQuote(quote)}
                      expanded={expandedMessages.has(row.key)}
                      onExpandedChange={(expanded) => changeMessageExpanded(row.key, expanded)}
                    />
                  )}
                </div>
              );
            })}
          </div>
        </div>
      </div>
      {showScrollToBottom && (
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

// MessageEntry 渲染一条消息及其时间分隔；是否与前后连成一组要看完整列表，不能只看视口里的邻居。
function MessageEntry({
  messages,
  index,
  onQuoteNavigate,
  expanded,
  onExpandedChange,
}: {
  messages: ChatMessage[];
  index: number;
  onQuoteNavigate: (quote: MessageQuote) => void;
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
}) {
  const message = messages[index];
  const previous = messages[index - 1];
  const next = messages[index + 1];
  const showTime =
    !previous ||
    !sameDay(previous.createdAt, message.createdAt) ||
    Math.abs(message.createdAt.getTime() - previous.createdAt.getTime()) >= 30 * 60000;
  const joinedWithPrevious = Boolean(previous && sameGroup(previous, message));
  const endsGroup = !(next && sameGroup(message, next));
  return (
    <>
      {showTime && <MessageTimeSeparator createdAt={message.createdAt} />}
      {message.isUser ? (
        <UserMessageBubble
          message={message}
          joinedWithPrevious={joinedWithPrevious}
          endsGroup={endsGroup}
          onQuoteNavigate={onQuoteNavigate}
          expanded={expanded}
          onExpandedChange={onExpandedChange}
        />
      ) : (
        <AssistantMessageBubble
          message={message}
          joinedWithPrevious={joinedWithPrevious}
          endsGroup={endsGroup}
          expanded={expanded}
          onExpandedChange={onExpandedChange}
        />
      )}
    </>
  );
}

function sameGroup(a: ChatMessage, b: ChatMessage) {
  return (
    a.isUser === b.isUser &&
    sameDay(a.createdAt, b.createdAt) &&
    Math.abs(a.createdAt.getTime() - b.createdAt.getTime()) < 120000
  );
}

// buildRows 把顶部状态、每条消息和底部思考或失败提示排成可滚动的行。
function buildRows(
  messages: ChatMessage[],
  showStatus: boolean,
  thinking: boolean,
  failed: boolean,
) {
  const rows: VirtualRow[] = [];
  if (showStatus) rows.push({ key: 'status', kind: 'status' });
  for (let index = 0; index < messages.length; index += 1) {
    rows.push({ key: messages[index].id, kind: 'message', messageIndex: index });
  }
  if (thinking) rows.push({ key: 'thinking', kind: 'thinking' });
  if (failed) rows.push({ key: 'failed', kind: 'failed' });
  return rows;
}

// rowOffsets 用量过的高度，没量过的用估计值，得到每行起点和总高度。
function rowOffsets(rows: VirtualRow[], measured: Map<string, number>) {
  const starts: number[] = [];
  let offset = 0;
  for (const row of rows) {
    starts.push(offset);
    offset += measured.get(row.key) ?? estimatedRowHeight[row.kind];
  }
  return { starts, total: offset };
}

// visibleRowRange 找出与视口上下预渲染区相交的行。行起点升序，用二分定位第一行。
function visibleRowRange(starts: number[], total: number, top: number, bottom: number) {
  let low = 0;
  let high = starts.length;
  while (low < high) {
    const mid = (low + high) >> 1;
    const end = mid + 1 < starts.length ? starts[mid + 1] : total;
    if (end <= top) low = mid + 1;
    else high = mid;
  }
  let last = low;
  while (last < starts.length && starts[last] < bottom) last += 1;
  return [low, last] as const;
}

// topMessageAt 选视口顶边所在的那条消息；顶边还在更上方的状态行里时，就选它下面第一条消息。
function topMessageAt(
  scrollTop: number,
  origin: number,
  rows: VirtualRow[],
  starts: number[],
): TopMessage | null {
  const edge = scrollTop - origin;
  let index = -1;
  for (let i = 0; i < rows.length; i += 1) {
    if (rows[i].kind !== 'message') continue;
    if (starts[i] <= edge) index = i;
    else break;
  }
  if (index < 0) index = rows.findIndex((row) => row.kind === 'message');
  if (index < 0) return null;
  return { key: rows[index].key, fromViewportTop: scrollTop - (origin + starts[index]) };
}

// keysBefore 把锚点消息前面的行拼起来，用来判断是不是有内容插到了阅读位置上方。
function keysBefore(rows: VirtualRow[], key: string) {
  const index = rows.findIndex((row) => row.key === key);
  if (index < 0) return '';
  let keys = '';
  for (let i = 0; i < index; i += 1) keys += `${rows[i].key}\0`;
  return keys;
}

// elementOrigin 是虚拟行区域顶部在滚动内容里的位置，消息内容的上内边距算在里面。
function elementOrigin(viewportElement: HTMLElement, innerElement: HTMLElement) {
  return (
    innerElement.getBoundingClientRect().top -
    viewportElement.getBoundingClientRect().top +
    viewportElement.scrollTop
  );
}
