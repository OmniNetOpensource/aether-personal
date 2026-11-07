// 本文件是管理看板应用组件：持有管理员登录、筛选与各页数据，编排自动刷新，并切换五个页面与调用详情弹窗。

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  api,
  ApiError,
  formatTime,
  setAdminToken,
  type Call,
  type Page,
  type QuotaItem,
  type Snapshot,
  type Timeline,
  type TimelineItem,
} from './api.js';
import { CallDetailDialog, CallsPage } from './Calls.js';
import { ModelSettingsPage } from './ModelSettings.js';
import { OverviewPage } from './Overview.js';
import { ServiceSettingsPage } from './ServiceSettings.js';
import { TimelinePage } from './Timeline.js';
import { Empty } from './ui.js';

type Tab = 'overview' | 'calls' | 'timeline' | 'models' | 'services';

const tabs: Record<Tab, [string, string]> = {
  overview: ['运行概览', '从用户到每一次调用，了解 Agent 正在如何工作。'],
  calls: ['模型调用', '真实请求尝试、提供方返回用量与结果。'],
  timeline: ['运行过程', '按时间查看 Main 收到的输入、推理与工具调用。'],
  models: ['模型配置', '分别选择对话与压缩模型，从下一次请求生效。'],
  services: ['服务配置', '管理模型、邮件与联网服务的 API Key，保存后从下一次请求生效。'],
};
const tabGlyphs: Record<Tab, string> = { overview: '◫', calls: '↗', timeline: '⋮', models: '⇄', services: '⚙' };

