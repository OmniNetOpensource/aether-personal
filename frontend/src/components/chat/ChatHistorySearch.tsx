// 本文件展示历史聊天搜索浮层：按关键词分页，保留结果位置，并打开独立的原文上下文窗口。

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { ChatController } from '../../lib/ChatController.js';
import type { ChatSearchHit } from '../../lib/serverEvents.js';
import { ChatHistoryContext } from './ChatHistoryContext.js';
import './chatSearch.css';

// 搜索状态区分首次读取和已有结果的翻页，失败时保留已读的片段供继续查看。
type SearchState =
  | { status: 'idle' }
  | { status: 'loading'; keyword: string }
  | { status: 'error'; keyword: string; error: string }
  | {
      status: 'ready';
      keyword: string;
      results: ChatSearchHit[];
      nextBefore?: string;
      loadingMore: boolean;
      error: string | null;
    };

// ChatHistorySearch 使用原生模态对话框约束键盘焦点；搜索期间正常聊天继续收消息和保存草稿。
export function ChatHistorySearch({
  controller,
  onClose,
  onLatest,
}: {
  controller: ChatController;
  onClose: () => void;
  onLatest: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const resultButton = useRef<HTMLButtonElement | null>(null);
  const version = useRef(0);
  const composing = useRef(false);
  const [text, setText] = useState('');
  const [keyword, setKeyword] = useState('');
  const [retry, setRetry] = useState(0);
  const [state, setState] = useState<SearchState>({ status: 'idle' });
  const [selected, setSelected] = useState<{ id: string; keyword: string } | null>(null);

  // 对话框关闭后浏览器会把焦点还给搜索入口；IME 组词期间不发起中间关键词请求。
  useLayoutEffect(() => {
    const element = dialog.current;
    element?.showModal();
    input.current?.focus();
    return () => element?.close();
  }, []);

  // 每次词语变更使旧请求失效，迟到的回执不能覆盖较新的结果或追加到另一组分页。
  useEffect(() => {
    const requestVersion = ++version.current;
    const trimmed = keyword.trim();
    if (!trimmed) {
      setState({ status: 'idle' });
      return;
    }
    if (Array.from(trimmed).length > 200) {
      setState({ status: 'error', keyword: trimmed, error: '搜索词不能超过 200 个字符' });
      return;
    }
    setState({ status: 'loading', keyword: trimmed });
    const timer = setTimeout(() => {
      void controller.searchMessages(trimmed).then(
        (page) => {
          if (version.current !== requestVersion) return;
          setState({
            status: 'ready',
            keyword: trimmed,
            results: page.results,
            nextBefore: page.next_before,
            loadingMore: false,
            error: null,
          });
        },
        (error: unknown) => {
          if (version.current !== requestVersion) return;
          setState({
            status: 'error',
            keyword: trimmed,
            error: error instanceof Error ? error.message : '搜索失败，请重试',
          });
        },
      );
    }, 300);
    return () => {
      clearTimeout(timer);
      version.current++;
    };
  }, [controller, keyword, retry]);

  // 翻页只在同一关键词的结果后追加，使用服务端返回的消息 ID 游标。
  const loadMore = async () => {
    if (state.status !== 'ready' || state.loadingMore || !state.nextBefore) return;
    const requestVersion = version.current;
    setState({ ...state, loadingMore: true, error: null });
    try {
      const page = await controller.searchMessages(state.keyword, state.nextBefore);
      if (version.current !== requestVersion) return;
      setState({
        ...state,
        results: [...state.results, ...page.results],
        nextBefore: page.next_before,
        loadingMore: false,
        error: null,
      });
    } catch (error) {
      if (version.current !== requestVersion) return;
      setState({
        ...state,
        loadingMore: false,
        error: error instanceof Error ? error.message : '搜索失败，请重试',
      });
    }
  };

  return (
    <dialog
      ref={dialog}
      className={`history-search-dialog${selected ? ' is-browsing' : ''}`}
      aria-label={selected ? '查看历史消息上下文' : '搜索历史聊天'}
      onKeyDown={(event) => {
        // 搜索输入框会先消费 Escape；本层显式关闭，但把组词和嵌套图片对话框交给它们自己处理。
        if (
          event.key === 'Escape' &&
          !event.nativeEvent.isComposing &&
          event.target instanceof Element &&
          event.target.closest('dialog') === event.currentTarget
        ) {
          event.preventDefault();
          onClose();
        }
      }}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClick={(event) => {
        if (event.target !== event.currentTarget) return;
        const rect = event.currentTarget.getBoundingClientRect();
        if (
          event.clientX < rect.left ||
          event.clientX > rect.right ||
          event.clientY < rect.top ||
          event.clientY > rect.bottom
        )
          onClose();
      }}
    >
      <section className="history-search-panel" hidden={selected !== null}>
        <header className="history-search-header">
          <div className="history-search-title">
            <h2>搜索历史聊天</h2>
            <button type="button" className="history-close" onClick={onClose} aria-label="关闭搜索">
              <span className="history-desktop-close" aria-hidden="true">
                ×
              </span>
              <span className="history-mobile-close" aria-hidden="true">
                取消
              </span>
            </button>
          </div>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              if (!composing.current) {
                setKeyword(text);
                setRetry((value) => value + 1);
              }
            }}
          >
            <div className="history-search-input">
              <svg viewBox="0 0 24 24" aria-hidden="true">
                <circle cx="10.5" cy="10.5" r="6.5" />
                <path d="m16 16 4 4" />
              </svg>
              <input
                ref={input}
                type="search"
                aria-label="搜索关键词"
                placeholder="搜索历史聊天"
                value={text}
                enterKeyHint="search"
                onChange={(event) => {
                  setText(event.target.value);
                  if (!composing.current) setKeyword(event.target.value);
                }}
                onCompositionStart={() => {
                  composing.current = true;
                }}
                onCompositionEnd={(event) => {
                  composing.current = false;
                  setKeyword(event.currentTarget.value);
                }}
              />
              {text && (
                <button
                  type="button"
                  className="history-search-clear"
                  aria-label="清空搜索"
                  onClick={() => {
                    setText('');
                    setKeyword('');
                    input.current?.focus();
                  }}
                >
                  ×
                </button>
              )}
            </div>
          </form>
        </header>
        <div
          className="history-search-results"
          key={keyword}
          aria-busy={state.status === 'loading' || (state.status === 'ready' && state.loadingMore)}
        >
          {state.status === 'idle' ? (
            <p className="history-search-empty">输入关键词，找回之前聊过的内容</p>
          ) : state.status === 'loading' ? (
            <p className="history-search-empty" role="status">
              正在搜索…
            </p>
          ) : state.status === 'error' ? (
            <div className="history-search-empty" role="alert">
              <p>{state.error}</p>
              <button type="button" onClick={() => setRetry((value) => value + 1)}>
                重试
              </button>
            </div>
          ) : (
            <>
              <div className="history-search-summary">
                <span>包含“{state.keyword}”的消息</span>
                <span>最新在前</span>
              </div>
              {state.results.length === 0 ? (
                <p className="history-search-empty" role="status">
                  没有找到相关消息，试试更短的关键词
                </p>
              ) : (
                <ul className="history-search-hits">
                  {state.results.map((hit) => (
                    <li key={hit.id}>
                      <button
                        type="button"
                        className="history-search-hit"
                        onClick={(event) => {
                          resultButton.current = event.currentTarget;
                          setSelected({ id: hit.id, keyword: state.keyword });
                        }}
                      >
                        <span className="history-hit-meta">
                          <span className="history-hit-sender">
                            {hit.sender === 'user' ? '你' : 'aether'}
                          </span>
                          <time dateTime={hit.created_at}>
                            {new Date(hit.created_at).toLocaleString('zh-CN', {
                              year: 'numeric',
                              month: 'numeric',
                              day: 'numeric',
                              hour: '2-digit',
                              minute: '2-digit',
                              hour12: false,
                            })}
                          </time>
                        </span>
                        <span className="history-hit-excerpt">
                          <SearchHighlight text={hit.excerpt} keyword={state.keyword} />
                        </span>
                        <span className="history-hit-action">
                          查看上下文 <span aria-hidden="true">→</span>
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
              {state.error && (
                <p className="history-search-error" role="alert">
                  {state.error}
                </p>
              )}
              {state.nextBefore && (
                <button
                  type="button"
                  className="history-search-more"
                  disabled={state.loadingMore}
                  onClick={() => void loadMore()}
                >
                  {state.loadingMore ? '正在加载…' : state.error ? '重试加载' : '加载更多结果'}
                </button>
              )}
            </>
          )}
        </div>
        <footer className="history-search-footer">
          <span>仅搜索你可见的聊天内容</span>
          <span className="history-search-escape">
            <kbd>Esc</kbd> 关闭
          </span>
        </footer>
      </section>
      {selected && (
        <ChatHistoryContext
          controller={controller}
          messageID={selected.id}
          keyword={selected.keyword}
          onBack={() => {
            setSelected(null);
            requestAnimationFrame(() => resultButton.current?.focus({ preventScroll: true }));
          }}
          onClose={onClose}
          onLatest={onLatest}
        />
      )}
    </dialog>
  );
}

// SearchHighlight 按字面关键词生成 mark，不把消息片段当作 HTML 执行。
function SearchHighlight({ text, keyword }: { text: string; keyword: string }) {
  const pattern = new RegExp(`(${keyword.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`, 'gi');
  return text
    .split(pattern)
    .map((part, index) => (index % 2 ? <mark key={index}>{part}</mark> : part));
}
