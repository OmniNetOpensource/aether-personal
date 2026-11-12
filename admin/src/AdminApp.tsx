// 本文件是管理看板应用组件：持有管理员登录、筛选与各页数据（支持切换查看 Main 与后台 Subagent 轨迹），编排自动刷新，并切换运行过程、配置页面与调用详情弹窗。

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  api,
  ApiError,
  formatTime,
  outcomeLabels,
  setAdminToken,
  type Call,
  type Snapshot,
  type Timeline,
  type TimelineItem,
} from './api.js';
import { CallDetailDialog } from './CallDetail.js';
import { ModelSettingsPage } from './ModelSettings.js';
import { ServiceSettingsPage } from './ServiceSettings.js';
import { TimelinePage } from './Timeline.js';
import { Empty } from './ui.js';

type Tab = 'timeline' | 'settings';

// TimelineFilters 区分编辑中的条件与已经提交的查询，自动刷新和翻页只使用已提交值。
interface TimelineFilters {
  hours: number;
  user: string;
  subagent: string;
  model: string;
  outcome: string;
}
const defaultFilters: TimelineFilters = { hours: 24, user: '', subagent: '', model: '', outcome: '' };

const tabs: Record<Tab, [string, string]> = {
  timeline: ['运行过程', '按时间查看 Main 与后台 Subagent 收到的输入、推理与工具调用。'],
  settings: ['配置', '选择对话、Subagent 与压缩模型，并管理模型、邮件与联网服务的凭证。'],
};
const tabGlyphs: Record<Tab, string> = { timeline: '⋮', settings: '⚙' };

