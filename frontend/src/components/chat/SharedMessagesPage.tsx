// 本文件是免登录的公开对话分享阅读页：通过只读 HTTP 接口拉取选中的消息，复用聊天气泡与 Markdown 组件渲染。

import { Link } from '@tanstack/react-router';
import { useEffect, useRef, useState } from 'react';
import { chatMessageFromRow } from '../../lib/ChatController.js';
import type { ChatMessage } from '../../lib/ChatController.js';
import type { MessageQuote } from '../../lib/messageAttachments.js';
import { publicMessageShareSchema } from '../../lib/serverEvents.js';
import { LoadingScreen } from '../LoadingScreen.js';
import { ChatMessageEntry } from './ChatMessageEntry.js';
import { highlightQuote } from './QuoteSelection.js';
import './chatShare.css';

type ShareViewState =
  | { status: 'loading' }
  | { status: 'not_found' }
  | { status: 'error'; message: string }
  | { status: 'ready'; createdAt: Date; messages: ChatMessage[] };

// SharedMessagesPage 按 URL 中的分享令牌加载公开对话，默认完整展开正文以方便他人直接阅读。
export function SharedMessagesPage({ shareToken }: { shareToken: string }) {
  const viewport = useRef<HTMLDivElement>(null);
  const clearHighlight = useRef<() => void>(() => {});
  const hintTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [state, setState] = useState<ShareViewState>({ status: 'loading' });
  const [retry, setRetry] = useState(0);
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const [quoteHint, setQuoteHint] = useState('');

  useEffect(() => {
    const abortController = new AbortController();
    setState({ status: 'loading' });
    void (async () => {
      try {
        const response = await fetch(`/shares/${encodeURIComponent(shareToken)}`, {
          method: 'GET',
          headers: { Accept: 'application/json' },
          signal: abortController.signal,
        });
        if (response.status === 404) {
          setState({ status: 'not_found' });
          return;
        }
        if (!response.ok) {
          const text = (await response.text()).trim();
          throw new Error(text || '分享内容加载失败，请稍后重试');
        }
        const parsed = publicMessageShareSchema.parse(await response.json());
        const messages = parsed.messages.map(chatMessageFromRow);
        setExpanded(new Set(messages.map((message) => message.id)));
        setState({
          status: 'ready',
          createdAt: new Date(parsed.created_at),
          messages,
        });
      } catch (error) {
        if (abortController.signal.aborted) return;
        setState({
          status: 'error',
          message: error instanceof Error ? error.message : '分享内容加载失败，请稍后重试',
        });
      }
    })();
    return () => {
      abortController.abort();
      clearHighlight.current();
      clearTimeout(hintTimer.current);
    };
  }, [shareToken, retry]);

  // navigateQuote 在本次分享的消息范围内定位并高亮被引用原文；未纳入分享的原消息不向外跳转。
  const navigateQuote = (quote: MessageQuote) => {
    if (state.status !== 'ready') return;
    clearHighlight.current();
    clearTimeout(hintTimer.current);
    const existsInShare = state.messages.some((message) => message.id === quote.message_id);
    if (!existsInShare) {
      setQuoteHint('引用的原消息未包含在本次分享中');
      hintTimer.current = setTimeout(() => setQuoteHint(''), 2500);
      return;
    }
    setQuoteHint('');
    setExpanded((current) => new Set(current).add(quote.message_id));
    requestAnimationFrame(() => {
      const container = viewport.current;
      const body = container?.querySelector<HTMLElement>(
        `[data-quote-body][data-message-id="${quote.message_id}"]`,
      );
      if (!container || !body) return;
      const clear = highlightQuote(body, quote, container);
      const timer = setTimeout(clear, 3000);
      clearHighlight.current = () => {
        clearTimeout(timer);
        clear();
      };
    });
  };

  if (state.status === 'loading') {
    return <LoadingScreen />;
  }

  if (state.status === 'not_found') {
    return (
      <main className="shared-page">
        <div className="shared-empty-wrap">
          <section className="shared-empty-card" role="status">
            <img className="mark" src="/app-icon.png" alt="aether" />
            <h1>该分享不存在或已被取消</h1>
            <p>分享链接可能已失效，或已被分享者主动撤回。</p>
            <Link to="/" className="shared-empty-action">
              打开 aether
            </Link>
          </section>
        </div>
      </main>
    );
  }

  if (state.status === 'error') {
    return (
      <main className="shared-page">
        <div className="shared-empty-wrap">
          <section className="shared-empty-card" role="alert">
            <img className="mark" src="/app-icon.png" alt="aether" />
            <h1>暂时无法打开分享</h1>
            <p>{state.message}</p>
            <button
              type="button"
              className="shared-empty-action"
              onClick={() => setRetry((value) => value + 1)}
            >
              重试
            </button>
          </section>
        </div>
      </main>
    );
  }

  const formattedDate = state.createdAt.toLocaleDateString('zh-CN', {
    year: 'numeric',
    month: 'numeric',
    day: 'numeric',
  });

  return (
    <main className="shared-page">
      <header className="shared-header">
        <div className="shared-header-inner">
          <div className="shared-brand">
            <img className="shared-brand-icon" src="/app-icon.png" alt="aether" />
            <div className="shared-brand-text">
              <span className="shared-brand-title">aether · 对话分享</span>
              <span className="shared-brand-meta">
                分享于 {formattedDate} · 共 {state.messages.length} 条消息
              </span>
            </div>
          </div>
          <Link to="/" className="shared-open-home">
            打开 aether
          </Link>
        </div>
      </header>

      <div ref={viewport} className="shared-viewport" aria-label="分享的对话内容">
        <div className="shared-messages">
          {state.messages.map((message, index) => (
            <div key={message.id} data-shared-message={message.id}>
              <ChatMessageEntry
                messages={state.messages}
                index={index}
                expanded={expanded.has(message.id)}
                onExpandedChange={(isExpanded) =>
                  setExpanded((current) => {
                    const next = new Set(current);
                    if (isExpanded) next.add(message.id);
                    else next.delete(message.id);
                    return next;
                  })
                }
                onQuoteNavigate={navigateQuote}
              />
            </div>
          ))}
        </div>
      </div>

      {quoteHint && (
        <p className="shared-quote-hint" role="status">
          {quoteHint}
        </p>
      )}
    </main>
  );
}
