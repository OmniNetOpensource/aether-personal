// 本文件实现 /memories 路由的 VS Code 样式只读记忆浏览页，展示左侧目录树、多文件标签页与 Markdown 预览/源码切换。

import { Link } from '@tanstack/react-router';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  MemoryAuthError,
  buildMemoryTree,
  fetchMemoryFileContent,
  fetchMemoryFiles,
  formatMemoryBytes,
} from '../../lib/memories.js';
import type { MemoryFileDetail, MemoryFileSummary, MemoryTreeNode } from '../../lib/memories.js';
import { readSession } from '../../lib/platform.js';
import { LoadingScreen } from '../LoadingScreen.js';
import { Markdown } from '../markdown/Markdown.js';
import './memories.css';

type PageStatus = 'loading' | 'unauthenticated' | 'error' | 'ready';
type ViewMode = 'preview' | 'source';

// MemoriesPage 管理当前账号记忆列表、按需加载的文件正文缓存及标签页状态，全程只读不改写数据库。
export function MemoriesPage() {
  const [status, setStatus] = useState<PageStatus>('loading');
  const [pageError, setPageError] = useState('');
  const [token, setToken] = useState<string | null>(null);
  const [email, setEmail] = useState('');
  const [files, setFiles] = useState<MemoryFileSummary[]>([]);
  const [openTabs, setOpenTabs] = useState<string[]>([]);
  const [activePath, setActivePath] = useState<string | null>(null);
  const [expandedDirs, setExpandedDirs] = useState<Set<string>>(() => new Set());
  const [fileCache, setFileCache] = useState<Record<string, MemoryFileDetail>>({});
  const [loadingPath, setLoadingPath] = useState<string | null>(null);
  const [fileError, setFileError] = useState('');
  const [filterText, setFilterText] = useState('');
  const [viewMode, setViewMode] = useState<ViewMode>('preview');
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const [reloadCount, setReloadCount] = useState(0);
  const copyTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const activePathRef = useRef<string | null>(null);
  activePathRef.current = activePath;

  // 页面卸载时清理复制反馈计时器，避免悬空回调触发状态更新。
  useEffect(() => () => clearTimeout(copyTimer.current), []);

  // 初始化或点击刷新时重新读取会话与文件列表，并保留仍存在的标签页和当前选中项。
  useEffect(() => {
    const abortController = new AbortController();
    setStatus((current) => (current === 'ready' ? 'ready' : 'loading'));
    setPageError('');

    void (async () => {
      try {
        const session = await readSession();
        if (abortController.signal.aborted) return;
        if (!session.token) {
          setToken(null);
          setStatus('unauthenticated');
          return;
        }
        setToken(session.token);
        setEmail(session.email);

        const loadedFiles = await fetchMemoryFiles(session.token, abortController.signal);
        if (abortController.signal.aborted) return;

        const validPaths = new Set(loadedFiles.map((item) => item.path));
        const allDirs = collectDirectoryPaths(loadedFiles);
        setFiles(loadedFiles);
        setFileCache({});
        setFileError('');
        setExpandedDirs((previous) => {
          const next = new Set(previous);
          for (const dir of allDirs) next.add(dir);
          return next;
        });

        const preferredPath =
          (activePathRef.current && validPaths.has(activePathRef.current)
            ? activePathRef.current
            : null) ??
          (validPaths.has('/memories/memory.md')
            ? '/memories/memory.md'
            : (loadedFiles[0]?.path ?? null));

        setOpenTabs((previous) => {
          const kept = previous.filter((item) => validPaths.has(item));
          if (preferredPath && !kept.includes(preferredPath)) {
            return [...kept, preferredPath];
          }
          return kept;
        });
        setActivePath(preferredPath);
        setStatus('ready');
      } catch (error) {
        if (abortController.signal.aborted) return;
        if (error instanceof MemoryAuthError) {
          setToken(null);
          setStatus('unauthenticated');
          return;
        }
        setPageError(error instanceof Error ? error.message : '记忆加载失败，请稍后重试');
        setStatus('error');
      }
    })();

    return () => abortController.abort();
  }, [reloadCount]);

  // 选中标签切换或刷新清空缓存后，按需拉取当前活动文件的完整正文。
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
        if (error instanceof MemoryAuthError) {
          setToken(null);
          setStatus('unauthenticated');
          return;
        }
        setLoadingPath(null);
        setFileError(error instanceof Error ? error.message : '无法读取该记忆文件');
      });

    return () => abortController.abort();
  }, [status, token, activePath, fileCache]);

  // 按输入关键词过滤文件清单后再构造目录树，使匹配的文件及其祖先目录自然展开呈现。
  const filteredFiles = useMemo(() => {
    const query = filterText.trim().toLowerCase();
    if (!query) return files;
    return files.filter((item) => item.path.toLowerCase().includes(query));
  }, [files, filterText]);

  const tree = useMemo(() => buildMemoryTree(filteredFiles), [filteredFiles]);
  const allDirectories = useMemo(() => collectDirectoryPaths(files), [files]);

  // selectFile 打开或切换到指定文件标签，并确保其祖先目录处于展开状态。
  const selectFile = useCallback((path: string) => {
    setOpenTabs((previous) => (previous.includes(path) ? previous : [...previous, path]));
    setActivePath(path);
    setExpandedDirs((previous) => {
      const next = new Set(previous);
      for (const dir of ancestorDirectories(path)) next.add(dir);
      return next;
    });
    setSidebarOpen(false);
    setCopied(false);
  }, []);

  // closeTab 关闭标签并在关闭当前活动标签时顺延切换到邻近标签。
  const closeTab = useCallback((path: string) => {
    setOpenTabs((previous) => {
      const index = previous.indexOf(path);
      if (index === -1) return previous;
      const next = previous.filter((item) => item !== path);
      setActivePath((current) => {
        if (current !== path) return current;
        if (next.length === 0) return null;
        return next[Math.min(index, next.length - 1)];
      });
      return next;
    });
    setCopied(false);
  }, []);

  // toggleDirectory 展开或折叠资源管理器中的目录节点。
  const toggleDirectory = useCallback((path: string) => {
    setExpandedDirs((previous) => {
      const next = new Set(previous);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  }, []);

  // toggleAllDirectories 一键展开或折叠全部子目录。
  const toggleAllDirectories = useCallback(() => {
    setExpandedDirs((previous) =>
      previous.size > 0 ? new Set() : new Set(allDirectories),
    );
  }, [allDirectories]);

  // copyActiveContent 将当前文件原文复制到剪贴板，用于快速粘贴到聊天引用或外部编辑。
  const activeDetail = activePath ? fileCache[activePath] : undefined;
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

  if (status === 'unauthenticated') {
    return (
      <main className="memories-page">
        <MemoriesTitleBar
          email=""
          refreshing={false}
          showSidebarToggle={false}
          sidebarOpen={false}
          onToggleSidebar={() => {}}
          onRefresh={() => {}}
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

  if (status === 'error') {
    return (
      <main className="memories-page">
        <MemoriesTitleBar
          email={email}
          refreshing={false}
          showSidebarToggle={false}
          sidebarOpen={false}
          onToggleSidebar={() => {}}
          onRefresh={() => setReloadCount((count) => count + 1)}
        />
        <div className="memories-center-state">
          <section className="memories-state-card" role="alert">
            <h2>无法读取记忆</h2>
            <p>{pageError}</p>
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
          </section>
        </div>
      </main>
    );
  }

  const lineCount = activeDetail ? splitLines(activeDetail.content).length : 0;
  const charCount = activeDetail ? Array.from(activeDetail.content).length : 0;
  const activeSummary = activePath ? files.find((item) => item.path === activePath) : undefined;
  const activeUpdatedAt = activeDetail?.updated_at ?? activeSummary?.updated_at;

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
          aria-label="记忆文件资源管理器"
        >
          <div className="memories-sidebar-header">
            <span className="memories-sidebar-title">
              Explorer · Memories
              <span className="memories-file-count">{files.length}</span>
            </span>
            {allDirectories.length > 0 && (
              <button
                type="button"
                className="memories-icon-btn"
                title={expandedDirs.size > 0 ? '全部折叠' : '全部展开'}
                aria-label={expandedDirs.size > 0 ? '全部折叠' : '全部展开'}
                onClick={toggleAllDirectories}
              >
                <svg viewBox="0 0 16 16" aria-hidden="true">
                  <path d="M3 5h10M3 8h7M3 11h10" />
                </svg>
              </button>
            )}
          </div>
          <div className="memories-filter-box">
            <div className="memories-filter-input-wrap">
              <input
                type="search"
                className="memories-filter-input"
                aria-label="筛选记忆文件"
                placeholder="筛选文件路径…"
                value={filterText}
                onChange={(event) => setFilterText(event.target.value)}
              />
              {filterText && (
                <button
                  type="button"
                  className="memories-filter-clear"
                  aria-label="清空筛选"
                  onClick={() => setFilterText('')}
                >
                  ×
                </button>
              )}
            </div>
          </div>
          <div className="memories-tree-scroll" role="tree" aria-label="记忆目录树">
            {files.length === 0 ? (
              <p className="memories-tree-empty">暂无记忆文件</p>
            ) : tree.length === 0 ? (
              <p className="memories-tree-empty">没有匹配“{filterText.trim()}”的文件</p>
            ) : (
              <MemoryTreeList
                nodes={tree}
                depth={0}
                activePath={activePath}
                expandedDirs={expandedDirs}
                forceExpand={filterText.trim() !== ''}
                onToggleDir={toggleDirectory}
                onSelectFile={selectFile}
              />
            )}
          </div>
        </aside>

        <section className="memories-editor" aria-label="记忆文档阅读器">
          {openTabs.length > 0 && (
            <div className="memories-tabs" role="tablist" aria-label="已打开的记忆文件">
              {openTabs.map((tabPath) => {
                const isActive = tabPath === activePath;
                const fileName = tabBaseName(tabPath);
                return (
                  <div
                    key={tabPath}
                    className={`memories-tab${isActive ? ' is-active' : ''}`}
                  >
                    <button
                      type="button"
                      role="tab"
                      aria-selected={isActive}
                      className="memories-tab-button"
                      title={tabPath}
                      onClick={() => selectFile(tabPath)}
                    >
                      <FileGlyph isRoot={tabPath === '/memories/memory.md'} />
                      <span className="memories-tab-name">{fileName}</span>
                    </button>
                    <button
                      type="button"
                      className="memories-tab-close"
                      aria-label={`关闭 ${fileName}`}
                      onClick={() => closeTab(tabPath)}
                    >
                      ×
                    </button>
                  </div>
                );
              })}
            </div>
          )}

          {activePath && (
            <div className="memories-toolbar">
              <div className="memories-breadcrumb" aria-label="文件路径">
                {activePath
                  .split('/')
                  .filter(Boolean)
                  .map((segment, index, all) => (
                    <span key={`${segment}-${index}`} className="memories-breadcrumb">
                      {index > 0 && (
                        <span className="memories-breadcrumb-sep" aria-hidden="true">
                          ›
                        </span>
                      )}
                      <span
                        className={`memories-breadcrumb-part${
                          index === all.length - 1 ? ' is-current' : ''
                        }`}
                      >
                        {segment}
                      </span>
                    </span>
                  ))}
              </div>
              <div className="memories-toolbar-actions">
                <div className="memories-mode-switch" role="group" aria-label="视图模式">
                  <button
                    type="button"
                    className={`memories-mode-btn${viewMode === 'preview' ? ' is-active' : ''}`}
                    aria-pressed={viewMode === 'preview'}
                    onClick={() => setViewMode('preview')}
                  >
                    预览
                  </button>
                  <button
                    type="button"
                    className={`memories-mode-btn${viewMode === 'source' ? ' is-active' : ''}`}
                    aria-pressed={viewMode === 'source'}
                    onClick={() => setViewMode('source')}
                  >
                    源码
                  </button>
                </div>
                <button
                  type="button"
                  className="memories-copy-btn"
                  disabled={!activeDetail}
                  onClick={() => void copyActiveContent()}
                >
                  {copied ? '已复制' : '复制'}
                </button>
              </div>
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
                  <Link to="/" className="memories-primary-action">
                    回到对话
                  </Link>
                </section>
              </div>
            ) : !activePath ? (
              <div className="memories-center-state">
                <p>从左侧资源管理器选择一个记忆文件查看内容</p>
              </div>
            ) : loadingPath === activePath && !activeDetail ? (
              <div className="memories-center-state" role="status">
                <p>正在读取 {tabBaseName(activePath)}…</p>
              </div>
            ) : fileError && !activeDetail ? (
              <div className="memories-center-state" role="alert">
                <section className="memories-state-card">
                  <h2>读取失败</h2>
                  <p>{fileError}</p>
                  <button
                    type="button"
                    className="memories-primary-action"
                    onClick={() => {
                      setFileCache((previous) => {
                        const next = { ...previous };
                        delete next[activePath];
                        return next;
                      });
                    }}
                  >
                    重试
                  </button>
                </section>
              </div>
            ) : activeDetail ? (
              viewMode === 'preview' ? (
                <article className="memories-preview">
                  {activeDetail.content.trim() === '' ? (
                    <p className="memories-empty-file">（空文件）</p>
                  ) : (
                    <Markdown text={activeDetail.content} />
                  )}
                </article>
              ) : (
                <div className="memories-source" role="region" aria-label="记忆文件源码">
                  {splitLines(activeDetail.content).map((line, index) => (
                    <div key={index} className="memories-source-line">
                      <span className="memories-line-number">{index + 1}</span>
                      <span className="memories-line-text">{line || ' '}</span>
                    </div>
                  ))}
                </div>
              )
            ) : null}
          </div>
        </section>
      </div>

      <footer className="memories-statusbar">
        <div className="memories-statusbar-group">
          <span className="memories-statusbar-item memories-readonly-pill">只读</span>
          <span className="memories-statusbar-path" title={activePath ?? '/memories'}>
            {activePath ?? '/memories'}
          </span>
        </div>
        <div className="memories-statusbar-group">
          {activeDetail && (
            <>
              <span className="memories-statusbar-item">{lineCount} 行</span>
              <span className="memories-statusbar-item memories-statusbar-hide-mobile">
                {charCount} 字符
              </span>
              <span className="memories-statusbar-item">
                {formatMemoryBytes(activeDetail.bytes)}
              </span>
            </>
          )}
          {activeUpdatedAt && (
            <span className="memories-statusbar-item memories-statusbar-hide-mobile">
              更新于 {formatUpdatedAt(activeUpdatedAt)}
            </span>
          )}
          <span className="memories-statusbar-item memories-statusbar-hide-mobile">Markdown</span>
        </div>
      </footer>
    </main>
  );
}

// MemoriesTitleBar 渲染顶部窗口条：在桌面和移动端提供返回聊天、目录抽屉开关与手动刷新。
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
            aria-label={sidebarOpen ? '收起文件树' : '展开文件树'}
            aria-expanded={sidebarOpen}
            onClick={onToggleSidebar}
          >
            <svg viewBox="0 0 16 16" aria-hidden="true">
              <rect x="2" y="2.5" width="12" height="11" rx="1.5" />
              <path d="M6 2.5v11" />
            </svg>
            目录
          </button>
        )}
        <Link to="/" className="memories-back-link">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M10 3.5 5.5 8 10 12.5" />
          </svg>
          返回对话
        </Link>
        <div className="memories-workspace-badge">
          <span className="memories-workspace-name">MEMORIES</span>
          {email && (
            <span className="memories-workspace-email" title={email}>
              {email}
            </span>
          )}
        </div>
      </div>
      <div className="memories-titlebar-right">
        <button
          type="button"
          className="memories-titlebar-btn"
          disabled={refreshing}
          onClick={onRefresh}
        >
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M13 8a5 5 0 1 1-1.35-3.42M13 2.75v2.75h-2.75" />
          </svg>
          刷新
        </button>
      </div>
    </header>
  );
}