// AdminApp 是看板根组件：未登录时显示密码登录，登录后显示侧边栏外壳与当前页。
export function AdminApp() {
  const [booting, setBooting] = useState(true);
  const [me, setMe] = useState<{ name: string } | null>(null);
  const [denied, setDenied] = useState('');
  const [tab, setTab] = useState<Tab>('timeline');
  const [filters, setFilters] = useState(defaultFilters);
  const [query, setQuery] = useState(defaultFilters);
  const [knownUsers, setKnownUsers] = useState(new Map<string, string>());
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [timeline, setTimeline] = useState<(Timeline & { key: string }) | null>(null);
  const [updatedText, setUpdatedText] = useState('尚未刷新');
  const [loadError, setLoadError] = useState('');
  const [refreshing, setRefreshing] = useState(false);
  const [browsingPages, setBrowsingPages] = useState(false);
  const [selectedCall, setSelectedCall] = useState<Call | null>(null);
  const [toast, setToast] = useState('');
  const [settingsRefreshKey, setSettingsRefreshKey] = useState(0);
  const [modelLoading, setModelLoading] = useState(false);
  const [serviceLoading, setServiceLoading] = useState(false);

  const controller = useRef<AbortController | null>(null);
  const generation = useRef(0);
  const browsingRef = useRef(false);
  const tabRef = useRef<Tab>('timeline');
  // 时间线展开状态与已加载全文跨自动刷新保留，沿用改造前 Map 的行为。
  const timelineOpen = useRef(new Map<string, boolean>());
  const timelineFull = useRef(new Map<string, TimelineItem>());
  const windowRef = useRef<{ from: string; to: string } | null>(null);
  const filtersRef = useRef(query);
  filtersRef.current = query;
  tabRef.current = tab;
  browsingRef.current = browsingPages;

  // notify 显示右下角短提示，四秒后自动收起。
  const notify = useCallback((message: string) => {
    setToast(message);
  }, []);
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast(''), 4000);
    return () => clearTimeout(timer);
  }, [toast]);

  // clearData 在退出或权限失效时清掉全部看板数据与未完成的请求。
  const clearData = useCallback(() => {
    controller.current?.abort();
    generation.current++;
    setMe(null);
    setSnapshot(null);
    setTimeline(null);
    timelineOpen.current.clear();
    timelineFull.current.clear();
    setFilters(defaultFilters);
    setQuery(defaultFilters);
    setKnownUsers(new Map());
    windowRef.current = null;
    setUpdatedText('尚未刷新');
    setBrowsingPages(false);
    setSelectedCall(null);
  }, []);

  // accessLost 处理令牌或权限失效：401 回登录页，403 显示无权页。
  const accessLost = useCallback(
    (error: ApiError) => {
      clearData();
      if (error.status === 401) {
        setAdminToken(null);
        sessionStorage.removeItem('aether_admin_token');
        setDenied('');
        setBooting(false);
      } else {
        setDenied(error.message);
      }
    },
    [clearData],
  );

  // buildQuery 为查询与翻页共用同一时间窗口和已提交条件。
  const buildQuery = useCallback((extra: Record<string, string | null> = {}) => {
    const current = filtersRef.current;
    const params = new URLSearchParams(windowRef.current ?? {});
    if (current.user) params.set('user_id', current.user);
    if (current.subagent) params.set('subagent_id', current.subagent);
    if (current.model) params.set('model', current.model);
    if (current.outcome) params.set('outcome', current.outcome);
    for (const [key, value] of Object.entries(extra)) if (value != null) params.set(key, value);
    return params.toString();
  }, []);

  // selectSubagent 在当前选中用户下立即切换到指定 Subagent（空字符串回到 Main）。
  const selectSubagent = useCallback((subagentID: string) => {
    setFilters((previous) => ({ ...previous, subagent: subagentID }));
    setQuery((previous) => ({ ...previous, subagent: subagentID }));
  }, []);

  // refresh 重新读取当前页数据，并按已提交的小时数重算时间窗口。
  const refresh = useCallback(async () => {
    if (tabRef.current === 'settings') {
      setSettingsRefreshKey((value) => value + 1);
      return;
    }
    setBrowsingPages(false);
    controller.current?.abort();
    const abort = new AbortController();
    controller.current = abort;
    const current = ++generation.current;
    const now = new Date();
    windowRef.current = {
      from: new Date(now.getTime() - filtersRef.current.hours * 3600000).toISOString(),
      to: now.toISOString(),
    };
    setRefreshing(true);
    try {
      const user = filtersRef.current.user;
      const subagent = filtersRef.current.subagent;
      const timelineQuery = buildQuery();
      const snapshotQuery = new URLSearchParams(windowRef.current ?? {});
      if (user) snapshotQuery.set('user_id', user);
      const [loadedSnapshot, list] = await Promise.all([
        api<Snapshot>(`/admin/snapshot?${snapshotQuery}`, { signal: abort.signal }),
        user ? api<Timeline>(`/admin/timeline?${timelineQuery}`, { signal: abort.signal }) : null,
      ]);
      if (current !== generation.current) return;
      setSnapshot(loadedSnapshot);
      setKnownUsers((previous) => {
        const users = new Map(previous);
        for (const item of loadedSnapshot.users.items) users.set(item.id, item.email);
        return users;
      });
      setTimeline(list ? { ...list, key: `${user}:${subagent}` } : null);
      setUpdatedText(`${formatTime(loadedSnapshot.generated_at)} 已更新`);
      setLoadError('');
    } catch (error) {
      if (error instanceof Error && error.name === 'AbortError') return;
      if (current !== generation.current) return;
      if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
        accessLost(error);
        return;
      }
      const message = error instanceof Error ? error.message : '加载失败，请稍后重试。';
      setLoadError(message);
    } finally {
      if (current === generation.current) setRefreshing(false);
    }
  }, [accessLost, buildQuery]);

  // 启动时若已有标签页令牌，先验证管理员身份再进看板，失效则回登录页。
  useEffect(() => {
    const token = sessionStorage.getItem('aether_admin_token');
    if (!token) {
      setBooting(false);
      return;
    }
    setAdminToken(token);
    api<{ name: string }>('/admin/me')
      .then((loaded) => {
        setMe(loaded);
        setBooting(false);
      })
      .catch(() => {
        setAdminToken(null);
        sessionStorage.removeItem('aether_admin_token');
        setBooting(false);
      });
  }, []);

  // 登录成功后首次加载，以及筛选或页签变化时重新读取（设置页自行加载）。
  useEffect(() => {
    if (!me || tab !== 'timeline') return;
    void refresh();
  }, [me, tab, query, refresh]);

  // 每 15 秒自动刷新；翻页浏览、弹窗打开、设置页与输入聚焦时暂停，页面切回前台时补一次。
  useEffect(() => {
    if (!me) return;
    const timer = setInterval(() => {
      if (
        document.hidden ||
        tabRef.current === 'settings' ||
        browsingRef.current ||
        selectedCall ||
        document.querySelector('input:focus')
      )
        return;
      void refresh();
    }, 15000);
    const visibility = () => {
      if (!document.hidden && tabRef.current === 'timeline' && !browsingRef.current && !selectedCall)
        void refresh();
    };
    document.addEventListener('visibilitychange', visibility);
    return () => {
      clearInterval(timer);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, [me, refresh, selectedCall]);

  // loadTimelineMessage 按条读取时间线全文与截图，存入保留 Map 后重绘。
  async function loadTimelineMessage(id: string, button: HTMLButtonElement) {
    const current = generation.current;
    button.disabled = true;
    try {
      const full = await api<TimelineItem>(`/admin/timeline/messages/${id}`, {
        signal: controller.current?.signal,
      });
      if (current !== generation.current || !me) return;
      timelineFull.current.set(id, full);
      setTimeline((value) => (value ? { ...value } : value));
    } catch (error) {
      if (error instanceof Error && error.name === 'AbortError') return;
      if (current !== generation.current) return;
      if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
        accessLost(error);
        return;
      }
      notify(error instanceof Error ? error.message : '加载失败，请稍后重试。');
      button.disabled = false;
    }
  }

  // loadEarlierTimeline 向前翻一页时间线并暂停自动刷新，避免刷新把已加载的更早记录替换掉。
  async function loadEarlierTimeline(button: HTMLButtonElement) {
    if (!timeline?.next_cursor || !controller.current) return;
    const current = generation.current;
    const signal = controller.current.signal;
    button.disabled = true;
    try {
      const pageData = await api<Timeline>(
        `/admin/timeline?${buildQuery({ before: timeline.next_cursor })}`,
        { signal },
      );
      if (current !== generation.current || signal.aborted || !me) return;
      setTimeline({
        ...timeline,
        items: [...pageData.items, ...timeline.items],
        next_cursor: pageData.next_cursor,
      });
      setBrowsingPages(true);
    } catch (error) {
      if (error instanceof Error && error.name === 'AbortError') return;
      if (current !== generation.current) return;
      if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
        accessLost(error);
        return;
      }
      notify(error instanceof Error ? error.message : '加载失败，请稍后重试。');
      button.disabled = false;
    }
  }

  // logout 撤销当前管理会话并回登录页；撤销失败（除已过期外）留在原页提示。
  async function logout() {
    try {
      await api<null>('/admin/logout', { method: 'POST' });
    } catch (error) {
      if (!(error instanceof ApiError) || error.status !== 401) {
        notify('暂时无法退出，请稍后重试。');
        return;
      }
    }
    clearData();
    setDenied('');
    setAdminToken(null);
    sessionStorage.removeItem('aether_admin_token');
    setBooting(false);
  }

  const onSettingsUpdated = useCallback((text: string) => setUpdatedText(text), []);
  // 模型与凭证各自报告加载；刷新按钮等到两段都结束，避免先完成的一段把状态清掉。
  const onModelLoading = useCallback((loading: boolean) => {
    setModelLoading(loading);
    if (!loading) setLoadError('');
  }, []);
  const onServiceLoading = useCallback((loading: boolean) => {
    setServiceLoading(loading);
    if (!loading) setLoadError('');
  }, []);

  if (booting) return <div className="boot">正在验证管理员身份…</div>;
  if (denied)
    return (
      <main className="access-denied">
        <div className="eyebrow teal">ACCESS RESTRICTED</div>
        <h1>管理权限已不可用</h1>
        <p>{denied}</p>
        <button type="button" className="button primary" onClick={() => void logout()}>
          重新登录
        </button>
      </main>
    );
  if (!me)
    return (
      <LoginPage
        onLoggedIn={(name) => {
          setMe({ name });
        }}
      />
    );

  const settingsPage = tab === 'settings';
  const pageBusy = settingsPage ? modelLoading || serviceLoading : refreshing;
  const subagentOptions = filters.user === query.user ? (snapshot?.subagents?.items ?? []) : [];

  return (
    <div className="layout">
      <aside className="sidebar">
        <a className="brand" href="/">
          <img className="brand-icon" src="/app-icon.png" alt="阿芒" width={34} height={34} />
          <span>
            阿芒<span className="brand-sub">CONTROL ROOM</span>
          </span>
        </a>
        <div className="nav-label">工作空间</div>
        <nav aria-label="管理导航">
          {(Object.keys(tabs) as Tab[]).map((key) => (
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
          <span className="live-dot" /> 管理员工作空间<p>调用观测与配置</p>
        </div>
        <div className="account">
          <div className="avatar">{me.name.charAt(0).toUpperCase()}</div>
          <div>
            <strong>{me.name}</strong>
            <span>管理员</span>
          </div>
          <button type="button" title="退出登录" aria-label="退出登录" onClick={() => void logout()}>
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
            <div className="refresh-group">
              <span id="updated">{browsingPages ? '分页浏览中 / 手动刷新' : `${updatedText}`}</span>
              <button type="button" className="button" disabled={pageBusy} onClick={() => void refresh()}>
                {pageBusy ? '↻ 更新中' : '↻ 刷新'}
              </button>
            </div>
          </div>
          {!settingsPage ? (
            <form
              className="filters"
              aria-label="运行过程查询"
              onSubmit={(event) => {
                event.preventDefault();
                setQuery({ ...filters, model: filters.model.trim() });
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
                  onChange={(event) => setFilters({ ...filters, hours: Number(event.target.value) })}
                >
                  <option value={24}>最近 24 小时</option>
                  <option value={168}>最近 7 天</option>
                  <option value={720}>最近 30 天</option>
                </select>
              </label>
              <label>
                模型
                <input
                  list="model-options"
                  placeholder="全部模型"
                  maxLength={160}
                  value={filters.model}
                  onChange={(event) => setFilters({ ...filters, model: event.target.value })}
                />
                <datalist id="model-options">
                  {(snapshot?.models ?? []).map((item) => (
                    <option key={item.model} value={item.model} />
                  ))}
                </datalist>
              </label>
              <label>
                请求结果
                <select
                  value={filters.outcome}
                  onChange={(event) => setFilters({ ...filters, outcome: event.target.value })}
                >
                  <option value="">全部结果</option>
                  <option value="succeeded">成功</option>
                  <option value="in_flight">请求中</option>
                  <option value="http_error">HTTP 错误</option>
                  <option value="transport_error">连接异常</option>
                  <option value="invalid_response">响应异常</option>
                  <option value="interrupted">中断</option>
                </select>
              </label>
              <button className="button primary" type="submit" disabled={pageBusy}>
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
                <ModelSettingsPage
                  refreshKey={settingsRefreshKey}
                  onUpdated={onSettingsUpdated}
                  onAccessLost={accessLost}
                  onLoadingChange={onModelLoading}
                  notify={notify}
                />
                <div className="settings-services">
                  <ServiceSettingsPage
                    refreshKey={settingsRefreshKey}
                    onUpdated={onSettingsUpdated}
                    onAccessLost={accessLost}
                    onLoadingChange={onServiceLoading}
                    notify={notify}
                  />
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
                fullItems={timelineFull.current}
                onSelectSubagent={selectSubagent}
                onLoadFull={(id, button) => void loadTimelineMessage(id, button)}
                onLoadEarlier={(button) => void loadEarlierTimeline(button)}
                onOpenCall={setSelectedCall}
                notify={notify}
              />
            )}
          </div>
          <footer className="footer">
            <span>
              {settingsPage
                ? '配置应用于所有用户 · 已发出的请求继续完成'
                : '最新在上 · 按查询条件显示输入、模型回复、工具与请求结果'}
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
      {selectedCall ? (
        <CallDetailDialog
          call={selectedCall}
          onClose={() => setSelectedCall(null)}
          onAccessLost={accessLost}
          notify={notify}
        />
      ) : null}
      <div id="toast" role="status" aria-live="polite" className={toast ? 'show' : ''}>
        {toast}
      </div>
    </div>
  );
}

// LoginPage 是管理员密码登录页：密码正确后换管理令牌进看板，令牌只存当前标签页。
function LoginPage({ onLoggedIn }: { onLoggedIn: (name: string) => void }) {
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  // 提交密码换取管理令牌，再读一次管理员身份确认会话可用。
  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setError('');
    try {
      const verified = await api<{ token: string }>('/admin/login', {
        method: 'POST',
        body: JSON.stringify({ password }),
      });
      setPassword('');
      setAdminToken(verified.token);
      sessionStorage.setItem('aether_admin_token', verified.token);
      const me = await api<{ name: string }>('/admin/me');
      onLoggedIn(me.name);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '登录失败，请重试。');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <main className="login-page">
      <div className="login-story">
        <a className="brand" href="/">
          <img className="brand-icon" src="/app-icon.png" alt="阿芒" width={34} height={34} />
          <span>
            阿芒<span className="brand-sub">CONTROL ROOM</span>
          </span>
        </a>
        <div className="login-story-body">
          <div className="eyebrow">阿芒 · AETHER</div>
          <h1>
            每一次调用，
            <br />
            清晰可见。
          </h1>
          <p>
            为用户与 Agent 的工作提供一扇窗口。
            <br />
            从运行过程，深入到每一个请求。
          </p>
          <div className="orbit">
            <span />
            <span />
            <span />
            <img src="/app-icon.png" alt="" width={130} height={130} />
          </div>
        </div>
        <div className="login-foot">
          ADMIN CONSOLE <span>调用观测 · 配置</span>
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
          <p className="login-note">查看模型调用、请求与响应 JSON，并管理模型与服务配置。</p>
        </form>
      </div>
    </main>
  );
}
