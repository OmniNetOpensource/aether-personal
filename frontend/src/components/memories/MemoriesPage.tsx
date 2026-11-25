// 本文件实现 /memories 路由的双栏只读记忆浏览页，展示左侧可筛选文件列表与右侧 Markdown 预览。

import { useEffect, useMemo, useRef, useState } from 'react';
import { AuthError } from '../../lib/api.js';
import { fetchMemoryFileContent, fetchMemoryFiles } from '../../lib/memories.js';
import type { MemoryFileDetail, MemoryFileSummary } from '../../lib/memories.js';
import { formatFileSize } from '../../lib/messageAttachments.js';
import { NavLink } from '../../lib/navigation.js';
import { readSession } from '../../lib/platform.js';
import { LoadingScreen } from '../LoadingScreen.js';
import { Markdown } from '../markdown/Markdown.js';
import './memories.css';

type PageStatus = 'loading' | 'unauthenticated' | 'error' | 'ready';

// MemoriesPage 管理当前账号记忆列表与按需拉取的正文缓存，全程只读不改写数据库。
export function MemoriesPage() {
  const [status, setStatus] = useState<PageStatus>('loading');
  const [pageError, setPageError] = useState('');
  const [token, setToken] = useState<string | null>(null);
  const [email, setEmail] = useState('');
  const [files, setFiles] = useState<MemoryFileSummary[]>([]);
  const [activePath, setActivePath] = useState<string | null>(null);
  const [fileCache, setFileCache] = useState<Record<string, MemoryFileDetail>>({});
  const [loadingPath, setLoadingPath] = useState<string | null>(null);
  const [fileError, setFileError] = useState('');
  const [filterText, setFilterText] = useState('');
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const [reloadCount, setReloadCount] = useState(0);
  const [fileRetry, setFileRetry] = useState(0);
  const copyTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const activePathRef = useRef<string | null>(null);
  activePathRef.current = activePath;

  useEffect(() => () => clearTimeout(copyTimer.current), []);

  // 初始化或刷新时重新读取会话与文件清单，尽量停留在当前查看的文件。
  useEffect(() => {
    const abortController = new AbortController();
    setStatus((current) => (current === 'ready' ? 'ready' : 'loading'));
    setPageError('');

    const session = readSession();
    if (!session.token) {
      setToken(null);
      setStatus('unauthenticated');
      return () => abortController.abort();
    }
    setToken(session.token);
    setEmail(session.email);

    void fetchMemoryFiles(session.token, abortController.signal)
      .then((loadedFiles) => {
        if (abortController.signal.aborted) return;
        const validPaths = new Set(loadedFiles.map((item) => item.path));
        const preferredPath =
          (activePathRef.current && validPaths.has(activePathRef.current)
            ? activePathRef.current
            : null) ??
          (validPaths.has('/memories/memory.md')
            ? '/memories/memory.md'
            : (loadedFiles[0]?.path ?? null));
        setFiles(loadedFiles);
        setFileCache({});
        setFileError('');
        setActivePath(preferredPath);
        setStatus('ready');
      })
      .catch((error: unknown) => {
        if (abortController.signal.aborted) return;
        if (error instanceof AuthError) {
          setToken(null);
          setStatus('unauthenticated');
          return;
        }
        setPageError(error instanceof Error ? error.message : '记忆加载失败，请稍后重试');
        setStatus('error');
      });

    return () => abortController.abort();
  }, [reloadCount]);

  // 选中文件切换或点击重试时，按需拉取该文件的完整文本。
  useEffect(() => {
    if (status !== 'ready' || !token || !activePath) return;
    if (fileCache[activePath]) {
      setLoadingPath(null);
      setFileError('');
      return;
    }

    const abortController = new AbortController();
    setLoadingPath(activePath);
    setFileError('');

    void fetchMemoryFileContent(token, activePath, abortController.signal)
      .then((detail) => {
        if (abortController.signal.aborted) return;
        setFileCache((previous) => ({ ...previous, [detail.path]: detail }));
        setLoadingPath(null);
      })
      .catch((error: unknown) => {
        if (abortController.signal.aborted) return;
        if (error instanceof AuthError) {
          setToken(null);
          setStatus('unauthenticated');
          return;
        }
        setLoadingPath(null);
        setFileError(error instanceof Error ? error.message : '无法读取该记忆文件');
      });

    return () => abortController.abort();
  }, [status, token, activePath, fileCache, fileRetry]);

  const filteredFiles = useMemo(() => {
    const query = filterText.trim().toLowerCase();
    if (!query) return files;
    return files.filter((item) => item.path.toLowerCase().includes(query));
  }, [files, filterText]);

  const activeDetail = activePath ? fileCache[activePath] : undefined;
  const activeSummary = activePath ? files.find((item) => item.path === activePath) : undefined;
  const activeUpdatedAt = activeDetail?.updated_at ?? activeSummary?.updated_at;

  const copyActiveContent = async () => {
    if (!activeDetail) return;
    await navigator.clipboard.writeText(activeDetail.content);
    setCopied(true);
    clearTimeout(copyTimer.current);
    copyTimer.current = setTimeout(() => setCopied(false), 1800);
  };

  if (status === 'loading') {
    return <LoadingScreen />;
  }

  if (status === 'unauthenticated' || status === 'error') {
    return (
      <main className="memories-page">
        <MemoriesTitleBar
          email={status === 'error' ? email : ''}
          refreshing={false}
          showSidebarToggle={false}
          sidebarOpen={false}
          onToggleSidebar={() => {}}
          onRefresh={() => setReloadCount((count) => count + 1)}
        />
        <div className="memories-center-state">
          <section className="memories-state-card" role={status === 'error' ? 'alert' : undefined}>
            <h2>{status === 'unauthenticated' ? '请先登录' : '无法读取记忆'}</h2>
            <p>
              {status === 'unauthenticated'
                ? '记忆文件属于你的个人账号，返回主页登录后即可查看。'
                : pageError}
            </p>
            {status === 'unauthenticated' ? (
              <NavLink to="/" className="memories-primary-action">
                返回登录 / 对话
              </NavLink>
            ) : (
              <button
                type="button"
                className="memories-primary-action"
                onClick={() => {
                  setStatus('loading');
                  setReloadCount((count) => count + 1);
                }}
              >
                重新加载
              </button>
            )}
          </section>
        </div>
      </main>
    );
  }

  return (
    <main className="memories-page">
      <MemoriesTitleBar
        email={email}
        refreshing={loadingPath !== null}
        showSidebarToggle
        sidebarOpen={sidebarOpen}
        onToggleSidebar={() => setSidebarOpen((open) => !open)}
        onRefresh={() => setReloadCount((count) => count + 1)}
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
                      setCopied(false);
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
          {activePath && (
            <div className="memories-toolbar">
              <div className="memories-doc-meta">
                <span className="memories-doc-path" title={activePath}>
                  {activePath}
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
          )}

          <div className="memories-viewport">
            {files.length === 0 ? (
              <div className="memories-center-state">
                <section className="memories-state-card">
                  <h2>还没有记忆文件</h2>
                  <p>
                    当你和 aether 沟通长期偏好、常用规范或项目背景时，aether 会自动整理并保存在这里。
                  </p>
                  <NavLink to="/" className="memories-primary-action">
                    回到对话
                  </NavLink>
                </section>
              </div>
            ) : !activePath ? (
              <div className="memories-center-state">
                <p>从左侧选择一个记忆文件查看内容</p>
              </div>
            ) : loadingPath === activePath && !activeDetail ? (
              <div className="memories-center-state" role="status">
                <p>正在读取 {activePath}…</p>
              </div>
            ) : fileError && !activeDetail ? (
              <div className="memories-center-state" role="alert">
                <section className="memories-state-card">
                  <h2>读取失败</h2>
                  <p>{fileError}</p>
                  <button
                    type="button"
                    className="memories-primary-action"
                    onClick={() => setFileRetry((count) => count + 1)}
                  >
                    重试
                  </button>
                </section>
              </div>
            ) : activeDetail ? (
              <article className="memories-preview">
                {activeDetail.content.trim() === '' ? (
                  <p className="memories-empty-file">（空文件）</p>
                ) : (
                  <Markdown text={activeDetail.content} />
                )}
              </article>
            ) : null}
          </div>
        </section>
      </div>
    </main>
  );
}

// MemoriesTitleBar 渲染顶部导航条：在桌面和移动端提供返回对话、抽屉开关与手动刷新。
function MemoriesTitleBar({
  email,
  refreshing,
  showSidebarToggle,
  sidebarOpen,
  onToggleSidebar,
  onRefresh,
}: {
  email: string;
  refreshing: boolean;
  showSidebarToggle: boolean;
  sidebarOpen: boolean;
  onToggleSidebar: () => void;
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
        <NavLink to="/" className="memories-back-link">
          ‹ 返回对话
        </NavLink>
        <span className="memories-workspace-name">只读记忆</span>
        {email && (
          <span className="memories-workspace-email" title={email}>
            {email}
          </span>
        )}
      </div>
      <button
        type="button"
        className="memories-titlebar-btn"
        disabled={refreshing}
        onClick={onRefresh}
      >
        刷新
      </button>
    </header>
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