// AdminApp 是看板根组件：未登录时显示密码登录，登录后显示侧边栏外壳与当前页。
export function AdminApp() {
  const [booting, setBooting] = useState(true);
  const [me, setMe] = useState<{ name: string } | null>(null);
  const [denied, setDenied] = useState('');
  const [tab, setTab] = useState<Tab>('overview');
  const [hours, setHours] = useState(24);
  const [user, setUser] = useState('');
  const [model, setModel] = useState('');
  const [modelInput, setModelInput] = useState('');
  const [outcome, setOutcome] = useState('');
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [calls, setCalls] = useState<Page<Call> | null>(null);
  const [timeline, setTimeline] = useState<(Timeline & { key: string }) | null>(null);
  const [updatedText, setUpdatedText] = useState('尚未刷新');
  const [loadError, setLoadError] = useState('');
  const [refreshing, setRefreshing] = useState(false);
  const [browsingPages, setBrowsingPages] = useState(false);
  const [quotas, setQuotas] = useState<{ items: QuotaItem[] } | null>(null);
  const [selectedCall, setSelectedCall] = useState<Call | null>(null);
  const [toast, setToast] = useState('');
  const [settingsRefreshKey, setSettingsRefreshKey] = useState(0);

  const controller = useRef<AbortController | null>(null);
  const generation = useRef(0);
  const quotasAt = useRef(0);
  const browsingRef = useRef(false);
  const tabRef = useRef<Tab>('overview');
  // 时间线展开状态与已加载全文跨自动刷新保留，沿用改造前 Map 的行为。
  const timelineOpen = useRef(new Map<string, boolean>());
  const timelineFull = useRef(new Map<string, TimelineItem>());
  const windowRef = useRef<{ from: string; to: string } | null>(null);
  const filtersRef = useRef({ hours: 24, user: '', model: '', outcome: '' });
  filtersRef.current = { hours, user, model, outcome };
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
    setCalls(null);
    setTimeline(null);
    timelineOpen.current.clear();
    timelineFull.current.clear();
    setUser('');
    setModel('');
    setModelInput('');
    setOutcome('');
    setQuotas(null);
    quotasAt.current = 0;
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

  const buildQuery = useCallback((extra: Record<string, string | null> = {}) => {
    const current = filtersRef.current;
    const range = windowRef.current!;
    const params = new URLSearchParams({ from: range.from, to: range.to, limit: '50' });
    if (current.user) params.set('user_id', current.user);
    if (current.model) params.set('model', current.model);
    if (current.outcome) params.set('outcome', current.outcome);
    for (const [key, value] of Object.entries(extra)) if (value != null) params.set(key, value);
    return params.toString();
  }, []);

  // loadQuotas 向后端实时查询各供应商额度；一分钟内复用上次结果，避免概览每 15 秒自动刷新都去打供应商。
  const loadQuotas = useCallback(
    async (force: boolean, signal: AbortSignal, current: number) => {
      if (!force && quotasAt.current && Date.now() - quotasAt.current < 60000) return;
      try {
        const data = await api<{ items: QuotaItem[] }>('/admin/quotas', { signal });
        if (current !== generation.current) return;
        setQuotas(data);
      } catch (error) {
        if (error instanceof Error && error.name === 'AbortError') return;
        if (current !== generation.current) return;
        if (error instanceof ApiError && (error.status === 401 || error.status === 403)) accessLost(error);
      }
      quotasAt.current = Date.now();
    },
    [accessLost],
  );

  // refresh 重新读取当前页数据；newWindow 为真时按当前小时数重算时间窗口。
  const refresh = useCallback(
    async (newWindow = true) => {
      if (tabRef.current === 'models' || tabRef.current === 'services') {
        setSettingsRefreshKey((value) => value + 1);
        return;
      }
      setBrowsingPages(false);
      controller.current?.abort();
      const abort = new AbortController();
      controller.current = abort;
      const current = ++generation.current;
      if (newWindow || !windowRef.current) {
        const now = new Date();
        windowRef.current = {
          from: new Date(now.getTime() - filtersRef.current.hours * 3600000).toISOString(),
          to: now.toISOString(),
        };
      }
      setRefreshing(true);
      try {
        const currentTab = tabRef.current;
        const requests: Promise<unknown>[] = [
          api<Snapshot>(`/admin/snapshot?${buildQuery()}`, { signal: abort.signal }),
        ];
        if (currentTab === 'calls')
          requests.push(api<Page<Call>>(`/admin/calls?${buildQuery()}`, { signal: abort.signal }));
        if (currentTab === 'timeline' && filtersRef.current.user)
          requests.push(
            api<Timeline>(`/admin/timeline?${new URLSearchParams({ user_id: filtersRef.current.user })}`, {
              signal: abort.signal,
            }),
          );
        const [loadedSnapshot, list] = await Promise.all(requests);
        // 概览的额度面板顺带更新，但额度查询慢或失败不能拖住概览本身。
        if (currentTab === 'overview') await loadQuotas(false, abort.signal, current);
        if (current !== generation.current) return;
        setSnapshot(loadedSnapshot as Snapshot);
        if (currentTab === 'calls') setCalls(list as Page<Call>);
        if (currentTab === 'timeline')
          setTimeline(list ? { ...(list as Timeline), key: filtersRef.current.user } : null);
        setUpdatedText(`${formatTime((loadedSnapshot as Snapshot).generated_at)} 已更新`);
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
    },
    [accessLost, buildQuery, loadQuotas],
  );

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
  const firstLoad = useRef(true);
  useEffect(() => {
    if (!me) return;
    if (tab === 'models' || tab === 'services') return;
    if (firstLoad.current) {
      firstLoad.current = false;
      void refresh();
      return;
    }
    void refresh(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [me, tab, hours, user, model, outcome]);

  // 每 15 秒自动刷新；翻页浏览、弹窗打开、设置页与输入聚焦时暂停，页面切回前台时补一次。
  useEffect(() => {
    if (!me) return;
    const timer = setInterval(() => {
      if (
        document.hidden ||
        tabRef.current === 'models' ||
        tabRef.current === 'services' ||
        browsingRef.current ||
        selectedCall ||
        document.querySelector('input:focus')
      )
        return;
      void refresh();
    }, 15000);
    const visibility = () => {
      if (!document.hidden && tabRef.current !== 'models' && !browsingRef.current) void refresh();
    };
    document.addEventListener('visibilitychange', visibility);
    return () => {
      clearInterval(timer);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, [me, refresh, selectedCall]);

  // loadMoreCalls 把下一页调用追加到列表，并暂停自动刷新避免被覆盖。
  async function loadMoreCalls(button: HTMLButtonElement) {
    if (!calls?.next_cursor || !controller.current) return;
    const current = generation.current;
    const signal = controller.current.signal;
    button.disabled = true;
    try {
      const response = await api<Page<Call>>(`/admin/calls?${buildQuery({ cursor: calls.next_cursor })}`, {
        signal,
      });
      if (current !== generation.current || signal.aborted || !me) return;
      setCalls({ items: [...calls.items, ...response.items], next_cursor: response.next_cursor });
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
        `/admin/timeline?${new URLSearchParams({ user_id: user, before: timeline.next_cursor })}`,
        { signal },
      );
      if (current !== generation.current || signal.aborted || !me) return;
      setTimeline({ ...timeline, items: [...pageData.items, ...timeline.items], next_cursor: pageData.next_cursor });
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
  const onSettingsLoading = useCallback(
    (loading: boolean) => {
      setRefreshing(loading);
      if (!loading) setLoadError('');
    },
    [],
  );

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

  const settingsPage = tab === 'models' || tab === 'services';
  const timelinePage = tab === 'timeline';
  const knownUsers = new Map<string, string>();
  for (const item of snapshot?.users.items ?? []) knownUsers.set(item.id, item.email);
  if (user && snapshot) {
    const found = snapshot.users.items.find((item) => item.id === user);
    if (found) knownUsers.set(found.id, found.email);
  }

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
          <span className="live-dot" /> 管理员工作空间<p>调用观测与模型配置</p>
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
              <button type="button" className="button" disabled={refreshing} onClick={() => void refresh()}>
                {refreshing ? '↻ 更新中' : '↻ 刷新'}
              </button>
            </div>
          </div>
          {!settingsPage ? (
            <div className="filters">
              {!timelinePage ? (
                <label>
                  时间范围
                  <select value={hours} onChange={(event) => setHours(Number(event.target.value))}>
                    <option value={24}>最近 24 小时</option>
                    <option value={168}>最近 7 天</option>
                    <option value={720}>最近 30 天</option>
                  </select>
                </label>
              ) : null}
              <label>
                用户
                <select value={user} onChange={(event) => setUser(event.target.value)}>
                  <option value="">全部用户</option>
                  {[...knownUsers].map(([id, email]) => (
                    <option key={id} value={id}>
                      {email}
                    </option>
                  ))}
                </select>
              </label>
              {!timelinePage ? (
                <>
                  <label>
                    模型
                    <input
                      list="model-options"
                      placeholder="全部模型"
                      value={modelInput}
                      onChange={(event) => setModelInput(event.target.value)}
                      onBlur={() => setModel(modelInput.trim())}
                      onKeyDown={(event) => {
                        if (event.key === 'Enter') setModel(modelInput.trim());
                      }}
                    />
                    <datalist id="model-options">
                      {(snapshot?.models ?? []).map((item) => (
                        <option key={item.model} value={item.model} />
                      ))}
                    </datalist>
                  </label>
                  <label>
                    请求结果
                    <select value={outcome} onChange={(event) => setOutcome(event.target.value)}>
                      <option value="">全部结果</option>
                      <option value="succeeded">成功</option>
                      <option value="in_flight">请求中</option>
                      <option value="http_error">HTTP 错误</option>
                      <option value="transport_error">连接异常</option>
                      <option value="invalid_response">响应异常</option>
                      <option value="interrupted">中断</option>
                    </select>
                  </label>
                </>
              ) : null}
              <button
                type="button"
                className="text-button"
                onClick={() => {
                  setUser('');
                  setModel('');
                  setModelInput('');
                  setOutcome('');
                  setHours(24);
                }}
              >
                重置
              </button>
            </div>
          ) : null}
          {loadError && !settingsPage ? (
            <div className="load-error" role="alert">
              {`${loadError}${snapshot ? ' 当前保留上次成功的数据。' : ''}`}
            </div>
          ) : null}
          <div aria-live="polite">
            {tab === 'models' ? (
              <ModelSettingsPage
                refreshKey={settingsRefreshKey}
                onUpdated={onSettingsUpdated}
                onAccessLost={accessLost}
                onLoadingChange={onSettingsLoading}
                notify={notify}
              />
            ) : tab === 'services' ? (
              <ServiceSettingsPage
                refreshKey={settingsRefreshKey}
                onUpdated={onSettingsUpdated}
                onAccessLost={accessLost}
                onLoadingChange={onSettingsLoading}
                notify={notify}
              />
            ) : !snapshot ? (
              loadError ? (
                <Empty title="暂时没有读取到数据" description="恢复连接后点击右上角刷新。" />
              ) : (
                <div className="loading-card">正在读取运行数据…</div>
              )
            ) : tab === 'overview' ? (
              <OverviewPage
                snapshot={snapshot}
                hours={hours}
                quotas={quotas}
                onRefreshQuotas={() => {
                  if (controller.current) void loadQuotas(true, controller.current.signal, generation.current);
                }}
                onViewUserCalls={(userId) => {
                  setUser(userId);
                  setTab('calls');
                }}
                onViewCalls={() => setTab('calls')}
              />
            ) : tab === 'calls' ? (
              <CallsPage
                snapshot={snapshot}
                calls={calls}
                onLoadMore={(button) => void loadMoreCalls(button)}
                onOpenCall={setSelectedCall}
                notify={notify}
              />
            ) : (
              <TimelinePage
                user={user}
                snapshot={snapshot}
                timeline={timeline}
                openState={timelineOpen.current}
                fullItems={timelineFull.current}
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
                : timelinePage
                  ? '最新在上 · 失败、中断与请求中的调用按开始时间插入'
                  : '按实际请求统计 · 调用详情可查看请求与响应 JSON'}
            </span>
            <span>
              {settingsPage ? '配置页面 / 手动刷新' : browsingPages ? '分页浏览中 / 手动刷新' : '自动刷新 / 15 秒'}
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
            从整体运行趋势，深入到每一个请求。
          </p>
          <div className="orbit">
            <span />
            <span />
            <span />
            <img src="/app-icon.png" alt="" width={130} height={130} />
          </div>
        </div>
        <div className="login-foot">
          ADMIN CONSOLE <span>调用观测 · 模型配置</span>
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
          <p className="login-note">查看模型调用、请求与响应 JSON，并管理模型配置。</p>
        </form>
      </div>
    </main>
  );
}
