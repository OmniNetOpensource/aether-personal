// 本文件实现 /memories 路由的双栏只读记忆浏览页，由 Effect Atom 驱动文件清单、按需正文读取以及远程 MCP 接入管理弹窗。

import { useEffect, useMemo, useRef, useState } from 'react';
import { useAtomRefresh, useAtomSet, useAtomValue } from '@effect/atom-react';
import { Link } from '@tanstack/react-router';
import { Exit } from 'effect';
import { AsyncResult } from 'effect/reactivity';
import { formatCauseError, isAuthFailure } from '../../lib/errors.js';
import type { MemoryFileSummary } from '../../lib/memories.js';
import { formatFileSize } from '../../lib/messageAttachments.js';
import {
  browserSessionAtom,
  memoryFileContentAtom,
  memoryFilesAtom,
  oauthConnectionsAtom,
  revokeOAuthConnectionAtom,
} from '../../lib/runtime.js';
import { LoadingScreen } from '../LoadingScreen.js';
import { ModalDialog } from '../ModalDialog.js';
import { Markdown } from '../markdown/Markdown.js';
import './memories.css';

// MemoriesPage 读取当前账号的记忆文件列表；未登录或会话失效时引导返回对话登录。
export function MemoriesPage() {
  const sessionResult = useAtomValue(browserSessionAtom);
  const refreshSession = useAtomRefresh(browserSessionAtom);
  const [authExpired, setAuthExpired] = useState(false);

  if (AsyncResult.isInitial(sessionResult)) {
    return <LoadingScreen />;
  }

  const session = AsyncResult.isSuccess(sessionResult) ? sessionResult.value : null;
  if (!session?.token || authExpired) {
    return (
      <main className="memories-page">
        <MemoriesTitleBar
          email=""
          refreshing={false}
          showSidebarToggle={false}
          showMCPButton={false}
          sidebarOpen={false}
          onToggleSidebar={() => {}}
          onOpenMCP={() => {}}
          onRefresh={() => {
            setAuthExpired(false);
            refreshSession();
          }}
        />
        <div className="memories-center-state">
          <section className="memories-state-card">
            <h2>请先登录</h2>
            <p>记忆文件属于你的个人账号，返回主页登录后即可查看。</p>
            <Link to="/" className="memories-primary-action">
              返回登录 / 对话
            </Link>
          </section>
        </div>
      </main>
    );
  }

  return (
    <MemoriesWorkbench
      token={session.token}
      email={session.email}
      onAuthExpired={() => setAuthExpired(true)}
    />
  );
}

const emptyMemoryFiles: readonly MemoryFileSummary[] = [];

