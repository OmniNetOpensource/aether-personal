// 本文件是管理看板配置页的 ChatGPT 订阅接入卡片：发起 Sign in with ChatGPT 授权链接、提交回环回调 URL 换票，并支持断开连接。

import { useState } from 'react';
import { useAtomSet, useAtomValue } from '@effect/atom-react';
import { Exit, Option } from 'effect';
import { AsyncResult } from 'effect/reactivity';
import { extractAdminHttpError, formatTime } from './api.js';
import type { ChatGPTAuthStart, ChatGPTStatus } from './api.js';
import {
  chatGPTStatusAtom,
  completeChatGPTAuthAtom,
  disconnectChatGPTAtom,
  notifyToastAtom,
  startChatGPTAuthAtom,
} from './state.js';
import { Empty } from './ui.js';

const statusLabels: Record<ChatGPTStatus['status'], string> = {
  connected: '已连接',
  not_configured: '未连接',
  unreadable: '无法解密',
};

// ChatGPTSubscriptionPage 订阅部署级 ChatGPT OAuth 连接状态；刷新失败时保留上次成功结果。
export function ChatGPTSubscriptionPage() {
  const result = useAtomValue(chatGPTStatusAtom);
  const status = AsyncResult.isSuccess(result)
    ? result.value
    : AsyncResult.isFailure(result) && Option.isSome(result.previousSuccess)
      ? result.previousSuccess.value.value
      : null;
  const loadError = AsyncResult.isFailure(result);

  if (loadError && !status) {
    return <Empty title="无法读取 ChatGPT 订阅状态" description="连接恢复后可重新加载。" />;
  }
  if (!status) return <div className="loading-card">正在读取 ChatGPT 订阅状态…</div>;

  return (
    <>
      {loadError ? (
        <div className="load-error" role="alert">
          暂时无法读取 ChatGPT 订阅状态，请刷新重试。
        </div>
      ) : null}
      <section className="model-intro">
        <div className="eyebrow teal">CHATGPT SUBSCRIPTION</div>
        <h2>ChatGPT 订阅</h2>
        <p>
          通过 Sign in with ChatGPT（OAuth 2.0 + PKCE）连接维护者的 ChatGPT Plus / Pro
          订阅作为部署级模型渠道。连接后后端自动在令牌过期前轮换刷新，可用模型会并入 App
          的模型选择器；请注意仅在受控自用场景下开启。
        </p>
      </section>
      <div className="model-role-grid service-settings-grid">
        <ChatGPTSubscriptionCard status={status} />
      </div>
    </>
  );
}

