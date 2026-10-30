// 本文件收集邮箱与验证码，生产发码前完成 Turnstile 安全验证；dev 保留一键登录。

import { useEffect, useRef, useState } from 'react';
import type { ChatController } from './ChatController.js';

// Turnstile 由 Cloudflare 官方脚本提供，页面只持有公开 site key 与一次性令牌。
interface TurnstileAPI {
  render(container: HTMLElement, options: {
    sitekey: string;
    action: string;
    theme: 'auto';
    size: 'flexible';
    callback: (token: string) => void;
    'expired-callback': () => void;
    'error-callback': () => void;
  }): string;
  remove(widgetID: string): void;
}

declare global {
  interface Window {
    turnstile?: TurnstileAPI;
  }
}

// LoginPage 管理本次登录表单与安全验证生命周期；令牌不写入浏览器存储。
export function LoginPage({ controller }: { controller: ChatController }) {
  const [email, setEmail] = useState(controller.email);
  const [code, setCode] = useState('');
  const [sent, setSent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const submitting = useRef(false);
  const challengeContainer = useRef<HTMLDivElement>(null);
  const [challengeToken, setChallengeToken] = useState('');
  const [challengeVersion, setChallengeVersion] = useState(0);
  const devLogin = controller.devLoginAvailable;
  const siteKey = controller.turnstileSiteKey;
  // 显式渲染适配 React 挂载；重试时重建组件，避免重复提交已消费的令牌。
  useEffect(() => {
    setChallengeToken('');
    if (devLogin || sent || !siteKey) return;
    let disposed = false;
    let widgetID: string | undefined;
    let script = document.querySelector<HTMLScriptElement>('script[data-aether-turnstile]');
    const failed = () => {
      if (disposed) return;
      setChallengeToken('');
      setError('安全验证加载失败，请刷新页面重试');
    };
    const render = () => {
      if (disposed || !window.turnstile || !challengeContainer.current) return;
      widgetID = window.turnstile.render(challengeContainer.current, {
        sitekey: siteKey,
        action: 'request_code',
        theme: 'auto',
        size: 'flexible',
        callback: (token) => { if (!disposed) setChallengeToken(token); },
        'expired-callback': () => { if (!disposed) setChallengeToken(''); },
        'error-callback': failed,
      });
    };
    if (!script) {
      script = document.createElement('script');
      script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';
      script.async = true;
      script.defer = true;
      script.dataset.aetherTurnstile = '';
      document.head.append(script);
    }
    script.addEventListener('load', render);
    script.addEventListener('error', failed);
    if (window.turnstile) render();
    return () => {
      disposed = true;
      script.removeEventListener('load', render);
      script.removeEventListener('error', failed);
      if (widgetID !== undefined) window.turnstile?.remove(widgetID);
    };
  }, [devLogin, sent, siteKey, challengeVersion]);
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
    if (!devLogin && !sent && !challengeToken) {
      setError(siteKey ? '请先完成安全验证' : '登录服务暂不可用，请稍后再试');
      return;
    }
    submitting.current = true;
    setBusy(true);
    setError('');
    try {
      if (devLogin) await controller.devSignIn();
      else if (sent) await controller.signIn(email, code);
      else {
        try {
          await controller.requestCode(email, challengeToken);
          setSent(true);
        } finally {
          setChallengeToken('');
          setChallengeVersion((version) => version + 1);
        }
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
          {!devLogin && !sent && siteKey && <div ref={challengeContainer} />}
          {!devLogin && !sent && !siteKey && (
            <p className="error login-error" role="alert">登录服务暂不可用，请稍后再试</p>
          )}
          {(error || controller.error) && (
            <p className="error login-error" role="alert">
              {error || controller.error}
            </p>
          )}
          <button className="login-button" type="submit" disabled={busy || (!devLogin && !sent && !challengeToken)}>
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