// MemoriesWorkbench 承载已登录状态下的记忆文件筛选、正文预览与 MCP 接入弹窗。
function MemoriesWorkbench({
  token,
  email,
  onAuthExpired,
}: {
  token: string;
  email: string;
  onAuthExpired: () => void;
}) {
  const filesAtom = memoryFilesAtom(token);
  const filesResult = useAtomValue(filesAtom);
  const refreshFiles = useAtomRefresh(filesAtom);

  const [activePath, setActivePath] = useState<string | null>(null);
  const [filterText, setFilterText] = useState('');
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [mcpDialogOpen, setMcpDialogOpen] = useState(false);

  useEffect(() => {
    if (AsyncResult.isFailure(filesResult) && isAuthFailure(filesResult.cause)) {
      onAuthExpired();
    }
  }, [filesResult, onAuthExpired]);

  const files = AsyncResult.isSuccess(filesResult) ? filesResult.value : emptyMemoryFiles;

  // 列表更新后尽量停留在当前查看文件，否则优先落到根文档 /memories/memory.md。
  useEffect(() => {
    if (!AsyncResult.isSuccess(filesResult)) return;
    const validPaths = new Set(filesResult.value.map((item) => item.path));
    setActivePath((current) => {
      if (current && validPaths.has(current)) return current;
      if (validPaths.has('/memories/memory.md')) return '/memories/memory.md';
      return filesResult.value[0]?.path ?? null;
    });
  }, [filesResult]);

  const filteredFiles = useMemo(() => {
    const query = filterText.trim().toLowerCase();
    if (!query) return files;
    return files.filter((item) => item.path.toLowerCase().includes(query));
  }, [files, filterText]);

  if (AsyncResult.isInitial(filesResult)) {
    return <LoadingScreen />;
  }

  if (AsyncResult.isFailure(filesResult)) {
    return (
      <main className="memories-page">
        <MemoriesTitleBar
          email={email}
          refreshing={filesResult.waiting}
          showSidebarToggle={false}
          showMCPButton={false}
          sidebarOpen={false}
          onToggleSidebar={() => {}}
          onOpenMCP={() => {}}
          onRefresh={refreshFiles}
        />
        <div className="memories-center-state">
          <section className="memories-state-card" role="alert">
            <h2>无法读取记忆</h2>
            <p>{formatCauseError(filesResult.cause, '记忆加载失败，请稍后重试')}</p>
            <button type="button" className="memories-primary-action" onClick={refreshFiles}>
              重新加载
            </button>
          </section>
        </div>
      </main>
    );
  }

  const activeSummary = activePath ? files.find((item) => item.path === activePath) : undefined;

  return (
    <main className="memories-page">
      <MemoriesTitleBar
        email={email}
        refreshing={filesResult.waiting}
        showSidebarToggle
        showMCPButton
        sidebarOpen={sidebarOpen}
        onToggleSidebar={() => setSidebarOpen((open) => !open)}
        onOpenMCP={() => setMcpDialogOpen(true)}
        onRefresh={refreshFiles}
      />
      <div className="memories-workbench">
        <div
          className={`memories-sidebar-backdrop${sidebarOpen ? ' is-open' : ''}`}
          aria-hidden="true"
          onClick={() => setSidebarOpen(false)}
        />
        <aside
          className={`memories-sidebar${sidebarOpen ? ' is-open' : ''}`}
          aria-label="记忆文件列表"
        >
          <div className="memories-sidebar-header">
            <span>记忆文件 ({files.length})</span>
          </div>
          <div className="memories-filter-box">
            <input
              type="search"
              className="memories-filter-input"
              aria-label="筛选记忆文件"
              placeholder="筛选文件路径…"
              value={filterText}
              onChange={(event) => setFilterText(event.target.value)}
            />
          </div>
          <div className="memories-file-list" role="listbox" aria-label="记忆文件">
            {files.length === 0 ? (
              <p className="memories-list-empty">暂无记忆文件</p>
            ) : filteredFiles.length === 0 ? (
              <p className="memories-list-empty">没有匹配“{filterText.trim()}”的文件</p>
            ) : (
              filteredFiles.map((file) => {
                const isActive = file.path === activePath;
                const displayPath = file.path.startsWith('/memories/')
                  ? file.path.slice('/memories/'.length)
                  : file.path;
                return (
                  <button
                    key={file.path}
                    type="button"
                    role="option"
                    aria-selected={isActive}
                    className={`memories-file-row${isActive ? ' is-active' : ''}`}
                    title={file.path}
                    onClick={() => {
                      setActivePath(file.path);
                      setSidebarOpen(false);
                    }}
                  >
                    <span className="memories-file-name">{displayPath}</span>
                    <span className="memories-file-size">{formatFileSize(file.bytes)}</span>
                  </button>
                );
              })
            )}
          </div>
        </aside>

        <section className="memories-editor" aria-label="记忆内容预览">
          {files.length === 0 ? (
            <div className="memories-viewport">
              <div className="memories-center-state">
                <section className="memories-state-card">
                  <h2>还没有记忆文件</h2>
                  <p>
                    当你和 aether 沟通长期偏好、常用规范或项目背景时，aether
                    会自动整理并保存在这里。
                  </p>
                  <Link to="/" className="memories-primary-action">
                    回到对话
                  </Link>
                </section>
              </div>
            </div>
          ) : !activePath ? (
            <div className="memories-viewport">
              <div className="memories-center-state">
                <p>从左侧选择一个记忆文件查看内容</p>
              </div>
            </div>
          ) : (
            <MemoryFilePreview
              token={token}
              path={activePath}
              fallbackUpdatedAt={activeSummary?.updated_at}
              onAuthExpired={onAuthExpired}
            />
          )}
        </section>
      </div>
      {mcpDialogOpen && (
        <MemoryMCPDialog
          token={token}
          onAuthExpired={() => {
            setMcpDialogOpen(false);
            onAuthExpired();
          }}
          onClose={() => setMcpDialogOpen(false)}
        />
      )}
    </main>
  );
}