// ChatGPTSubscriptionCard 管理一次完整的连接、粘贴回调 URL 换票与二次确认断开流程。
function ChatGPTSubscriptionCard({ status }: { status: ChatGPTStatus }) {
  const startAuth = useAtomSet(startChatGPTAuthAtom, { mode: 'promiseExit' });
  const completeAuth = useAtomSet(completeChatGPTAuthAtom, { mode: 'promiseExit' });
  const disconnect = useAtomSet(disconnectChatGPTAtom, { mode: 'promiseExit' });
  const notify = useAtomSet(notifyToastAtom);

  const [authStart, setAuthStart] = useState<ChatGPTAuthStart | null>(null);
  const [callbackURL, setCallbackURL] = useState('');
  const [confirmDisconnect, setConfirmDisconnect] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  // 向后端生成一次性 state、nonce 与 PKCE 挑战，并返回官方授权链接。
  async function handleStart() {
    if (busy) return;
    setError('');
    setConfirmDisconnect(false);
    setBusy(true);
    try {
      const exit = await startAuth(undefined);
      if (Exit.isFailure(exit)) {
        setError('生成授权链接失败，请刷新后重试。');
        return;
      }
      setAuthStart(exit.value);
      setCallbackURL('');
    } finally {
      setBusy(false);
    }
  }

  // 提交管理员从浏览器地址栏复制的回环回调 URL 完成换票。
  async function handleComplete(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || !callbackURL.trim()) return;
    setError('');
    setBusy(true);
    try {
      const exit = await completeAuth(callbackURL.trim());
      if (Exit.isFailure(exit)) {
        const httpError = extractAdminHttpError(exit.cause);
        setError(
          httpError?.status === 400
            ? '回调 URL 无效或授权会话已过期，请核对是否粘贴了完整地址，或重新生成授权链接。'
            : httpError?.status === 502
              ? '向 OpenAI 换取令牌失败，请重新生成授权链接后重试。'
              : '未能确认连接结果，请刷新后重试。',
        );
        return;
      }
      setAuthStart(null);
      setCallbackURL('');
      const label = exit.value.account_email || exit.value.account_sub || 'ChatGPT 账号';
      notify(`已连接 ${label}，刷新 App 模型列表即可选用。`);
    } finally {
      setBusy(false);
    }
  }

  // 复制授权链接到剪贴板，方便在另一台设备或无痕窗口打开。
  async function handleCopyLink() {
    if (!authStart) return;
    try {
      await navigator.clipboard.writeText(authStart.authorize_url);
      notify('已复制 OpenAI 授权链接。');
    } catch {
      notify('复制失败，请直接点击“打开 OpenAI 授权页”。');
    }
  }

  // 二次确认后清除令牌与待完成授权流。
  async function handleDisconnect() {
    if (busy) return;
    setError('');
    setBusy(true);
    try {
      const exit = await disconnect(undefined);
      if (Exit.isFailure(exit)) {
        setError('断开连接失败，请刷新后重试。');
        return;
      }
      setAuthStart(null);
      setCallbackURL('');
      notify('已断开 ChatGPT 订阅连接，App 将不再列出该渠道模型。');
    } finally {
      setBusy(false);
      setConfirmDisconnect(false);
    }
  }

  const badgeClass =
    status.status === 'connected'
      ? 'saved'
      : status.status === 'unreadable'
        ? 'unreadable'
        : 'not_configured';
  const accountDisplay = status.account_email || status.account_sub;

  return (
    <form
      className="panel model-role-card service-card"
      onSubmit={(event) => void handleComplete(event)}
    >
      <div className="service-heading">
        <h3>Sign in with ChatGPT</h3>
        <span className={`service-status ${badgeClass}`}>{statusLabels[status.status]}</span>
      </div>
      <p className="muted">
        使用 OpenAI 官方 Responses API 路由订阅额度，支持多实例并发自动刷新，令牌经 AES-GCM
        加密保存且永不回显。
      </p>
      <p className="service-current">
        {status.status === 'not_configured'
          ? '当前未连接 ChatGPT 订阅'
          : accountDisplay
            ? `已连接账号 · ${accountDisplay}`
            : '已连接账号'}
      </p>
      {status.status === 'connected' && status.access_expires_at ? (
        <p className="service-updated">
          当前访问令牌有效期至 {formatTime(status.access_expires_at)}（到期前自动刷新）
        </p>
      ) : null}
      <p className="service-updated">
        {status.updated_at ? `最近更新 ${formatTime(status.updated_at)}` : '尚未连接'}
      </p>
      {status.status === 'unreadable' ? (
        <p className="form-error">
          无法解密当前令牌。请核对部署加密主密钥，或点击下方“重新连接”重新授权。
        </p>
      ) : null}

      {authStart ? (
        <>
          <p className="muted">
            1. 点击下方打开 OpenAI 授权页并同意授权；2. 浏览器跳转到{' '}
            <code>{authStart.redirect_uri}</code> 时页面无法打开属正常现象；3.
            复制浏览器地址栏的完整 URL 粘贴到下方提交。
          </p>
          <div className="service-actions">
            <a
              className="button primary"
              href={authStart.authorize_url}
              target="_blank"
              rel="noopener noreferrer"
            >
              打开 OpenAI 授权页 ↗
            </a>
            <button
              type="button"
              className="button"
              disabled={busy}
              onClick={() => void handleCopyLink()}
            >
              复制授权链接
            </button>
            <button
              type="button"
              className="button"
              disabled={busy}
              onClick={() => {
                setAuthStart(null);
                setCallbackURL('');
                setError('');
              }}
            >
              取消
            </button>
          </div>
          <label className="model-picker" style={{ marginTop: '18px' }}>
            回调地址（从地址栏完整复制）
            <input
              type="url"
              autoComplete="off"
              spellCheck={false}
              required
              placeholder={`${authStart.redirect_uri}?code=...&state=...&client_id=...`}
              value={callbackURL}
              disabled={busy}
              onChange={(event) => setCallbackURL(event.target.value)}
            />
          </label>
        </>
      ) : null}

      <div className="form-error" role="alert">
        {error}
      </div>

      <div className="service-actions">
        {authStart ? (
          <button
            type="submit"
            className="button primary"
            disabled={busy || !callbackURL.trim()}
          >
            {busy ? '正在换票…' : '完成连接'}
          </button>
        ) : (
          <button
            type="button"
            className="button primary"
            disabled={busy}
            onClick={() => void handleStart()}
          >
            {busy ? '正在生成…' : status.status === 'not_configured' ? '开始连接' : '重新连接'}
          </button>
        )}
        {status.status !== 'not_configured' ? (
          confirmDisconnect ? (
            <>
              <button
                type="button"
                className="button danger"
                disabled={busy}
                onClick={() => void handleDisconnect()}
              >
                确认断开
              </button>
              <button
                type="button"
                className="button"
                disabled={busy}
                onClick={() => setConfirmDisconnect(false)}
              >
                取消
              </button>
            </>
          ) : (
            <button
              type="button"
              className="button"
              disabled={busy}
              onClick={() => setConfirmDisconnect(true)}
            >
              断开连接
            </button>
          )
        ) : null}
      </div>
    </form>
  );
}
