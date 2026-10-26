import { useEffect, useRef, useState } from '@lynx-js/react';
import type { ScrollEvent } from '@lynx-js/types';
import { ChatController } from './chat.js';
import type { ChatMessage } from './chat.js';
import { Markdown } from './Markdown.js';
import { host } from './platform.js';
import './style.css';

const controller = new ChatController();

export function App() {
  const [, refresh] = useState(0);
  useEffect(() => {
    const unsubscribe = controller.subscribe(() => refresh((value) => value + 1));
    void controller.initialize();
    const online = () => controller.reconnect();
    lynx.getJSModule('GlobalEventEmitter').addListener('aetherOnline', online);
    return () => {
      unsubscribe();
      controller.dispose();
      lynx.getJSModule('GlobalEventEmitter').removeListener('aetherOnline', online);
    };
  }, []);
  if (controller.initializing)
    return (
      <view className="loading">
        <text className="muted">正在加载…</text>
      </view>
    );
  return controller.token ? <Chat /> : <Login />;
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
  useEffect(() => {
    const action = () => void submit();
    lynx.getJSModule('GlobalEventEmitter').addListener('aetherSubmit', action);
    return () => lynx.getJSModule('GlobalEventEmitter').removeListener('aetherSubmit', action);
  }, [email, code, sent, devLogin]);
  return (
    <scroll-view scroll-orientation="vertical" className="login-page">
      <view className="login-center">
        <view className="login-card">
          <image className="mark" src="/app-icon.png" mode="aspectFit" accessibility-label="阿芒" />
          <text className="login-title">和阿芒聊聊</text>
          <text className="login-subtitle">
            {devLogin
              ? '本地开发环境，一键进入测试对话。'
              : sent
                ? '输入验证码，继续你的对话。'
                : '用邮箱登录，开启你们的对话。'}
          </text>
          {!devLogin && (
            <input
              id="email"
              className="login-input"
              type="email"
              placeholder="邮箱地址"
              accessibility-label="邮箱地址"
              default-value={email}
              readonly={sent}
              maxlength={254}
              bindinput={(event) => setEmail(event.detail.value)}
            />
          )}
          {!devLogin && sent && (
            <input
              id="code"
              className="login-input"
              type="number"
              placeholder="6 位数字"
              accessibility-label="验证码"
              default-value={code}
              maxlength={6}
              bindinput={(event) => setCode(event.detail.value.replace(/\D/g, '').slice(0, 6))}
            />
          )}
          {(error || controller.error) && (
            <text className="error login-error">{error || controller.error}</text>
          )}
          <view
            accessibility-element={true}
            accessibility-role="button"
            accessibility-label={buttonLabel}
            className={`login-button ${busy ? 'busy' : ''}`}
            bindtap={() => void submit()}
          >
            <text className="button-label">{busy ? '请稍候…' : buttonLabel}</text>
          </view>
          {!devLogin && sent && (
            <view
              className="change-email"
              accessibility-element={true}
              accessibility-role="button"
              bindtap={() => {
                if (!busy) {
                  setSent(false);
                  setError('');
                  setCode('');
                }
              }}
            >
              <text className="link">更换邮箱或重新获取</text>
            </view>
          )}
        </view>
      </view>
    </scroll-view>
  );
}

