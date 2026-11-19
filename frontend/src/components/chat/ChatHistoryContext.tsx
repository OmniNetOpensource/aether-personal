// 本文件用 Virtuoso 展示独立的历史上下文窗口：按消息 ID 定位、展开原文、卸载视口外 DOM，并向两侧分页而不改动实时聊天。

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { Virtuoso } from 'react-virtuoso';
import type { ListItem, VirtuosoHandle } from 'react-virtuoso';
import { chatMessageFromRow } from '../../lib/ChatController.js';
import type { ChatController, ChatMessage } from '../../lib/ChatController.js';
import { ChatMessageEntry } from './ChatMessageEntry.js';
import { highlightQuote } from './QuoteSelection.js';
import { useVirtuosoFirstItemIndex } from './useVirtuosoFirstItemIndex.js';
import type { MessageQuote } from '../../lib/messageAttachments.js';

// 页头与页尾组件保持稳定引用，避免翻页状态变化导致 Virtuoso 重建头部或尾部容器。
type ContextListContext = { header: ReactNode; footer: ReactNode };
const contextComponents = {
  Header: ({ context }: { context: ContextListContext }) => (
    <div className="history-context-list-header">{context.header}</div>
  ),
  Footer: ({ context }: { context: ContextListContext }) => (
    <div className="history-context-list-footer">{context.footer}</div>
  ),
};

// 已加载的历史与正常聊天分开保存，单侧翻页失败时仍保留当前内容。
type ContextState =
  | { status: 'loading' }
  | { status: 'error'; error: string }
  | {
      status: 'ready';
      messages: ChatMessage[];
      hasMoreBefore: boolean;
      hasMoreAfter: boolean;
      loading: 'older' | 'newer' | null;
      error: string | null;
    };

