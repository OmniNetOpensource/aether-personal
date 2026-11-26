// 本文件实现 /oauth/authorize 路由的第三方记忆授权页：校验客户端请求、未登录时内嵌原登录流程、确认后跳回外部 MCP 客户端。

import { useEffect, useMemo, useRef, useState } from 'react';
import { AuthError } from '../../lib/api.js';
import { ChatController } from '../../lib/ChatController.js';
import { Link } from '@tanstack/react-router';
import {
  fetchOAuthAuthorizeContext,
  readOAuthAuthorizeParams,
  submitOAuthAuthorizeDecision,
} from '../../lib/oauth.js';
import type { OAuthAuthorizeContext } from '../../lib/oauth.js';
import { clearSession, readSession } from '../../lib/platform.js';
import { LoadingScreen } from '../LoadingScreen.js';
import { LoginPage } from '../login/LoginPage.js';
import './oauth.css';

// OAuthAuthorizePage 负责远程记忆 MCP 的 OAuth 2.1 授权交互，不依赖 Cookie 而是复用本地会话令牌。
export function OAuthAuthorizePage() {
  const search = window.location.search;
  const params = useMemo(() => readOAuthAuthorizeParams(search), [search]);
  const [contextLoading, setContextLoading] = useState(true);
  const [contextError, setContextError] = useState('');
  const [authContext, setAuthContext] = useState<OAuthAuthorizeContext | null>(null);
  const [sessionToken, setSessionToken] = useState<string | null>(() => readSession().token);
  const [sessionEmail, setSessionEmail] = useState<string>(() => readSession().email);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState('');

  // 页面加载时先由服务端核验 client_id 与 redirect_uri，再决定展示授权页、报错或直接回跳。
  useEffect(() => {
    const abortController = new AbortController();
    setContextLoading(true);
    setContextError('');
    void fetchOAuthAuthorizeContext(search, abortController.signal)
      .then((result) => {
        if (abortController.signal.aborted) return;
        if (result.redirect_to) {
          window.location.replace(result.redirect_to);
          return;
        }
        if (result.error) {
          setContextError(result.error);
          setContextLoading(false);
          return;
        }
        setAuthContext(result);
        setContextLoading(false);
      })
      .catch((error: unknown) => {
        if (abortController.signal.aborted) return;
        setContextError(error instanceof Error ? error.message : '无法加载授权信息');
        setContextLoading(false);
      });
    return () => abortController.abort();
  }, [search]);

  if (contextLoading) {
    return <LoadingScreen />;
  }

  if (contextError || !authContext) {
    return (
      <main className="oauth-page">
        <section className="oauth-card" role="alert">
          <div className="oauth-header">
            <span className="oauth-badge">aether Memory MCP</span>
            <h1>无法完成授权</h1>
            <p>{contextError || '授权请求无效'}</p>
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

  // 未登录或旧会话失效时在当前页内嵌现有邮箱登录流程，登录完成后自动回到授权确认卡片。
  if (!sessionToken) {
    return (
      <EmbeddedAuthorizeLogin
        onAuthenticated={(token, email) => {
          setSessionToken(token);
          setSessionEmail(email);
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
      const redirectTo = await submitOAuthAuthorizeDecision(sessionToken, params, approve);
      window.location.replace(redirectTo);
    } catch (error: unknown) {
      if (error instanceof AuthError) {
        clearSession();
        setSessionToken(null);
        setSessionEmail('');
        setSubmitting(false);
        return;
      }
      setSubmitError(error instanceof Error ? error.message : '提交授权失败，请重试');
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

// EmbeddedAuthorizeLogin 复用现有聊天 WebSocket 登录流程（含生产 Turnstile 与开发自动登录），登录成功后回传令牌。
function EmbeddedAuthorizeLogin({
  onAuthenticated,
}: {
  onAuthenticated: (token: string, email: string) => void;
}) {
  const [controller, setController] = useState<ChatController | null>(null);
  const [, refresh] = useState(0);
  const onAuthenticatedRef = useRef(onAuthenticated);
  onAuthenticatedRef.current = onAuthenticated;

  useEffect(() => {
    const nextController = new ChatController();
    setController(nextController);
    const unsubscribe = nextController.subscribe(() => {
      if (nextController.token) {
        onAuthenticatedRef.current(nextController.token, nextController.email);
        return;
      }
      refresh((value) => value + 1);
    });
    void nextController.initialize();
    return () => {
      unsubscribe();
      nextController.dispose();
    };
  }, []);

  if (!controller || controller.initializing) {
    return <LoadingScreen />;
  }
  return <LoginPage controller={controller} />;
}
