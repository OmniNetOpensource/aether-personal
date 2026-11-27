// 本文件实现 /oauth/authorize 路由的第三方记忆授权页：由 Effect Atom 核验授权上下文、内嵌登录并提交授权决策。

import { useEffect, useMemo, useState } from 'react';
import { useAtomMount, useAtomSet, useAtomValue } from '@effect/atom-react';
import { Link } from '@tanstack/react-router';
import { Exit } from 'effect';
import { AsyncResult } from 'effect/reactivity';
import { formatCauseError, isAuthFailure } from '../../lib/errors.js';
import { readOAuthAuthorizeParams } from '../../lib/oauth.js';
import {
  browserSessionAtom,
  chatSessionAtom,
  chatStateAtom,
  clearBrowserSessionAtom,
  oauthAuthorizeContextAtom,
  submitOAuthDecisionAtom,
} from '../../lib/runtime.js';
import { LoadingScreen } from '../LoadingScreen.js';
import { LoginPage } from '../login/LoginPage.js';
import './oauth.css';

// OAuthAuthorizePage 负责远程记忆 MCP 的 OAuth 2.1 授权交互，不依赖 Cookie 而是复用本地会话令牌。
export function OAuthAuthorizePage() {
  const search = window.location.search;
  const params = useMemo(() => readOAuthAuthorizeParams(search), [search]);
  const contextResult = useAtomValue(oauthAuthorizeContextAtom(search));
  const sessionResult = useAtomValue(browserSessionAtom);
  const clearSession = useAtomSet(clearBrowserSessionAtom, { mode: 'promiseExit' });
  const submitDecision = useAtomSet(submitOAuthDecisionAtom, { mode: 'promiseExit' });

  const [overrideSession, setOverrideSession] = useState<{
    readonly token: string | null;
    readonly email: string;
  } | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState('');

  // 服务端返回直接错误回调地址时立刻跳回外部客户端。
  useEffect(() => {
    if (AsyncResult.isSuccess(contextResult) && contextResult.value.redirect_to) {
      window.location.replace(contextResult.value.redirect_to);
    }
  }, [contextResult]);

  if (AsyncResult.isInitial(contextResult) || AsyncResult.isInitial(sessionResult)) {
    return <LoadingScreen />;
  }

  if (
    AsyncResult.isFailure(contextResult) ||
    contextResult.value.redirect_to ||
    contextResult.value.error
  ) {
    if (AsyncResult.isSuccess(contextResult) && contextResult.value.redirect_to) {
      return <LoadingScreen />;
    }
    const contextError = AsyncResult.isFailure(contextResult)
      ? formatCauseError(contextResult.cause, '无法加载授权信息')
      : contextResult.value.error || '授权请求无效';
    return (
      <main className="oauth-page">
        <section className="oauth-card" role="alert">
          <div className="oauth-header">
            <span className="oauth-badge">aether Memory MCP</span>
            <h1>无法完成授权</h1>
            <p>{contextError}</p>
          </div>
          <div className="oauth-actions">
            <Link to="/" className="oauth-btn-primary">
              返回 aether
            </Link>
          </div>
        </section>
      </main>
    );
  }

  const authContext = contextResult.value;
  const baseSession = AsyncResult.isSuccess(sessionResult) ? sessionResult.value : null;
  const sessionToken = overrideSession ? overrideSession.token : (baseSession?.token ?? null);
  const sessionEmail = overrideSession ? overrideSession.email : (baseSession?.email ?? '');

  // 未登录或旧会话失效时在当前页内嵌现有邮箱登录流程，登录完成后自动回到授权确认卡片。
  if (!sessionToken) {
    return (
      <EmbeddedAuthorizeLogin
        onAuthenticated={(token, email) => {
          setOverrideSession({ token, email });
          setSubmitError('');
        }}
      />
    );
  }

  const handleDecision = async (approve: boolean) => {
    if (submitting) return;
    setSubmitting(true);
    setSubmitError('');
    try {
      const exit = await submitDecision({ token: sessionToken, params, approve });
      if (Exit.isFailure(exit)) {
        if (isAuthFailure(exit.cause)) {
          await clearSession(undefined);
          setOverrideSession({ token: null, email: '' });
          setSubmitting(false);
          return;
        }
        setSubmitError(formatCauseError(exit.cause, '提交授权失败，请重试'));
        setSubmitting(false);
        return;
      }
      window.location.replace(exit.value);
    } catch {
      setSubmitting(false);
    }
  };

  return (
    <main className="oauth-page">
      <section className="oauth-card" aria-labelledby="oauth-authorize-title">
        <div className="oauth-header">
          <span className="oauth-badge">aether Memory MCP</span>
          <h1 id="oauth-authorize-title">授权访问你的个人记忆</h1>
          <p>确认后，该外部应用将与 aether 实时共享你的长期记忆文件。</p>
        </div>

        <div className="oauth-client-box">
          <span className="oauth-client-name">{authContext.client_name || '外部应用'}</span>
          {authContext.redirect_host && (
            <span className="oauth-client-host">回调域名：{authContext.redirect_host}</span>
          )}
        </div>

        <ul className="oauth-scope-list">
          <li>允许查看、创建、编辑、重命名和删除 /memories 下的记忆文件。</li>
          <li>无法访问你的聊天历史、模型设置或沙箱电脑。</li>
          <li>你随时可以在 /memories 页面的「MCP 接入」中撤销此授权。</li>
        </ul>

        {sessionEmail && (
          <p className="oauth-account-row">
            当前登录账号：<strong>{sessionEmail}</strong>
          </p>
        )}

        {submitError && (
          <p className="oauth-error" role="alert">
            {submitError}
          </p>
        )}

        <div className="oauth-actions">
          <button
            type="button"
            className="oauth-btn-secondary"
            disabled={submitting}
            onClick={() => void handleDecision(false)}
          >
            拒绝
          </button>
          <button
            type="button"
            className="oauth-btn-primary"
            disabled={submitting}
            onClick={() => void handleDecision(true)}
          >
            {submitting ? '正在跳转…' : '允许授权'}
          </button>
        </div>
      </section>
    </main>
  );
}

// EmbeddedAuthorizeLogin 复用现有聊天 WebSocket 登录流程，登录成功后回传令牌并卸载释放会话 Scope。
function EmbeddedAuthorizeLogin({
  onAuthenticated,
}: {
  onAuthenticated: (token: string, email: string) => void;
}) {
  useAtomMount(chatSessionAtom);
  const initializing = useAtomValue(chatStateAtom, (state) => state.initializing);
  const token = useAtomValue(chatStateAtom, (state) => state.token);
  const email = useAtomValue(chatStateAtom, (state) => state.email);

  useEffect(() => {
    if (token) {
      onAuthenticated(token, email);
    }
  }, [token, email, onAuthenticated]);

  if (initializing) {
    return <LoadingScreen />;
  }
  return <LoginPage />;
}