function Chat() {
  const [draft, setDraft] = useState('');
  const [inputVersion, setInputVersion] = useState(0);
  const [error, setError] = useState('');
  const nearBottom = useRef(true);
  const loadingOlder = useRef(false);
  const userScrolling = useRef(false);
  const scrollTop = useRef(0);
  const scrollHeight = useRef(0);
  const viewportHeight = useRef(0);
  const sending = useRef(false);
  const recap = controller.activity.state === 'working' ? controller.activity.summary : '';
  const thinking =
    controller.connected && (controller.mainThinking || controller.activity.state === 'working');
  const messages = controller.messages;
  const lastUser = messages.reduce((last, message, index) => (message.isUser ? index : last), -1);
  const scrollEnd = () => {
    userScrolling.current = false;
    lynx
      .createSelectorQuery()
      .select('#message-end')
      .invoke({ method: 'scrollIntoView', params: { block: 'end', behavior: 'instant' } })
      .exec();
  };
  const send = async () => {
    if (sending.current) return;
    if (Array.from(draft.trim()).length > 32000) {
      setError('消息不能超过 32,000 个字符');
      return;
    }
    sending.current = true;
    try {
      if (await controller.send(draft)) {
        setDraft('');
        setError('');
        setInputVersion((value) => value + 1);
        nearBottom.current = true;
        setTimeout(() => {
          scrollEnd();
          lynx.createSelectorQuery().select('#message-input').invoke({ method: 'focus' }).exec();
        }, 0);
      }
    } finally {
      sending.current = false;
    }
  };
  useEffect(() => {
    const submit = () => void send();
    const resize = () => {
      if (nearBottom.current) scrollEnd();
    };
    lynx.getJSModule('GlobalEventEmitter').addListener('aetherSubmit', submit);
    lynx.getJSModule('GlobalEventEmitter').addListener('aetherResize', resize);
    return () => {
      lynx.getJSModule('GlobalEventEmitter').removeListener('aetherSubmit', submit);
      lynx.getJSModule('GlobalEventEmitter').removeListener('aetherResize', resize);
    };
  }, [draft]);
  useEffect(() => {
    if (nearBottom.current && !loadingOlder.current) setTimeout(scrollEnd, 0);
  }, [messages.length, thinking, recap]);
  const older = async () => {
    if (loadingOlder.current || controller.loadingHistory || !controller.hasMoreHistory) return;
    loadingOlder.current = true;
    const height = scrollHeight.current,
      top = scrollTop.current;
    await controller.loadHistory(true);
    setTimeout(() => {
      lynx
        .createSelectorQuery()
        .select('#message-content')
        .invoke({
          method: 'boundingClientRect',
          success: (value: unknown) => {
            if (
              typeof value === 'object' &&
              value &&
              'height' in value &&
              typeof value.height === 'number'
            ) {
              lynx
                .createSelectorQuery()
                .select('#messages')
                .invoke({
                  method: 'scrollTo',
                  params: { offset: top + value.height - height, smooth: false },
                })
                .exec();
            }
            loadingOlder.current = false;
          },
          fail: () => {
            loadingOlder.current = false;
          },
        })
        .exec();
    }, 0);
  };
  const onScroll = (event: ScrollEvent) => {
    const previous = scrollTop.current;
    scrollTop.current = Math.max(0, event.detail.scrollTop);
    scrollHeight.current = event.detail.scrollHeight;
    nearBottom.current =
      event.detail.scrollHeight - scrollTop.current - viewportHeight.current < 160;
    if (userScrolling.current && scrollTop.current < previous && scrollTop.current < 160)
      void older();
  };
  useEffect(() => {
    lynx
      .createSelectorQuery()
      .select('#messages')
      .invoke({
        method: 'boundingClientRect',
        success: (value: unknown) => {
          if (
            typeof value === 'object' &&
            value &&
            'height' in value &&
            typeof value.height === 'number'
          )
            viewportHeight.current = value.height;
        },
      })
      .exec();
  }, []);
  return (
    <view className="chat-page">
      <view className="chat-toolbar">
        <text className="chat-name">阿芒</text>
        <view
          className="desktop-button"
          accessibility-element={true}
          accessibility-role="button"
          accessibility-label="查看沙盒桌面"
          bindtap={() => {
            if (controller.token) host.openDesktop(controller.token);
          }}
        >
          <text className="desktop-button-label">▣ 查看桌面</text>
        </view>
      </view>
      <scroll-view
        id="messages"
        className="messages"
        scroll-orientation="vertical"
        bindscroll={onScroll}
        bindwheel={() => {
          userScrolling.current = true;
        }}
        bindtouchstart={() => {
          userScrolling.current = true;
        }}
        bindlayoutchange={(event) => {
          viewportHeight.current = event.detail.height;
          if (nearBottom.current) scrollEnd();
        }}
        bindcontentsizechanged={() => {
          if (nearBottom.current && !loadingOlder.current) scrollEnd();
        }}
      >
        <view id="message-content" className="message-content">
          {controller.error && (
            <view
              className="history-error"
              accessibility-element={true}
              accessibility-role="button"
              bindtap={() => void controller.loadHistory()}
            >
              <text className="error">{controller.error}</text>
            </view>
          )}
          {messages.map((message, index) => {
            const previous = messages[index - 1],
              next = messages[index + 1];
            const showDate =
              !previous ||
              !sameDay(previous.createdAt, message.createdAt) ||
              Math.abs(message.createdAt.getTime() - previous.createdAt.getTime()) >= 30 * 60000;
            return (
              <view key={message.id}>
                {showDate && (
                  <text className="date">
                    {dateLabel(message.createdAt)} {timeLabel(message.createdAt)}
                  </text>
                )}
                <view
                  className={`message-row ${message.isUser ? 'user-row' : ''} ${previous && sameGroup(previous, message) ? 'joined' : ''}`}
                >
                  <view className="message-column">
                    <view
                      className={`bubble ${message.isUser ? 'user-bubble' : ''} ${previous && sameGroup(previous, message) ? 'join-top' : ''} ${next && sameGroup(message, next) ? '' : 'group-end'}`}
                    >
                      {message.isUser ? (
                        <text className="user-text" text-selection={true}>
                          {message.text}
                        </text>
                      ) : (
                        <Markdown text={message.text} />
                      )}
                    </view>
                    {message.isUser &&
                      (index === lastUser ||
                        message.delivery === 'sending' ||
                        message.delivery === 'failed') &&
                      (message.delivery === 'failed' ? (
                        <view
                          className="retry"
                          accessibility-element={true}
                          accessibility-role="button"
                          accessibility-label="发送失败，点击重发"
                          bindtap={() => controller.retry(message)}
                        >
                          <text className="error delivery">ⓘ 发送失败，点击重发</text>
                        </view>
                      ) : (
                        <text className="delivery">
                          {message.delivery === 'sending'
                            ? '发送中…'
                            : message.delivery === 'read'
                              ? '已读'
                              : '已送达'}
                        </text>
                      ))}
                  </view>
                </view>
              </view>
            );
          })}
          {thinking && (
            <view
              className={recap ? 'recap' : 'thinking'}
              accessibility-element={true}
              accessibility-label={recap || '阿芒正在处理'}
            >
              {recap ? (
                <text className="recap-text">{recap}</text>
              ) : (
                <view className="thinking-dots">
                  <view className="dot dot-one" />
                  <view className="dot dot-two" />
                  <view className="dot dot-three" />
                </view>
              )}
            </view>
          )}
          <view id="message-end" style={{ height: '1px' }} />
        </view>
      </scroll-view>
      {error && <text className="error composer-error">{error}</text>}
      <view className="composer-padding">
        <view className="composer">
          <textarea
            key={inputVersion}
            id="message-input"
            className="message-input"
            accessibility-label="消息"
            placeholder="发消息给阿芒"
            maxlength={128000}
            maxlines={6}
            bindinput={(event) => {
              setDraft(event.detail.value);
              setError('');
              host.resizeComposer();
            }}
          />
          <view
            className={`send-button ${draft.trim() ? '' : 'send-disabled'}`}
            accessibility-element={true}
            accessibility-role="button"
            accessibility-label="发送消息"
            bindtap={() => {
              if (draft.trim()) void send();
            }}
          >
            <text className="send-arrow">↑</text>
          </view>
        </view>
      </view>
    </view>
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
