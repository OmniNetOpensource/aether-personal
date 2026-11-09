// 本文件展示独立的历史上下文窗口：按消息 ID 定位、展开原文，并向两侧分页而不改动实时聊天。

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { chatMessageFromRow } from '../../lib/ChatController.js';
import type { ChatController, ChatMessage } from '../../lib/ChatController.js';
import { ChatMessageEntry } from './ChatMessageEntry.js';
import { highlightQuote } from './QuoteSelection.js';
import type { MessageQuote } from '../../lib/messageAttachments.js';

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

// ChatHistoryContext 共用聊天气泡与时间分组，命中的长消息首次显示时就展开。
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
  const viewport = useRef<HTMLDivElement>(null);
  const back = useRef<HTMLButtonElement>(null);
  const version = useRef(0);
  const paging = useRef(false);
  const pendingScroll = useRef<
    { kind: 'anchor' } | { kind: 'prepend'; height: number; top: number } | null
  >(null);
  const [anchor, setAnchor] = useState<{ id: string; quote?: MessageQuote }>({ id: messageID });
  const [state, setState] = useState<ContextState>({ status: 'loading' });
  const [retry, setRetry] = useState(0);
  const [expanded, setExpanded] = useState(() => new Set([messageID]));

  // 每个锚点有自己的请求版本；点击引用或退出窗口后，旧回执不再改变当前页面。
  useEffect(() => {
    const requestVersion = ++version.current;
    paging.current = false;
    setState({ status: 'loading' });
    back.current?.focus({ preventScroll: true });
    void controller.loadMessageContext(anchor.id, 'around').then(
      (page) => {
        if (version.current !== requestVersion) return;
        pendingScroll.current = { kind: 'anchor' };
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
    };
  }, [controller, anchor, retry]);

  // 按锚点定位到首个命中片段；向前翻页时按高度差保住原来的阅读位置。
  useLayoutEffect(() => {
    const element = viewport.current;
    if (!element || state.status !== 'ready') return;
    const target = element.querySelector<HTMLElement>(`[data-history-message="${anchor.id}"]`);
    const bubble = target?.querySelector<HTMLElement>('.bubble');
    const highlighted = bubble ? highlightSearchKeyword(bubble, keyword) : null;
    const scroll = pendingScroll.current;
    pendingScroll.current = null;
    let quoteCleanup = () => {};
    if (scroll?.kind === 'prepend') {
      element.scrollTop = scroll.top + element.scrollHeight - scroll.height;
    } else if (scroll?.kind === 'anchor' && target) {
      if (anchor.quote && bubble) {
        quoteCleanup = highlightQuote(bubble, anchor.quote, element);
      } else {
        const rect =
          highlighted?.firstRange?.getBoundingClientRect() ?? target.getBoundingClientRect();
        element.scrollTop += rect.top - element.getBoundingClientRect().top - 90;
      }
    }
    return () => {
      highlighted?.cleanup();
      quoteCleanup();
    };
  }, [state, anchor, keyword, expanded]);

  // 两侧分页始终从本窗口的边界继续，新增消息去重后按原行位置排序。
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
      const element = viewport.current;
      if (direction === 'older' && element) {
        pendingScroll.current = {
          kind: 'prepend',
          height: element.scrollHeight,
          top: element.scrollTop,
        };
      }
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
      <div
        ref={viewport}
        className="history-context-viewport"
        tabIndex={0}
        aria-label="历史消息上下文"
        aria-busy={
          state.status === 'loading' || (state.status === 'ready' && state.loading !== null)
        }
      >
        {state.status === 'loading' ? (
          <p className="history-context-status" role="status">
            正在加载上下文…
          </p>
        ) : state.status === 'error' ? (
          <div className="history-context-status" role="alert">
            <p>{state.error}</p>
            <button type="button" onClick={() => setRetry((value) => value + 1)}>
              重试
            </button>
          </div>
        ) : (
          <div className="history-context-messages">
            {state.hasMoreBefore && (
              <button
                type="button"
                className="history-context-page"
                disabled={state.loading !== null}
                onClick={() => void loadPage('older')}
              >
                {state.loading === 'older' ? '正在加载…' : '查看更早的消息 ↑'}
              </button>
            )}
            {state.messages.map((message, index) => (
              <div
                key={message.id}
                data-history-message={message.id}
                className={message.id === anchor.id ? 'history-context-target' : undefined}
              >
                {message.id === anchor.id && (
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
                  onExpandedChange={(value) =>
                    setExpanded((current) => {
                      const next = new Set(current);
                      if (value) next.add(message.id);
                      else next.delete(message.id);
                      return next;
                    })
                  }
                  onQuoteNavigate={(quote) => {
                    setExpanded((current) => new Set(current).add(quote.message_id));
                    setAnchor({ id: quote.message_id, quote });
                  }}
                />
              </div>
            ))}
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
          </div>
        )}
      </div>
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