// MemoryFilePreview 按文件路径订阅对应的内容 Atom，切换文件时旧请求由 AtomRegistry 自动取消。
function MemoryFilePreview({
  token,
  path,
  fallbackUpdatedAt,
  onAuthExpired,
}: {
  token: string;
  path: string;
  fallbackUpdatedAt?: string;
  onAuthExpired: () => void;
}) {
  const detailAtom = memoryFileContentAtom(`${token}\0${path}`);
  const detailResult = useAtomValue(detailAtom);
  const refreshDetail = useAtomRefresh(detailAtom);
  const [copied, setCopied] = useState(false);
  const copyTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => {
    setCopied(false);
    return () => clearTimeout(copyTimer.current);
  }, [path]);

  useEffect(() => {
    if (AsyncResult.isFailure(detailResult) && isAuthFailure(detailResult.cause)) {
      onAuthExpired();
    }
  }, [detailResult, onAuthExpired]);

  const activeDetail = AsyncResult.isSuccess(detailResult) ? detailResult.value : undefined;
  const activeUpdatedAt = activeDetail?.updated_at ?? fallbackUpdatedAt;

  const copyActiveContent = async () => {
    if (!activeDetail) return;
    await navigator.clipboard.writeText(activeDetail.content);
    setCopied(true);
    clearTimeout(copyTimer.current);
    copyTimer.current = setTimeout(() => setCopied(false), 1800);
  };

  return (
    <>
      <div className="memories-toolbar">
        <div className="memories-doc-meta">
          <span className="memories-doc-path" title={path}>
            {path}
          </span>
          {activeUpdatedAt && (
            <span className="memories-doc-updated">更新于 {formatUpdatedAt(activeUpdatedAt)}</span>
          )}
        </div>
        <button
          type="button"
          className="memories-copy-btn"
          disabled={!activeDetail}
          onClick={() => void copyActiveContent()}
        >
          {copied ? '已复制' : '复制原文'}
        </button>
      </div>

      <div className="memories-viewport">
        {AsyncResult.isInitial(detailResult) ? (
          <div className="memories-center-state" role="status">
            <p>正在读取 {path}…</p>
          </div>
        ) : AsyncResult.isFailure(detailResult) ? (
          <div className="memories-center-state" role="alert">
            <section className="memories-state-card">
              <h2>读取失败</h2>
              <p>{formatCauseError(detailResult.cause, '无法读取该记忆文件')}</p>
              <button type="button" className="memories-primary-action" onClick={refreshDetail}>
                重试
              </button>
            </section>
          </div>
        ) : (
          <article className="memories-preview">
            {detailResult.value.content.trim() === '' ? (
              <p className="memories-empty-file">（空文件）</p>
            ) : (
              <Markdown text={detailResult.value.content} />
            )}
          </article>
        )}
      </div>
    </>
  );
}

// MemoriesTitleBar 渲染顶部导航条：在桌面和移动端提供返回对话、抽屉开关、MCP 接入入口与手动刷新。
function MemoriesTitleBar({
  email,
  refreshing,
  showSidebarToggle,
  showMCPButton,
  sidebarOpen,
  onToggleSidebar,
  onOpenMCP,
  onRefresh,
}: {
  email: string;
  refreshing: boolean;
  showSidebarToggle: boolean;
  showMCPButton: boolean;
  sidebarOpen: boolean;
  onToggleSidebar: () => void;
  onOpenMCP: () => void;
  onRefresh: () => void;
}) {
  return (
    <header className="memories-titlebar">
      <div className="memories-titlebar-left">
        {showSidebarToggle && (
          <button
            type="button"
            className="memories-titlebar-btn memories-sidebar-toggle"
            aria-label={sidebarOpen ? '收起文件列表' : '展开文件列表'}
            aria-expanded={sidebarOpen}
            onClick={onToggleSidebar}
          >
            文件
          </button>
        )}
        <Link to="/" className="memories-back-link">
          ‹ 返回对话
        </Link>
        <span className="memories-workspace-name">只读记忆</span>
        {email && (
          <span className="memories-workspace-email" title={email}>
            {email}
          </span>
        )}
      </div>
      <div className="memories-titlebar-actions">
        {showMCPButton && (
          <button type="button" className="memories-titlebar-btn" onClick={onOpenMCP}>
            MCP 接入
          </button>
        )}
        <button
          type="button"
          className="memories-titlebar-btn"
          disabled={refreshing}
          onClick={onRefresh}
        >
          刷新
        </button>
      </div>
    </header>
  );
}

