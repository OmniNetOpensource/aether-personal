// 本文件是管理看板应用组件：消费 Effect Atom 维护的鉴权、筛选、自动刷新与各页数据，并切换运行过程与配置页面。

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  useAtom,
  useAtomMount,
  useAtomRefresh,
  useAtomSet,
  useAtomValue,
} from '@effect/atom-react';
import { Cause, Exit } from 'effect';
import { AsyncResult } from 'effect/reactivity';
import { formatAdminCause, outcomeLabels } from './api.js';
import { ChatGPTSubscriptionPage } from './ChatGPTSubscription.js';
import { ModelProvidersPage } from './ModelProviders.js';
import { RuntimeSettingsPage } from './RuntimeSettings.js';
import { ServiceSettingsPage } from './ServiceSettings.js';
import {
  accessStateAtom,
  activeTabAtom,
  bootstrapSessionAtom,
  browsingPagesAtom,
  chatGPTStatusAtom,
  defaultFilters,
  draftFiltersAtom,
  knownUsersAtom,
  loadEarlierTimelineAtom,
  loadTimelineMessageAtom,
  loginAdminAtom,
  logoutAdminAtom,
  modelProvidersAtom,
  notifyToastAtom,
  refreshTimelineAtom,
  runtimeSettingsAtom,
  serviceSettingsAtom,
  settingsBusyAtom,
  snapshotDataAtom,
  submittedFiltersAtom,
  timelineAutoRefreshAtom,
  timelineDataAtom,
  timelineFullItemsAtom,
  timelineLoadErrorAtom,
  toastMessageAtom,
  updatedTextAtom,
} from './state.js';
import type { AdminTab } from './state.js';
import { TimelinePage } from './Timeline.js';
import { Empty } from './ui.js';

const tabs: Record<AdminTab, [string, string]> = {
  timeline: ['运行过程', '按时间查看 Main 与后台 Subagent 收到的输入、推理与工具调用。'],
  settings: ['配置', '管理主动压缩提醒线、热压缩触发线、模型供应商和服务凭证。'],
};
const tabGlyphs: Record<AdminTab, string> = { timeline: '⋮', settings: '⚙' };

// AdminApp 是看板根组件：启动时通过 Atom 校验管理员身份，按登录态切换登录页、无权页或工作空间。
export function AdminApp() {
  useAtomMount(bootstrapSessionAtom);
  const access = useAtomValue(accessStateAtom);
  const logout = useAtomSet(logoutAdminAtom, { mode: 'promiseExit' });
  const notify = useAtomSet(notifyToastAtom);

  const handleLogout = useCallback(async () => {
    const exit = await logout(undefined);
    if (Exit.isFailure(exit)) {
      notify('暂时无法退出，请稍后重试。');
    }
  }, [logout, notify]);

  if (access.kind === 'booting') return <div className="boot">正在验证管理员身份…</div>;
  if (access.kind === 'denied') {
    return (
      <main className="access-denied">
        <div className="eyebrow teal">ACCESS RESTRICTED</div>
        <h1>管理权限已不可用</h1>
        <p>{access.message}</p>
        <button type="button" className="button primary" onClick={() => void handleLogout()}>
          重新登录
        </button>
      </main>
    );
  }
  if (access.kind === 'logged_out') {
    return <LoginPage />;
  }

  return <AuthenticatedConsole adminName={access.name} onLogout={handleLogout} />;
}

