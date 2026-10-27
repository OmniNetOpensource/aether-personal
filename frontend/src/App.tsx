import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { UIEvent } from 'react';
import { ChatController } from './chat.js';
import type { ChatMessage } from './chat.js';
import { Markdown } from './Markdown.js';
import { PwaControls } from './PwaControls.js';

const controller = new ChatController();

export function App() {
  const [, refresh] = useState(0);
  const [online, setOnline] = useState(navigator.onLine);
  useEffect(() => {
    const unsubscribe = controller.subscribe(() => refresh((value) => value + 1));
    void controller.initialize();
    const resume = () => {
      setOnline(navigator.onLine);
      if (navigator.onLine && !document.hidden) controller.reconnect();
    };
    const suspend = () => {
      setOnline(navigator.onLine);
      controller.suspend();
    };
    const visibility = () => (document.hidden ? suspend() : resume());
    const sessionChanged = (event: StorageEvent) => {
      if (event.key === 'aether_session' && event.newValue !== controller.token) location.reload();
    };
    window.addEventListener('online', resume);
    window.addEventListener('offline', suspend);
    window.addEventListener('pageshow', resume);
    window.addEventListener('pagehide', suspend);
    window.addEventListener('storage', sessionChanged);
    document.addEventListener('visibilitychange', visibility);
    return () => {
      unsubscribe();
      controller.dispose();
      window.removeEventListener('online', resume);
      window.removeEventListener('offline', suspend);
      window.removeEventListener('pageshow', resume);
      window.removeEventListener('pagehide', suspend);
      window.removeEventListener('storage', sessionChanged);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, []);
  return (
    <>
      <header className="chat-toolbar">
        <span className="chat-name">阿芒</span>
        <div className="app-actions">
          <span className="connection-status" role="status">
            {!online
              ? '离线 · 联网后恢复'
              : controller.token && !controller.connected
                ? '正在连接…'
                : ''}
          </span>
          <PwaControls />
        </div>
      </header>
      {controller.initializing ? (
        <main className="loading">
          <p className="muted">正在加载…</p>
        </main>
      ) : controller.token ? (
        <Chat />
      ) : (
        <Login />
      )}
    </>
  );
}

function Login() {
  const [email, setEmail] = useState(controller.email);
  const [code, setCode] = useState('');
  const [sent, setSent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const submitting = useRef(false);
  const devLogin = controller.devLoginAvailable;
  const buttonLabel = devLogin ? '开发测试登录' : sent ? '登录' : '获取验证码';
  const submit = async () => {
    if (submitting.current) return;
    if (!devLogin && (!email.includes('@') || !email.includes('.'))) {
      setError('请输入有效的邮箱地址');
      return;
    }
    if (!devLogin && sent && !/^\d{6}$/.test(code.trim())) {
      setError('请输入 6 位验证码');
      return;
    }
    submitting.current = true;
    setBusy(true);
    setError('');
    try {
      if (devLogin) await controller.devSignIn();
      else if (sent) await controller.signIn(email, code);
      else {
        await controller.requestCode(email);
        setSent(true);
      }
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : '连接失败，请检查网络后重试');
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  };
  return (
    <main className="login-page">
      <div className="login-center">
        <form
          className="login-card"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <img className="mark" src={controller.iconURL} alt="阿芒" width="56" height="56" />
          <h1 className="login-title">和阿芒聊聊</h1>
          <p className="login-subtitle">
            {devLogin
              ? '本地开发环境，一键进入测试对话。'
              : sent
                ? '输入验证码，继续你的对话。'
                : '用邮箱登录，开启你们的对话。'}
          </p>
          {!devLogin && (
            <input
              id="email"
              className="login-input"
              type="email"
              placeholder="邮箱地址"
              aria-label="邮箱地址"
              autoComplete="email"
              value={email}
              readOnly={sent}
              maxLength={254}
              required
              onChange={(event) => setEmail(event.target.value)}
            />
          )}
          {!devLogin && sent && (
            <input
              id="code"
              className="login-input"
              type="text"
              inputMode="numeric"
              pattern="[0-9]{6}"
              autoComplete="one-time-code"
              placeholder="6 位数字"
              aria-label="验证码"
              value={code}
              maxLength={6}
              autoFocus
              required
              onChange={(event) => setCode(event.target.value.replace(/\D/g, '').slice(0, 6))}
            />
          )}
          {(error || controller.error) && (
            <p className="error login-error" role="alert">
              {error || controller.error}
            </p>
          )}
          <button className="login-button" type="submit" disabled={busy}>
            {busy ? '请稍候…' : buttonLabel}
          </button>
          {!devLogin && sent && (
            <button
              type="button"
              className="change-email link"
              disabled={busy}
              onClick={() => {
                setSent(false);
                setError('');
                setCode('');
              }}
            >
              更换邮箱或重新获取
            </button>
          )}
        </form>
      </div>
    </main>
  );
}

function Chat() {
  const draftKey = `aether_draft:${controller.email}`;
  const [draft, setDraft] = useState(() => {
    try {
      return sessionStorage.getItem(draftKey) ?? '';
    } catch {
      return '';
    }
  });
  const [error, setError] = useState('');
  const viewport = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const nearBottom = useRef(true);
  const userScrolling = useRef(false);
  const loadingOlder = useRef(false);
  const sending = useRef(false);
  const recap = controller.activity.state === 'working' ? controller.activity.summary : '';
  const thinking =
    controller.connected && (controller.mainThinking || controller.activity.state === 'working');
  const messages = controller.messages;
  const lastUser = messages.reduce((last, message, index) => (message.isUser ? index : last), -1);
  const scrollEnd = () => {
    userScrolling.current = false;
    const element = viewport.current;
    if (element) element.scrollTop = element.scrollHeight;
  };
  useEffect(() => {
    try {
      if (draft) sessionStorage.setItem(draftKey, draft);
      else sessionStorage.removeItem(draftKey);
    } catch {
      /* Drafts still work in memory when storage is disabled. */
    }
  }, [draft, draftKey]);
  useLayoutEffect(() => {
    const element = input.current;
    if (!element) return;
    element.style.height = '0px';
    element.style.height = `${Math.min(164, Math.max(44, element.scrollHeight))}px`;
  }, [draft]);
  useLayoutEffect(() => {
    if (nearBottom.current && !loadingOlder.current) scrollEnd();
  }, [messages, thinking, recap]);
  useEffect(() => {
    const resize = new ResizeObserver(() => {
      if (nearBottom.current && !loadingOlder.current) scrollEnd();
    });
    if (viewport.current) resize.observe(viewport.current);
    if (content.current) resize.observe(content.current);
    return () => resize.disconnect();
  }, []);
  const send = async () => {
    if (sending.current || !draft.trim()) return;
    if (Array.from(draft.trim()).length > 32000) {
      setError('消息不能超过 32,000 个字符');
      return;
    }
    sending.current = true;
    try {
      if (await controller.send(draft)) {
        setDraft('');
        setError('');
        nearBottom.current = true;
        input.current?.focus({ preventScroll: true });
      }
    } finally {
      sending.current = false;
    }
  };
  const older = async () => {
    const element = viewport.current;
    if (!element || loadingOlder.current || controller.loadingHistory || !controller.hasMoreHistory)
      return;
    loadingOlder.current = true;
    nearBottom.current = false;
    const height = element.scrollHeight,
      top = element.scrollTop;
    await controller.loadHistory(true);
    requestAnimationFrame(() => {
      element.scrollTop = top + element.scrollHeight - height;
      loadingOlder.current = false;
    });
  };
  const onScroll = (event: UIEvent<HTMLDivElement>) => {
    if (!userScrolling.current) return;
    const element = event.currentTarget;
    nearBottom.current = element.scrollHeight - element.scrollTop - element.clientHeight < 160;
    if (element.scrollTop < 160 && !nearBottom.current) void older();
  };
  return (
    <main className="chat-page">
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
        <div className="message-content" ref={content}>
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
              onClick={() => void older()}
            >
              {controller.loadingHistory ? '正在加载…' : '加载更早的消息'}
            </button>
          )}
          {messages.map((message, index) => {
            const previous = messages[index - 1],
              next = messages[index + 1];
            const showDate =
              !previous ||
              !sameDay(previous.createdAt, message.createdAt) ||
              Math.abs(message.createdAt.getTime() - previous.createdAt.getTime()) >= 30 * 60000;
            return (
              <div key={message.id}>
                {showDate && (
                  <p className="date">
                    <time dateTime={message.createdAt.toISOString()}>
                      {dateLabel(message.createdAt)} {timeLabel(message.createdAt)}
                    </time>
                  </p>
                )}
                <div
                  className={`message-row ${message.isUser ? 'user-row' : ''} ${previous && sameGroup(previous, message) ? 'joined' : ''}`}
                >
                  <article className="message-column" aria-label={message.isUser ? '你' : '阿芒'}>
                    <div
                      className={`bubble ${message.isUser ? 'user-bubble' : ''} ${previous && sameGroup(previous, message) ? 'join-top' : ''} ${next && sameGroup(message, next) ? '' : 'group-end'}`}
                    >
                      {message.isUser ? (
                        <p className="user-text">{message.text}</p>
                      ) : (
                        <Markdown text={message.text} />
                      )}
                    </div>
                    {message.isUser &&
                      (index === lastUser ||
                        message.delivery === 'sending' ||
                        message.delivery === 'failed') &&
                      (message.delivery === 'failed' ? (
                        <button
                          type="button"
                          className="retry error delivery"
                          onClick={() => controller.retry(message)}
                        >
                          ⓘ 发送失败，点击重发
                        </button>
                      ) : (
                        <span className="delivery">
                          {message.delivery === 'sending'
                            ? '发送中…'
                            : message.delivery === 'read'
                              ? '已读'
                              : '已送达'}
                        </span>
                      ))}
                  </article>
                </div>
              </div>
            );
          })}
          {thinking && (
            <div
              className={recap ? 'recap' : 'thinking'}
              role="status"
              aria-label={recap || '阿芒正在处理'}
            >
              {recap ? (
                <span className="recap-text">{recap}</span>
              ) : (
                <div className="thinking-dots" aria-hidden="true">
                  <span className="dot" />
                  <span className="dot dot-two" />
                  <span className="dot dot-three" />
                </div>
              )}
            </div>
          )}
        </div>
      </div>
      {error && (
        <p className="error composer-error" role="alert">
          {error}
        </p>
      )}
      <div className="composer-padding">
        <form
          className="composer"
          onSubmit={(event) => {
            event.preventDefault();
            void send();
          }}
        >
          <textarea
            ref={input}
            id="message-input"
            className="message-input"
            aria-label="消息"
            placeholder="发消息给阿芒"
            value={draft}
            maxLength={128000}
            rows={1}
            enterKeyHint="send"
            onChange={(event) => {
              setDraft(event.target.value);
              setError('');
            }}
            onKeyDown={(event) => {
              if (
                event.key === 'Enter' &&
                !event.shiftKey &&
                !event.ctrlKey &&
                !event.metaKey &&
                !event.nativeEvent.isComposing &&
                event.nativeEvent.keyCode !== 229
              ) {
                event.preventDefault();
                void send();
              }
            }}
          />
          <button
            type="submit"
            className="send-button"
            aria-label="发送消息"
            disabled={!draft.trim()}
          >
            <span className="send-arrow" aria-hidden="true">
              ↑
            </span>
          </button>
        </form>
      </div>
    </main>
  );
}

function sameDay(a: Date, b: Date) {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}
function sameGroup(a: ChatMessage, b: ChatMessage) {
  return (
    a.isUser === b.isUser &&
    sameDay(a.createdAt, b.createdAt) &&
    Math.abs(a.createdAt.getTime() - b.createdAt.getTime()) < 120000
  );
}
function timeLabel(date: Date) {
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
}
function dateLabel(date: Date) {
  const today = new Date(),
    yesterday = new Date();
  yesterday.setDate(today.getDate() - 1);
  return sameDay(date, today)
    ? '今天'
    : sameDay(date, yesterday)
      ? '昨天'
      : `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
}
