// 本文件展示「已分享记录」管理浮层：列出当前账号创建过的公开分享链接，支持一键复制链接、打开查看与随时取消分享。

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { ChatController } from '../../lib/ChatController.js';
import type { MessageShareSummary } from '../../lib/serverEvents.js';
import './chatShare.css';

// buildShareURL 根据当前站点域名拼出完整的公开分享地址。
export function buildShareURL(token: string): string {
  return `${window.location.origin}/s/${token}`;
}

// formatShareTime 将分享创建时间格式化为简短中文时间，方便在管理列表中区分不同批次。
function formatShareTime(isoString: string): string {
  const date = new Date(isoString);
  const now = new Date();
  const sameYear = date.getFullYear() === now.getFullYear();
  return date.toLocaleString('zh-CN', {
    ...(sameYear ? {} : { year: 'numeric' }),
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}

// ChatShareSheet 使用原生模态对话框加载并管理历史分享记录，撤销后对应公开链接立即失效。
export function ChatShareSheet({
  controller,
  onClose,
}: {
  controller: ChatController;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const closeButton = useRef<HTMLButtonElement>(null);
  const [shares, setShares] = useState<MessageShareSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [reloadCount, setReloadCount] = useState(0);
  const [revokingID, setRevokingID] = useState<string | null>(null);
  const [copiedID, setCopiedID] = useState<string | null>(null);

  useLayoutEffect(() => {
    const element = dialog.current;
    element?.showModal();
    closeButton.current?.focus({ preventScroll: true });
    return () => element?.close();
  }, []);

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError('');
    void controller.listShares().then(
      (items) => {
        if (!active) return;
        setShares(items);
        setLoading(false);
      },
      (reason: unknown) => {
        if (!active) return;
        setError(reason instanceof Error ? reason.message : '分享列表加载失败，请重试');
        setLoading(false);
      },
    );
    return () => {
      active = false;
    };
  }, [controller, reloadCount]);

  // copyShareLink 将指定分享链接写入系统剪贴板并给出短暂反馈。
  const copyShareLink = async (share: MessageShareSummary) => {
    try {
      await navigator.clipboard.writeText(buildShareURL(share.token));
      setCopiedID(share.id);
      setTimeout(() => {
        setCopiedID((current) => (current === share.id ? null : current));
      }, 2000);
    } catch {
      setError('浏览器拒绝访问剪贴板，请点击「查看」后从地址栏复制');
    }
  };

  // revokeShare 请求后端删除分享记录，成功后立即从列表中移除。
  const revokeShare = async (share: MessageShareSummary) => {
    if (revokingID) return;
    setRevokingID(share.id);
    setError('');
    try {
      await controller.deleteShare(share.id);
      setShares((current) => current.filter((item) => item.id !== share.id));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '取消分享失败，请稍后重试');
    } finally {
      setRevokingID(null);
    }
  };

  return (
    <dialog
      ref={dialog}
      className="share-sheet-dialog"
      aria-label="已分享记录"
      onKeyDown={(event) => {
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
        ) {
          onClose();
        }
      }}
    >
      <section className="share-sheet-panel">
        <header className="share-sheet-header">
          <div className="share-sheet-title">
            <h2>已分享记录</h2>
            <button
              ref={closeButton}
              type="button"
              className="share-sheet-close"
              onClick={onClose}
              aria-label="关闭已分享记录"
            >
              <span className="share-sheet-desktop-close" aria-hidden="true">
                ×
              </span>
              <span className="share-sheet-mobile-close" aria-hidden="true">
                关闭
              </span>
            </button>
          </div>
          <p className="share-sheet-subtitle">
            持有链接的人无需登录即可查看选中的对话；取消分享后链接立即失效
          </p>
        </header>

        <div className="share-sheet-body">
          {error && (
            <p className="error" role="alert" style={{ marginBottom: 10 }}>
              {error}
            </p>
          )}
          {loading ? (
            <p className="share-sheet-status" role="status">
              正在加载已分享记录…
            </p>
          ) : shares.length === 0 ? (
            <div className="share-sheet-empty">
              <p>还没有创建过分享链接。</p>
              <p>在聊天中勾选若干条消息并点击「生成并复制链接」即可生成公开打卡页。</p>
              {error && (
                <button
                  type="button"
                  className="share-btn-secondary"
                  style={{ marginTop: 12 }}
                  onClick={() => setReloadCount((value) => value + 1)}
                >
                  重新加载
                </button>
              )}
            </div>
          ) : (
            <ul className="share-sheet-list">
              {shares.map((share) => (
                <li key={share.id}>
                  <article className="share-card">
                    <div className="share-card-meta">
                      <span className="share-card-count">{share.message_count} 条消息</span>
                      <time dateTime={share.created_at}>{formatShareTime(share.created_at)}</time>
                    </div>
                    <p className="share-card-preview">{share.preview || '图片或文件附件消息'}</p>
                    <div className="share-card-actions">
                      <button
                        type="button"
                        className="share-card-action"
                        onClick={() => void copyShareLink(share)}
                      >
                        {copiedID === share.id ? '已复制' : '复制链接'}
                      </button>
                      <a
                        className="share-card-action"
                        href={`/s/${share.token}`}
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        查看
                      </a>
                      <button
                        type="button"
                        className="share-card-revoke"
                        disabled={revokingID !== null}
                        onClick={() => void revokeShare(share)}
                      >
                        {revokingID === share.id ? '取消中…' : '取消分享'}
                      </button>
                    </div>
                  </article>
                </li>
              ))}
            </ul>
          )}
        </div>

        <footer className="share-sheet-footer">
          <span>仅展示选中的消息与附件，不会公开你的账号邮箱</span>
          <span className="share-sheet-escape">
            <kbd>Esc</kbd> 关闭
          </span>
        </footer>
      </section>
    </dialog>
  );
}
