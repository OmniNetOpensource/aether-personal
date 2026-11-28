// 本文件展示独立的历史上下文窗口：按消息 ID 定位、展开并高亮原文，使用 Effect Atom 请求上下文并自动中断过期请求。

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { useAtomSet } from '@effect/atom-react';
import { Cause, Exit } from 'effect';
import { chatMessageFromRow } from '../../../lib/ChatController.js';
import type { ChatMessage } from '../../../lib/ChatController.js';
import { formatCauseError } from '../../../lib/errors.js';
import type { MessageQuote } from '../../../lib/messageAttachments.js';
import { loadMessageContextAtom } from '../../../lib/runtime.js';
import { ChatMessageEntry, getMessageGroupState } from '../messages/ChatMessageEntry.js';
import { highlightQuote } from '../messages/QuoteSelection.js';

// 已加载的历史与正常聊天分开保存，单侧翻页失败时仍保留当前内容。
type ContextState =
  | { readonly status: 'loading' }
  | { readonly status: 'error'; readonly error: string }
  | {
      readonly status: 'ready';
      readonly messages: readonly ChatMessage[];
      readonly hasMoreBefore: boolean;
      readonly hasMoreAfter: boolean;
      readonly loading: 'older' | 'newer' | null;
      readonly error: string | null;
    };

