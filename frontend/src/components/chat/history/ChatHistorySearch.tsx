// 本文件展示历史聊天搜索浮层：用 Effect Fiber 自动防抖与取消旧请求，分页浏览结果并打开原文上下文窗口。

import { useEffect, useRef, useState } from 'react';
import { useAtomSet } from '@effect/atom-react';
import { Cause, Effect, Exit, Fiber } from 'effect';
import { formatCauseError } from '../../../lib/errors.js';
import { searchMessagesAtom } from '../../../lib/runtime.js';
import type { ChatSearchHit } from '../../../lib/serverEvents.js';
import { ModalDialog } from '../../ModalDialog.js';
import { ChatHistoryContext } from './ChatHistoryContext.js';
import './chatSearch.css';

// 搜索状态区分首次读取和已有结果的翻页，失败时保留已读的片段供继续查看。
type SearchState =
  | { readonly status: 'idle' }
  | { readonly status: 'loading'; readonly keyword: string }
  | { readonly status: 'error'; readonly keyword: string; readonly error: string }
  | {
      readonly status: 'ready';
      readonly keyword: string;
      readonly results: readonly ChatSearchHit[];
      readonly nextBefore?: string;
      readonly loadingMore: boolean;
      readonly error: string | null;
    };

// ChatHistorySearch 使用模态对话框约束键盘焦点；搜索期间正常聊天继续收消息和保存草稿。
export function ChatHistorySearch({
  onClose,
  onLatest,
}: {
  onClose: () => void;
  onLatest: () => void;
}) {
  const runSearch = useAtomSet(searchMessagesAtom, { mode: 'promiseExit' });
  const input = useRef<HTMLInputElement>(null);
  const resultButton = useRef<HTMLButtonElement | null>(null);
  const composing = useRef(false);
  const activeKeyword = useRef('');
  const [text, setText] = useState('');
  const [keyword, setKeyword] = useState('');
  const [retry, setRetry] = useState(0);
  const [state, setState] = useState<SearchState>({ status: 'idle' });
  const [selected, setSelected] = useState<{
    readonly id: string;
    readonly keyword: string;
  } | null>(null);

  // 模态框通过 useLayoutEffect 打开后聚焦搜索框；IME 组词期间不发起中间关键词请求。
  useEffect(() => {
    input.current?.focus();
  }, []);

  // 关键词变更时中断上一次防抖与搜索 Fiber，迟到的回执无法覆盖新关键词结果。
  useEffect(() => {
    const trimmed = keyword.trim();
    activeKeyword.current = trimmed;
    if (!trimmed) {
      setState({ status: 'idle' });
      return;
    }
    if (Array.from(trimmed).length > 200) {
      setState({ status: 'error', keyword: trimmed, error: '搜索词不能超过 200 个字符' });
      return;
    }
    setState({ status: 'loading', keyword: trimmed });

    const fiber = Effect.runFork(
      Effect.sleep('300 millis').pipe(
        Effect.andThen(Effect.promise(() => runSearch({ keyword: trimmed }))),
        Effect.tap((exit) =>
          Effect.sync(() => {
            if (activeKeyword.current !== trimmed) return;
            if (Exit.isSuccess(exit)) {
              setState({
                status: 'ready',
                keyword: trimmed,
                results: exit.value.results,
                nextBefore: exit.value.next_before,
                loadingMore: false,
                error: null,
              });
            } else if (!Cause.hasInterruptsOnly(exit.cause)) {
              setState({
                status: 'error',
                keyword: trimmed,
                error: formatCauseError(exit.cause, '搜索失败，请重试'),
              });
            }
          }),
        ),
      ),
    );

    return () => {
      Effect.runFork(Fiber.interrupt(fiber));
    };
  }, [runSearch, keyword, retry]);

  // 翻页只在同一关键词的结果后追加，使用服务端返回的消息 ID 游标。
  const loadMore = async () => {
    if (state.status !== 'ready' || state.loadingMore || !state.nextBefore) return;
    const targetKeyword = state.keyword;
    setState({ ...state, loadingMore: true, error: null });
    const exit = await runSearch({ keyword: targetKeyword, before: state.nextBefore });
    if (activeKeyword.current !== targetKeyword) return;
    if (Exit.isSuccess(exit)) {
      setState((current) =>
        current.status === 'ready' && current.keyword === targetKeyword
          ? {
              ...current,
              results: [...current.results, ...exit.value.results],
              nextBefore: exit.value.next_before,
              loadingMore: false,
              error: null,
            }
          : current,
      );
    } else if (!Cause.hasInterruptsOnly(exit.cause)) {
      setState((current) =>
        current.status === 'ready' && current.keyword === targetKeyword
          ? {
              ...current,
              loadingMore: false,
              error: formatCauseError(exit.cause, '搜索失败，请重试'),
            }
          : current,
      );
    }
  };

  return (
    <ModalDialog
      className={`history-search-dialog${selected ? ' is-browsing' : ''}`}
      ariaLabel={selected ? '查看历史消息上下文' : '搜索历史聊天'}
      onClose={onClose}
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
    </ModalDialog>
  );
}

// SearchHighlight 按字面关键词生成 mark，不把消息片段当作 HTML 执行。
function SearchHighlight({ text, keyword }: { text: string; keyword: string }) {
  const pattern = new RegExp(`(${keyword.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`, 'gi');
  return text
    .split(pattern)
    .map((part, index) => (index % 2 ? <mark key={index}>{part}</mark> : part));
}
