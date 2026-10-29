// 本文件是登录页组件：收集邮箱与验证码（dev 环境为一键登录），调用聊天控制器完成登录并展示错误。

import { useRef, useState } from 'react';
import type { ChatController } from './ChatController.js';

export function LoginPage({ controller }: { controller: ChatController }) {
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