// MemoryMCPDialog 展示远程记忆 MCP 接入地址与当前账号已授权的外部应用清单，支持一键复制地址与随时撤销授权。
function MemoryMCPDialog({
  token,
  onAuthExpired,
  onClose,
}: {
  token: string;
  onAuthExpired: () => void;
  onClose: () => void;
}) {
  const connectionsAtom = oauthConnectionsAtom(token);
  const connectionsResult = useAtomValue(connectionsAtom);
  const refreshConnections = useAtomRefresh(connectionsAtom);
  const revokeConnection = useAtomSet(revokeOAuthConnectionAtom, { mode: 'promiseExit' });

  const [revokingID, setRevokingID] = useState<string | null>(null);
  const [actionError, setActionError] = useState('');
  const [urlCopied, setUrlCopied] = useState(false);
  const copyTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => () => clearTimeout(copyTimer.current), []);

  useEffect(() => {
    if (AsyncResult.isFailure(connectionsResult) && isAuthFailure(connectionsResult.cause)) {
      onAuthExpired();
    }
  }, [connectionsResult, onAuthExpired]);

  const mcpURL = AsyncResult.isSuccess(connectionsResult) ? connectionsResult.value.mcp_url : '';
  const connections = AsyncResult.isSuccess(connectionsResult)
    ? connectionsResult.value.connections
    : [];
  const error =
    actionError ||
    (AsyncResult.isFailure(connectionsResult)
      ? formatCauseError(connectionsResult.cause, '无法读取 MCP 接入信息')
      : '');

  const copyURL = async () => {
    if (!mcpURL) return;
    await navigator.clipboard.writeText(mcpURL);
    setUrlCopied(true);
    clearTimeout(copyTimer.current);
    copyTimer.current = setTimeout(() => setUrlCopied(false), 1800);
  };

  const revoke = async (connectionID: string) => {
    if (revokingID) return;
    setRevokingID(connectionID);
    setActionError('');
    try {
      const exit = await revokeConnection({ token, connectionID });
      if (Exit.isFailure(exit)) {
        if (isAuthFailure(exit.cause)) {
          onAuthExpired();
          return;
        }
        setActionError(formatCauseError(exit.cause, '撤销授权失败'));
        return;
      }
      refreshConnections();
    } finally {
      setRevokingID(null);
    }
  };

  return (
    <ModalDialog
      open
      canClose={revokingID === null}
      className="memories-mcp-dialog"
      ariaLabelledBy="memories-mcp-title"
      onClose={onClose}
    >
      <div className="memories-mcp-header">
        <h2 id="memories-mcp-title">远程记忆 MCP</h2>
        <button
          type="button"
          className="memories-mcp-close"
          disabled={revokingID !== null}
          onClick={onClose}
        >
          关闭
        </button>
      </div>

      {AsyncResult.isInitial(connectionsResult) ? (
        <p className="memories-mcp-hint">正在读取接入信息…</p>
      ) : (
        <div className="memories-mcp-body">
          <section className="memories-mcp-section">
            <h3>接入地址（Streamable HTTP + OAuth 2.1）</h3>
            {mcpURL ? (
              <div className="memories-mcp-url-row">
                <code className="memories-mcp-url">{mcpURL}</code>
                <button type="button" className="memories-copy-btn" onClick={() => void copyURL()}>
                  {urlCopied ? '已复制' : '复制地址'}
                </button>
              </div>
            ) : (
              <p className="memories-mcp-hint">当前部署尚未配置 PUBLIC_ORIGIN，暂不可用。</p>
            )}
            <p className="memories-mcp-hint">
              在 ChatGPT Developer Mode、Gemini Apps Developer Mode、Claude 或 Cursor
              中填入此地址并选择 OAuth 授权，即可与 aether 双向共享同一份记忆。
            </p>
          </section>

          <section className="memories-mcp-section">
            <h3>已授权应用 ({connections.length})</h3>
            {connections.length === 0 ? (
              <p className="memories-mcp-hint">还没有已授权的外部应用。</p>
            ) : (
              <ul className="memories-mcp-list">
                {connections.map((item) => (
                  <li key={item.id} className="memories-mcp-item">
                    <div className="memories-mcp-item-meta">
                      <span className="memories-mcp-item-name">{item.client_name}</span>
                      <span className="memories-mcp-item-time">
                        授权于 {formatUpdatedAt(item.created_at)}
                        {item.last_used_at
                          ? ` · 最近使用 ${formatUpdatedAt(item.last_used_at)}`
                          : ' · 尚未调用'}
                      </span>
                    </div>
                    <button
                      type="button"
                      className="memories-mcp-revoke"
                      disabled={revokingID !== null}
                      onClick={() => void revoke(item.id)}
                    >
                      {revokingID === item.id ? '撤销中…' : '撤销授权'}
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>

          {error && (
            <p className="memories-mcp-error" role="alert">
              {error}
            </p>
          )}
        </div>
      )}
    </ModalDialog>
  );
}

// formatUpdatedAt 用本地中文短时间展示文件最近更新时间。
function formatUpdatedAt(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleString('zh-CN', {
    year: 'numeric',
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}