// ChatHistoryContext 共用聊天气泡与时间分组，命中的长消息首次显示时就展开，视口外消息由 Virtuoso 卸载。
export function ChatHistoryContext({
  controller,
  messageID,
  keyword,
  onBack,
  onClose,
  onLatest,
}: {
  controller: ChatController;
  messageID: string;
  keyword: string;
  onBack: () => void;
  onClose: () => void;
  onLatest: () => void;
}) {
  const list = useRef<VirtuosoHandle>(null);
  const viewport = useRef<HTMLDivElement | null>(null);
  // 稳定的回调让 Virtuoso 只在滚动容器挂载和卸载时绑定监听。
  const bindViewport = useCallback((element: HTMLElement | Window | null) => {
    viewport.current = element instanceof HTMLDivElement ? element : null;
  }, []);
  const back = useRef<HTMLButtonElement>(null);
  const version = useRef(0);
  const paging = useRef(false);
  const pendingAnchorScroll = useRef(false);
  const anchorScrollFrame = useRef(0);
  const anchorRowRef = useRef<HTMLDivElement | null>(null);
  const anchorHighlightRange = useRef<Range | null>(null);
  const clearQuoteHighlight = useRef<() => void>(() => {});
  const [anchorRow, setAnchorRow] = useState<HTMLDivElement | null>(null);
  const bindAnchorRow = useCallback((element: HTMLDivElement | null) => {
    anchorRowRef.current = element;
    setAnchorRow(element);
  }, []);
  const [anchor, setAnchor] = useState<{ id: string; quote?: MessageQuote }>({ id: messageID });
  const [state, setState] = useState<ContextState>({ status: 'loading' });
  const [retry, setRetry] = useState(0);
  const [expanded, setExpanded] = useState(() => new Set([messageID]));

  const readyMessages = state.status === 'ready' ? state.messages : [];
  const firstItemIndex = useVirtuosoFirstItemIndex(readyMessages);
  const anchorIndex = readyMessages.findIndex((message) => message.id === anchor.id);

  // 每个锚点有自己的请求版本；点击引用或退出窗口后，旧回执不再改变当前页面。
  useEffect(() => {
    const requestVersion = ++version.current;
    paging.current = false;
    pendingAnchorScroll.current = true;
    setState({ status: 'loading' });
    back.current?.focus({ preventScroll: true });
    void controller.loadMessageContext(anchor.id, 'around').then(
      (page) => {
        if (version.current !== requestVersion) return;
        setState({
          status: 'ready',
          messages: page.messages.map(chatMessageFromRow),
          hasMoreBefore: page.has_more_before,
          hasMoreAfter: page.has_more_after,
          loading: null,
          error: null,
        });
      },
      (error: unknown) => {
        if (version.current !== requestVersion) return;
        setState({
          status: 'error',
          error: error instanceof Error ? error.message : '历史消息加载失败',
        });
      },
    );
    return () => {
      version.current++;
      cancelAnimationFrame(anchorScrollFrame.current);
      clearQuoteHighlight.current();
      clearQuoteHighlight.current = () => {};
    };
  }, [controller, anchor, retry]);

  // 锚点行挂载在视口中时维持关键词高亮，随虚拟行卸载清理，滚回后自动恢复。
  useLayoutEffect(() => {
    const bubble = anchorRow?.querySelector<HTMLElement>('.bubble');
    if (!bubble) {
      anchorHighlightRange.current = null;
      return;
    }
    const highlighted = highlightSearchKeyword(bubble, keyword);
    anchorHighlightRange.current = highlighted?.firstRange ?? null;
    return () => {
      anchorHighlightRange.current = null;
      highlighted?.cleanup();
    };
  }, [anchorRow, keyword, expanded]);

  // Virtuoso 完成首次锚点定位后，再微调到首个命中片段或引用原文，避免与 initialTopMostItemIndex 互相覆盖。
  const handleItemsRendered = (items: ListItem<ChatMessage>[]) => {
    if (!pendingAnchorScroll.current) return;
    const renderedAnchor = items.find((item) => item.data?.id === anchor.id && item.size > 0);
    if (!renderedAnchor) return;
    pendingAnchorScroll.current = false;
    const requestVersion = version.current;
    cancelAnimationFrame(anchorScrollFrame.current);
    anchorScrollFrame.current = requestAnimationFrame(() => {
      if (version.current !== requestVersion) return;
      const element = viewport.current;
      const target = anchorRowRef.current;
      const bubble = target?.querySelector<HTMLElement>('.bubble');
      if (!element || !target) return;
      if (anchor.quote && bubble) {
        clearQuoteHighlight.current();
        clearQuoteHighlight.current = highlightQuote(bubble, anchor.quote, element);
        return;
      }
      const range = anchorHighlightRange.current;
      if (range) {
        const rect = range.getBoundingClientRect();
        element.scrollTop += rect.top - element.getBoundingClientRect().top - 90;
      }
    });
  };

  // 收起长消息时若消息开头已滚出视口顶部，先对齐消息起点，避免收起后阅读位置错位。
  function changeExpanded(targetID: string, index: number, value: boolean) {
    const element = viewport.current;
    const row = element?.querySelector<HTMLElement>(`[data-history-message="${targetID}"]`);
    if (
      !value &&
      element &&
      row &&
      row.getBoundingClientRect().top < element.getBoundingClientRect().top
    ) {
      list.current?.scrollToIndex({ index, align: 'start' });
    }
    setExpanded((current) => {
      const next = new Set(current);
      if (value) next.add(targetID);
      else next.delete(targetID);
      return next;
    });
  }

  // 两侧分页始终从本窗口的边界继续，新增消息去重后按原行位置排序；向上翻页位置由 Virtuoso 保持。
  const loadPage = async (direction: 'older' | 'newer') => {
    if (state.status !== 'ready' || paging.current) return;
    const edge = direction === 'older' ? state.messages[0] : state.messages.at(-1);
    if (!edge) return;
    const requestVersion = version.current;
    paging.current = true;
    setState({ ...state, loading: direction, error: null });
    try {
      const page = await controller.loadMessageContext(edge.id, direction);
      if (version.current !== requestVersion) return;
      const messages = new Map(state.messages.map((message) => [message.id, message]));
      for (const row of page.messages) messages.set(row.id, chatMessageFromRow(row));
      setState({
        ...state,
        messages: [...messages.values()].sort((a, b) => a.position.localeCompare(b.position)),
        hasMoreBefore: direction === 'older' ? page.has_more_before : state.hasMoreBefore,
        hasMoreAfter: direction === 'newer' ? page.has_more_after : state.hasMoreAfter,
        loading: null,
        error: null,
      });
    } catch (error) {
      if (version.current !== requestVersion) return;
      setState({
        ...state,
        loading: null,
        error: error instanceof Error ? error.message : '历史消息加载失败，请重试',
      });
    } finally {
      if (version.current === requestVersion) paging.current = false;
    }
  };

  // 上下翻页按钮和报错挂在 Virtuoso 页头与页尾，不占用真实消息索引。
  const listContext: ContextListContext =
    state.status === 'ready'
      ? {
          header: state.hasMoreBefore ? (
            <button
              type="button"
              className="history-context-page"
              disabled={state.loading !== null}
              onClick={() => void loadPage('older')}
            >
              {state.loading === 'older' ? '正在加载…' : '查看更早的消息 ↑'}
            </button>
          ) : null,
          footer: (
            <>
              {state.error && (
                <p className="history-context-error" role="alert">
                  {state.error}
                </p>
              )}
              {state.hasMoreAfter && (
                <button
                  type="button"
                  className="history-context-page"
                  disabled={state.loading !== null}
                  onClick={() => void loadPage('newer')}
                >
                  {state.loading === 'newer' ? '正在加载…' : '继续查看后面的消息 ↓'}
                </button>
              )}
            </>
          ),
        }
      : { header: null, footer: null };

  return (
    <section className="history-context">
      <header className="history-context-header">
        <button ref={back} type="button" className="history-context-back" onClick={onBack}>
          <span aria-hidden="true">‹</span> 返回搜索结果
        </button>
        <p>历史消息 · 搜索“{keyword}”</p>
        <button type="button" className="history-close" onClick={onClose} aria-label="关闭历史浏览">
          ×
        </button>
      </header>
      {state.status === 'loading' ? (
        <div
          className="history-context-viewport"
          tabIndex={0}
          aria-label="历史消息上下文"
          aria-busy="true"
        >
          <p className="history-context-status" role="status">
            正在加载上下文…
          </p>
        </div>
      ) : state.status === 'error' ? (
        <div className="history-context-viewport" tabIndex={0} aria-label="历史消息上下文">
          <div className="history-context-status" role="alert">
            <p>{state.error}</p>
            <button type="button" onClick={() => setRetry((value) => value + 1)}>
              重试
            </button>
          </div>
        </div>
      ) : (
        <Virtuoso
          ref={list}
          className="history-context-viewport"
          aria-label="历史消息上下文"
          aria-busy={state.loading !== null}
          scrollerRef={bindViewport}
          data={state.messages}
          components={contextComponents}
          context={listContext}
          computeItemKey={(_, message) => message.id}
          firstItemIndex={firstItemIndex}
          initialTopMostItemIndex={
            anchorIndex >= 0 ? { index: anchorIndex, align: 'start', offset: -90 } : 0
          }
          itemsRendered={handleItemsRendered}
          itemContent={(itemIndex, message) => {
            const index = itemIndex - firstItemIndex;
            const isAnchor = message.id === anchor.id;
            return (
              <div
                ref={isAnchor ? bindAnchorRow : undefined}
                data-history-message={message.id}
                className={
                  isAnchor ? 'history-context-row history-context-target' : 'history-context-row'
                }
              >
                {isAnchor && (
                  <p className="history-context-match-label">
                    {anchor.quote ? '引用原文' : '找到的消息'} ·{' '}
                    {message.createdAt.toLocaleTimeString('zh-CN', {
                      hour: '2-digit',
                      minute: '2-digit',
                      hour12: false,
                    })}
                  </p>
                )}
                <ChatMessageEntry
                  messages={state.messages}
                  index={index}
                  expanded={expanded.has(message.id)}
                  onExpandedChange={(value) => changeExpanded(message.id, index, value)}
                  onQuoteNavigate={(quote) => {
                    setExpanded((current) => new Set(current).add(quote.message_id));
                    setAnchor({ id: quote.message_id, quote });
                  }}
                />
              </div>
            );
          }}
        />
      )}
      <button type="button" className="history-context-latest" onClick={onLatest}>
        回到最新聊天 <span aria-hidden="true">↓</span>
      </button>
    </section>
  );
}