// MemoryTreeList 递归渲染可展开的文件夹与文件行，保留 VS Code 样式的层级缩进。
function MemoryTreeList({
  nodes,
  depth,
  activePath,
  expandedDirs,
  forceExpand,
  onToggleDir,
  onSelectFile,
}: {
  nodes: MemoryTreeNode[];
  depth: number;
  activePath: string | null;
  expandedDirs: Set<string>;
  forceExpand: boolean;
  onToggleDir: (path: string) => void;
  onSelectFile: (path: string) => void;
}) {
  return (
    <>
      {nodes.map((node) => {
        const paddingLeft = 10 + depth * 14;
        if (node.kind === 'directory') {
          const isOpen = forceExpand || expandedDirs.has(node.path);
          return (
            <div key={node.path} role="treeitem" aria-expanded={isOpen}>
              <button
                type="button"
                className="memories-tree-row"
                style={{ paddingLeft }}
                onClick={() => onToggleDir(node.path)}
              >
                <span
                  className={`memories-tree-chevron${isOpen ? ' is-open' : ''}`}
                  aria-hidden="true"
                >
                  <svg viewBox="0 0 16 16">
                    <path d="m6 4 4 4-4 4" />
                  </svg>
                </span>
                <span className="memories-tree-icon is-folder" aria-hidden="true">
                  <svg viewBox="0 0 16 16">
                    <path d="M2 4.5A1.5 1.5 0 0 1 3.5 3h2.8l1.3 1.5h4.9A1.5 1.5 0 0 1 14 6v5.5a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 2 11.5z" />
                  </svg>
                </span>
                <span className="memories-tree-label">{node.name}</span>
              </button>
              {isOpen && (
                <div role="group">
                  <MemoryTreeList
                    nodes={node.children}
                    depth={depth + 1}
                    activePath={activePath}
                    expandedDirs={expandedDirs}
                    forceExpand={forceExpand}
                    onToggleDir={onToggleDir}
                    onSelectFile={onSelectFile}
                  />
                </div>
              )}
            </div>
          );
        }

        const isActive = node.path === activePath;
        const isRoot = node.path === '/memories/memory.md';
        return (
          <button
            key={node.path}
            type="button"
            role="treeitem"
            aria-selected={isActive}
            className={`memories-tree-row${isActive ? ' is-active' : ''}`}
            style={{ paddingLeft: paddingLeft + 18 }}
            title={node.path}
            onClick={() => onSelectFile(node.path)}
          >
            <FileGlyph isRoot={isRoot} />
            <span className="memories-tree-label">{node.name}</span>
            <span className="memories-tree-size">{formatMemoryBytes(node.file.bytes)}</span>
          </button>
        );
      })}
    </>
  );
}