// ChatHistoryContext 共用聊天气泡与时间分组，命中的长消息默认展开，向上翻页时按高度差保持当前阅读位置。
export function ChatHistoryContext({
  messageID,
  keyword,
  onBack,
  onClose,
  onLatest,
}: {
  messageID: string;
  keyword: string;
  onBack: () => void;
  onClose: () => void;
  onLatest: () => void;
}) {
  const loadContext = useAtomSet(loadMessageContextAtom, { mode: 'promiseExit' });
  const viewport = useRef<HTMLDivElement>(null);
  const back = useRef<HTMLButtonElement>(null);
  const paging = useRef(false);
  const activeAnchorID = useRef(messageID);
  const pendingAnchorScroll = useRef(false);
  const prependAnchor = useRef<{ scrollHeight: number; scrollTop: number } | null>(null);
  const clearQuoteHighlight = useRef<() => void>(() => {});
  const [anchor, setAnchor] = useState<{ readonly id: string; readonly quote?: MessageQuote }>({
    id: messageID,
  });
  const [state, setState] = useState<ContextState>({ status: 'loading' });
  const [retry, setRetry] = useState(0);
  const [expanded, setExpanded] = useState(() => new Set([messageID]));

  // 每次锚点切换发起新请求，AtomResultFn 自动中断上一条未完成的上下文加载。
  useEffect(() => {
    let active = true;
    activeAnchorID.current = anchor.id;
    paging.current = false;
    pendingAnchorScroll.current = true;
    prependAnchor.current = null;
    setState({ status: 'loading' });
    back.current?.focus({ preventScroll: true });

    void loadContext({ messageID: anchor.id, direction: 'around' }).then((exit) => {
      if (!active) return;
      if (Exit.isSuccess(exit)) {
        setState({
          status: 'ready',
          messages: (exit.value.messages ?? []).map(chatMessageFromRow),
          hasMoreBefore: exit.value.has_more_before,
          hasMoreAfter: exit.value.has_more_after,
          loading: null,
          error: null,
        });
      } else if (!Cause.hasInterruptsOnly(exit.cause)) {
        setState({
          status: 'error',
          error: formatCauseError(exit.cause, '历史消息加载失败'),
        });
      }
    });

    return () => {
      active = false;
      clearQuoteHighlight.current();
      clearQuoteHighlight.current = () => {};
    };
  }, [loadContext, anchor, retry]);

  // 向上翻出更早一页后按容器新增高度补齐 scrollTop，保持正在阅读的内容不跳动。
  const readyMessages = state.status === 'ready' ? state.messages : null;
  useLayoutEffect(() => {
    const saved = prependAnchor.current;
    const element = viewport.current;
    if (!saved || !element) return;
    prependAnchor.current = null;
    element.scrollTop = saved.scrollTop + (element.scrollHeight - saved.scrollHeight);
  }, [readyMessages]);

  // 维持锚点消息中的关键词高亮，并在首次就绪或跳转引用时把目标片段滚到视口合适位置。
  const ready = state.status === 'ready';
  useLayoutEffect(() => {
    if (!ready) return;
    const element = viewport.current;
    const targetRow = element?.querySelector<HTMLElement>(`[data-history-message="${anchor.id}"]`);
    const bubble = targetRow?.querySelector<HTMLElement>('.bubble');
    if (!element || !targetRow) return;

    const highlighted = bubble ? highlightSearchKeyword(bubble, keyword) : null;
    if (pendingAnchorScroll.current) {
      pendingAnchorScroll.current = false;
      if (anchor.quote && bubble) {
        clearQuoteHighlight.current();
        clearQuoteHighlight.current = highlightQuote(bubble, anchor.quote, element);
      } else if (highlighted?.firstRange) {
        const rect = highlighted.firstRange.getBoundingClientRect();
        element.scrollTop += rect.top - element.getBoundingClientRect().top - 90;
      } else {
        const rect = targetRow.getBoundingClientRect();
        element.scrollTop += rect.top - element.getBoundingClientRect().top - 90;
      }
    }
    return () => {
      highlighted?.cleanup();
    };
  }, [ready, anchor, keyword, expanded]);

  // 展开状态按消息 ID 记录；收起长消息时的滚动锚定由 CollapsibleMessage 同步维护。
  const changeExpanded = useCallback((targetID: string, value: boolean) => {
    setExpanded((current) => {
      if (current.has(targetID) === value) return current;
      const next = new Set(current);
      if (value) next.add(targetID);
      else next.delete(targetID);
      return next;
    });
  }, []);

  const handleQuoteNavigate = useCallback((quote: MessageQuote) => {
    setExpanded((current) => new Set(current).add(quote.message_id));
    setAnchor({ id: quote.message_id, quote });
  }, []);

  // 两侧分页始终从本窗口的边界继续，新增消息去重后按原行位置排序；向上翻页前记录当前高度以保阅读位置。
  const loadPage = async (direction: 'older' | 'newer') => {
    if (state.status !== 'ready' || paging.current) return;
    const edge = direction === 'older' ? state.messages[0] : state.messages.at(-1);
    if (!edge) return;
    const targetAnchorID = anchor.id;
    paging.current = true;
    setState({ ...state, loading: direction, error: null });
    try {
      const exit = await loadContext({ messageID: edge.id, direction });
      if (activeAnchorID.current !== targetAnchorID) return;
      if (Exit.isSuccess(exit)) {
        const element = viewport.current;
        if (direction === 'older' && element) {
          prependAnchor.current = {
            scrollHeight: element.scrollHeight,
            scrollTop: element.scrollTop,
          };
        }
        setState((current) => {
          if (current.status !== 'ready') return current;
          const messages = new Map(current.messages.map((message) => [message.id, message]));
          for (const row of exit.value.messages ?? [])
            messages.set(row.id, chatMessageFromRow(row));
          return {
            ...current,
            messages: [...messages.values()].sort((a, b) => a.position.localeCompare(b.position)),
            hasMoreBefore:
              direction === 'older' ? exit.value.has_more_before : current.hasMoreBefore,
            hasMoreAfter: direction === 'newer' ? exit.value.has_more_after : current.hasMoreAfter,
            loading: null,
            error: null,
          };
        });
      } else if (!Cause.hasInterruptsOnly(exit.cause)) {
        setState((current) =>
          current.status === 'ready'
            ? {
                ...current,
                loading: null,
                error: formatCauseError(exit.cause, '历史消息加载失败，请重试'),
              }
            : current,
        );
      }
    } finally {
      if (activeAnchorID.current === targetAnchorID) paging.current = false;
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
        <div
          ref={viewport}
          className="history-context-viewport"
          tabIndex={0}
          aria-label="历史消息上下文"
          aria-busy={state.loading !== null}
        >
          <div className="history-context-list-header">
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
          </div>
          {state.messages.map((message, index) => {
            const { showTime, joinedWithPrevious, endsGroup } = getMessageGroupState(
              state.messages,
              index,
            );
            const isAnchor = message.id === anchor.id;
            return (
              <div
                key={message.id}
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
                  message={message}
                  showTime={showTime}
                  joinedWithPrevious={joinedWithPrevious}
                  endsGroup={endsGroup}
                  expanded={expanded.has(message.id)}
                  onExpandedChange={changeExpanded}
                  onQuoteNavigate={handleQuoteNavigate}
                />
              </div>
            );
          })}
          <div className="history-context-list-footer">
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
        </div>
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