// highlightSearchKeyword 在已渲染的正文中建立高亮范围，不改 Markdown DOM，也不影响复制与代码按钮。
function highlightSearchKeyword(root: HTMLElement, keyword: string) {
  if (!('highlights' in CSS) || typeof Highlight === 'undefined') return null;
  const nodes: Text[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  let node = walker.nextNode();
  while (node) {
    if (node instanceof Text) {
      const parent = node.parentElement;
      const button = parent?.closest('button');
      // 引用卡片虽然是按钮，其快照仍属于聊天正文；代码复制等工具按钮不参与搜索高亮。
      if (
        !parent?.closest('header, .katex-mathml, [hidden]') &&
        (!button || button.classList.contains('sent-quote'))
      )
        nodes.push(node);
    }
    node = walker.nextNode();
  }
  const pattern = new RegExp(keyword.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'gi');
  const text = nodes.map((node) => node.data).join('');
  const ranges: Range[] = [];
  // 搜索基于拼接后的可见文字，再把字符位置映射回文本节点，允许命中跨越 Markdown 格式。
  for (const match of text.matchAll(pattern)) {
    let offset = 0;
    const range = document.createRange();
    for (const node of nodes) {
      if (match.index >= offset && match.index < offset + node.length)
        range.setStart(node, match.index - offset);
      const end = match.index + match[0].length;
      if (end > offset && end <= offset + node.length) {
        range.setEnd(node, end - offset);
        break;
      }
      offset += node.length;
    }
    ranges.push(range);
  }
  CSS.highlights.set('chat-search-match', new Highlight(...ranges));
  return { firstRange: ranges[0], cleanup: () => CSS.highlights.delete('chat-search-match') };
}