// FileGlyph 为根索引文件与普通主题文件提供一致的轻量文档图标。
function FileGlyph({ isRoot }: { isRoot: boolean }) {
  return (
    <span className={`memories-tree-icon${isRoot ? ' is-root' : ''}`} aria-hidden="true">
      <svg viewBox="0 0 16 16">
        <path d="M4 2.5h5.2L12.5 5.8v7.7a1 1 0 0 1-1 1h-7.5a1 1 0 0 1-1-1v-10a1 1 0 0 1 1-1Z" />
        <path d="M9 2.5v3.5h3.5" />
      </svg>
    </span>
  );
}

// collectDirectoryPaths 从扁平文件路径提取全部隐式父目录，供初始展开与全部折叠按钮使用。
function collectDirectoryPaths(files: MemoryFileSummary[]): string[] {
  const dirs = new Set<string>();
  for (const file of files) {
    for (const dir of ancestorDirectories(file.path)) {
      dirs.add(dir);
    }
  }
  return Array.from(dirs).sort();
}

// ancestorDirectories 返回某个 /memories/... 文件路径下的全部隐式目录路径。
function ancestorDirectories(filePath: string): string[] {
  const relative = filePath.startsWith('/memories/')
    ? filePath.slice('/memories/'.length)
    : filePath;
  const segments = relative.split('/').filter(Boolean);
  const dirs: string[] = [];
  let prefix = '/memories';
  for (let index = 0; index < segments.length - 1; index++) {
    prefix += '/' + segments[index];
    dirs.push(prefix);
  }
  return dirs;
}

// tabBaseName 提取路径最后一段作为标签页和提示文案中的文件名。
function tabBaseName(path: string): string {
  const index = path.lastIndexOf('/');
  return index >= 0 ? path.slice(index + 1) : path;
}

// splitLines 按换行拆分原文并去掉末尾空行，保持与后端 viewMemoryFile 一致的行数统计。
function splitLines(content: string): string[] {
  if (content === '') return [''];
  const lines = content.split('\n');
  if (lines.length > 1 && lines[lines.length - 1] === '') {
    lines.pop();
  }
  return lines;
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