// AuthenticatedConsole 承载已登录管理员的侧边栏外壳、15 秒自动刷新与运行过程/配置页切换。
function AuthenticatedConsole({
  adminName,
  onLogout,
}: {
  adminName: string;
  onLogout: () => Promise<void>;
}) {
  useAtomMount(timelineAutoRefreshAtom);

  const [tab, setTab] = useAtom(activeTabAtom);
  const [filters, setFilters] = useAtom(draftFiltersAtom);
  const [query, setQuery] = useAtom(submittedFiltersAtom);
  const knownUsers = useAtomValue(knownUsersAtom);
  const snapshot = useAtomValue(snapshotDataAtom);
  const timeline = useAtomValue(timelineDataAtom);
  const fullItems = useAtomValue(timelineFullItemsAtom);
  const loadError = useAtomValue(timelineLoadErrorAtom);
  const browsingPages = useAtomValue(browsingPagesAtom);
  const updatedText = useAtomValue(updatedTextAtom);
  const toast = useAtomValue(toastMessageAtom);

  const refreshTimelineResult = useAtomValue(refreshTimelineAtom);
  const triggerRefreshTimeline = useAtomSet(refreshTimelineAtom);
  const loadEarlier = useAtomSet(loadEarlierTimelineAtom, { mode: 'promiseExit' });
  const loadFullMessage = useAtomSet(loadTimelineMessageAtom, { mode: 'promiseExit' });
  const notify = useAtomSet(notifyToastAtom);

  // 时间线折叠展开状态属于纯界面记忆，跨自动刷新保留。
  const timelineOpen = useRef(new Map<string, boolean>());

  // 切回运行过程页或提交新筛选条件时自动拉取最新数据。
  useEffect(() => {
    if (tab !== 'timeline') return;
    triggerRefreshTimeline(undefined);
  }, [tab, query, triggerRefreshTimeline]);

  // selectSubagent 在当前选中用户下立即切换到指定 Subagent（空字符串回到 Main）。
  const selectSubagent = useCallback(
    (subagentID: string) => {
      setFilters((previous) => ({ ...previous, subagent: subagentID }));
      setQuery((previous) => ({ ...previous, subagent: subagentID }));
    },
    [setFilters, setQuery],
  );

  // loadTimelineMessage 按条读取时间线全文与截图，成功后按钮自动消失，失败时恢复按钮。
  const handleLoadFull = useCallback(
    async (id: string, button: HTMLButtonElement) => {
      button.disabled = true;
      const exit = await loadFullMessage(id);
      if (Exit.isFailure(exit) && !Cause.hasInterruptsOnly(exit.cause)) {
        notify(formatAdminCause(exit.cause));
        button.disabled = false;
      }
    },
    [loadFullMessage, notify],
  );

  // loadEarlierTimeline 向前翻一页时间线，失败时提示并恢复按钮。
  const handleLoadEarlier = useCallback(
    async (button: HTMLButtonElement) => {
      button.disabled = true;
      const exit = await loadEarlier(undefined);
      if (Exit.isFailure(exit) && !Cause.hasInterruptsOnly(exit.cause)) {
        notify(formatAdminCause(exit.cause));
        button.disabled = false;
      }
    },
    [loadEarlier, notify],
  );

  const settingsPage = tab === 'settings';
  const timelineBusy = AsyncResult.isWaiting(refreshTimelineResult);
  const subagentOptions = filters.user === query.user ? (snapshot?.subagents?.items ?? []) : [];

  return (
    <div className="layout">
      <aside className="sidebar">
        <a className="brand" href="/">
          <img className="brand-icon" src="/app-icon.png" alt="aether" width={34} height={34} />
          <span>
            aether<span className="brand-sub">CONTROL ROOM</span>
          </span>
        </a>
        <div className="nav-label">工作空间</div>
        <nav aria-label="管理导航">
          {(Object.keys(tabs) as AdminTab[]).map((key) => (
            <button
              key={key}
              type="button"
              className={`nav-item${tab === key ? ' selected' : ''}`}
              onClick={() => setTab(key)}
            >
              <span className="nav-icon">{tabGlyphs[key]}</span>
              <span>{tabs[key][0]}</span>
            </button>
          ))}
        </nav>
        <div className="sidebar-note">
          <span className="live-dot" /> 管理员工作空间<p>运行过程与配置</p>
        </div>
        <div className="account">
          <div className="avatar">{adminName.charAt(0).toUpperCase()}</div>
          <div>
            <strong>{adminName}</strong>
            <span>管理员</span>
          </div>
          <button
            type="button"
            title="退出登录"
            aria-label="退出登录"
            onClick={() => void onLogout()}
          >
            ↗
          </button>
        </div>
      </aside>
      <main className="main">
        <header className="topbar">
          <div className="breadcrumb">
            工作空间 <span>/</span> <strong>{tabs[tab][0]}</strong>
          </div>
          <div className="topbar-right">
            <span className="readonly">◉ 管理员</span>
            <span className="timezone">{Intl.DateTimeFormat().resolvedOptions().timeZone}</span>
          </div>
        </header>
        <section className="workspace">
          <div className="page-heading">
            <div>
              <div className="eyebrow">OBSERVABILITY</div>
              <h1>{tabs[tab][0]}</h1>
              <p>{tabs[tab][1]}</p>
            </div>
            <RefreshControls
              settingsPage={settingsPage}
              browsingPages={browsingPages}
              updatedText={updatedText}
              timelineBusy={timelineBusy}
              onRefreshTimeline={() => triggerRefreshTimeline(undefined)}
            />
          </div>
          {!settingsPage ? (
            <form
              className="filters"
              aria-label="运行过程查询"
              onSubmit={(event) => {
                event.preventDefault();
                setQuery({ ...filters });
              }}
            >
              <label>
                用户
                <select
                  id="user-filter"
                  value={filters.user}
                  onChange={(event) =>
                    setFilters({ ...filters, user: event.target.value, subagent: '' })
                  }
                >
                  <option value="">请选择用户</option>
                  {[...knownUsers].map(([id, email]) => (
                    <option key={id} value={id}>
                      {email}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                Agent
                <select
                  value={filters.subagent}
                  disabled={!filters.user}
                  onChange={(event) => setFilters({ ...filters, subagent: event.target.value })}
                >
                  <option value="">Main（主对话）</option>
                  {subagentOptions.map((item) => (
                    <option key={item.id} value={item.id}>
                      {`[${outcomeLabels[item.status] || item.status}] ${item.title} (${item.id.slice(0, 8)})`}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                时间范围
                <select
                  value={filters.hours}
                  onChange={(event) =>
                    setFilters({ ...filters, hours: Number(event.target.value) })
                  }
                >
                  <option value={24}>最近 24 小时</option>
                  <option value={168}>最近 7 天</option>
                  <option value={720}>最近 30 天</option>
                </select>
              </label>
              <button className="button primary" type="submit" disabled={timelineBusy}>
                查询
              </button>
              <button
                type="button"
                className="text-button"
                onClick={() => {
                  setFilters(defaultFilters);
                  setQuery({ ...defaultFilters });
                }}
              >
                重置
              </button>
            </form>
          ) : null}
          {loadError && !settingsPage ? (
            <div className="load-error" role="alert">
              {`${loadError}${snapshot ? ' 当前保留上次成功的数据。' : ''}`}
            </div>
          ) : null}
          <div aria-live="polite">
            {tab === 'settings' ? (
              <>
                <RuntimeSettingsPage />
                <div className="settings-services">
                  <ChatGPTSubscriptionPage />
                </div>
                <div className="settings-services">
                  <ModelProvidersPage />
                </div>
                <div className="settings-services">
                  <ServiceSettingsPage />
                </div>
              </>
            ) : !snapshot ? (
              loadError ? (
                <Empty title="暂时没有读取到数据" description="恢复连接后点击右上角刷新。" />
              ) : (
                <div className="loading-card">正在读取运行数据…</div>
              )
            ) : (
              <TimelinePage
                user={query.user}
                subagent={query.subagent}
                snapshot={snapshot}
                timeline={timeline}
                openState={timelineOpen.current}
                fullItems={fullItems}
                onSelectSubagent={selectSubagent}
                onLoadFull={(id, button) => void handleLoadFull(id, button)}
                onLoadEarlier={(button) => void handleLoadEarlier(button)}
                notify={notify}
              />
            )}
          </div>
          <footer className="footer">
            <span>
              {settingsPage
                ? '配置应用于所有用户 · 已发出的请求继续完成'
                : '最新在上 · 按查询条件显示输入、模型回复与工具调用'}
            </span>
            <span>
              {settingsPage
                ? '配置页面 / 手动刷新'
                : browsingPages
                  ? '分页浏览中 / 手动刷新'
                  : '自动刷新 / 15 秒'}
            </span>
          </footer>
        </section>
      </main>
      <div id="toast" role="status" aria-live="polite" className={toast ? 'show' : ''}>
        {toast}
      </div>
    </div>
  );
}

// RefreshControls 按当前页签触发运行过程或配置页三组 Atom 的刷新，避免在运行过程页提前挂载配置 Atom。
function RefreshControls({
  settingsPage,
  browsingPages,
  updatedText,
  timelineBusy,
  onRefreshTimeline,
}: {
  settingsPage: boolean;
  browsingPages: boolean;
  updatedText: string;
  timelineBusy: boolean;
  onRefreshTimeline: () => void;
}) {
  if (settingsPage) {
    return <SettingsRefreshControls updatedText={updatedText} />;
  }
  return (
    <div className="refresh-group">
      <span id="updated">{browsingPages ? '分页浏览中 / 手动刷新' : updatedText}</span>
      <button type="button" className="button" disabled={timelineBusy} onClick={onRefreshTimeline}>
        {timelineBusy ? '↻ 更新中' : '↻ 刷新'}
      </button>
    </div>
  );
}

// SettingsRefreshControls 只在配置页挂载时订阅各组配置 Atom 的加载状态与刷新句柄。
function SettingsRefreshControls({ updatedText }: { updatedText: string }) {
  const settingsBusy = useAtomValue(settingsBusyAtom);
  const refreshRuntime = useAtomRefresh(runtimeSettingsAtom);
  const refreshChatGPT = useAtomRefresh(chatGPTStatusAtom);
  const refreshProviders = useAtomRefresh(modelProvidersAtom);
  const refreshServices = useAtomRefresh(serviceSettingsAtom);

  return (
    <div className="refresh-group">
      <span id="updated">{updatedText}</span>
      <button
        type="button"
        className="button"
        disabled={settingsBusy}
        onClick={() => {
          refreshRuntime();
          refreshChatGPT();
          refreshProviders();
          refreshServices();
        }}
      >
        {settingsBusy ? '↻ 更新中' : '↻ 刷新'}
      </button>
    </div>
  );
}

// LoginPage 是管理员密码登录页：通过 loginAdminAtom 换取管理令牌并验证会话。
function LoginPage() {
  const login = useAtomSet(loginAdminAtom, { mode: 'promiseExit' });
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setError('');
    try {
      const exit = await login(password);
      if (Exit.isFailure(exit)) {
        setError(formatAdminCause(exit.cause, '登录失败，请重试。'));
        return;
      }
      setPassword('');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <main className="login-page">
      <div className="login-story">
        <a className="brand" href="/">
          <img className="brand-icon" src="/app-icon.png" alt="aether" width={34} height={34} />
          <span>
            aether<span className="brand-sub">CONTROL ROOM</span>
          </span>
        </a>
        <div className="login-story-body">
          <div className="eyebrow">aether</div>
          <h1>
            每一次运行，
            <br />
            清晰可见。
          </h1>
          <p>
            为用户与 Agent 的工作提供一扇窗口。
            <br />
            从输入、推理到工具调用。
          </p>
          <div className="orbit">
            <span />
            <span />
            <span />
            <img src="/app-icon.png" alt="" width={130} height={130} />
          </div>
        </div>
        <div className="login-foot">
          ADMIN CONSOLE <span>运行过程 · 配置</span>
        </div>
      </div>
      <div className="login-form-wrap">
        <form className="login-form" onSubmit={submit}>
          <div className="eyebrow teal">管理员访问</div>
          <h2>欢迎回来</h2>
          <p className="muted">输入管理员密码，进入管理看板。</p>
          <label>
            管理员密码
            <input
              type="password"
              autoComplete="current-password"
              placeholder="输入管理员密码"
              maxLength={1024}
              required
              autoFocus
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
          </label>
          <div className="form-error" role="alert">
            {error}
          </div>
          <button className="button primary" type="submit" disabled={submitting}>
            进入看板 <span>→</span>
          </button>
          <p className="login-note">查看 Agent 运行过程，并管理全局参数与服务配置。</p>
        </form>
      </div>
    </main>
  );
}
