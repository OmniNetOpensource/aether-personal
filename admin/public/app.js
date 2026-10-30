// 本文件是管理看板的前端脚本：负责管理员登录、调用观测、Agent 运行过程时间线、模型选择与服务凭证配置；完整 Key 只用于当次提交，不存入页面状态。

const root = document.querySelector('#app');
const dialog = document.querySelector('#detail');
const tabs = { overview: ['运行概览', '从用户到每一次调用，了解 Agent 正在如何工作。'], calls: ['模型调用', '真实请求尝试、提供方返回用量与结果。'], timeline: ['运行过程', '按时间查看单个 Agent 收到的输入、推理与工具调用。'] };
tabs.models = ['模型配置', '选择 Main 与 Subagent 使用的模型，应用后从下一次请求生效。'];
tabs.services = ['服务配置', '管理模型、邮件与联网服务的 API Key，保存后从下一次请求生效。'];
const outcomeLabels = { in_flight: '请求中', succeeded: '成功', http_error: 'HTTP 错误', transport_error: '连接异常', invalid_response: '响应异常', interrupted: '中断 · 结果未知', processing: '处理中', idle: '空闲', failed: '失败' };
const state = { token: sessionStorage.getItem('aether_admin_token'), me: null, tab: 'overview', hours: 24, user: '', agent: '', model: '', outcome: '', snapshot: null, calls: null, timeline: null, timelineOpen: new Map(), timelineFull: new Map(), window: null, controller: null, generation: 0, updated: null };
const number = (value) => new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 0 }).format(value ?? 0);
const compact = (value) => new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 }).format(value ?? 0);
const time = (value) => value ? new Date(value).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) : '—';
const money = (value) => value == null ? '未知' : `$${Number(value).toLocaleString('en-US', { minimumFractionDigits: 4, maximumFractionDigits: 6 })}`;
const duration = (value) => value == null ? '未知' : value < 1000 ? `${Math.round(value)} ms` : `${(value / 1000).toFixed(2)} s`;
function el(tag, className, text) { const node = document.createElement(tag); if (className) node.className = className; if (text !== undefined) node.textContent = text; return node; }
function button(text, handler, className = 'button') { const node = el('button', className, text); node.type = 'button'; node.addEventListener('click', handler); return node; }
function pill(status) { return el('span', `pill ${status}`, outcomeLabels[status] || status); }
function shortModel(value) { return value?.split('/').slice(1).join('/') || value || '未知'; }
function notify(message) { const toast = document.querySelector('#toast'); toast.textContent = message; toast.classList.add('show'); clearTimeout(notify.timer); notify.timer = setTimeout(() => toast.classList.remove('show'), 4000); }
async function api(path, options = {}) {
  const response = await fetch(`/api${path}`, { ...options, headers: { ...(state.token ? { Authorization: `Bearer ${state.token}` } : {}), ...(options.body ? { 'Content-Type': 'application/json' } : {}), ...options.headers } });
  if (!response.ok) {
    const error = new Error(response.status === 401 ? (path === '/admin/login' ? '密码错误，请重试。' : '登录已过期，请重新登录。') : response.status === 403 ? '管理权限已不可用，请重新登录。' : response.status === 429 ? '请求过于频繁，请稍后重试。' : response.status === 502 || response.status === 503 ? '暂时无法连接后端，请检查服务状态。' : response.status === 400 ? '请求未通过校验，请检查输入或筛选条件。' : '加载失败，请稍后重试。');
    error.status = response.status; throw error;
  }
  return response.status === 204 ? null : response.json();
}
function login(message = '') {
  state.controller?.abort();
  root.innerHTML = `<main class="login-page"><div class="login-story"><a class="brand" href="/"><img class="brand-icon" src="/app-icon.png" alt="阿芒" width="34" height="34"><span>阿芒<span class="brand-sub">CONTROL ROOM</span></span></a><div class="login-story-body"><div class="eyebrow">阿芒 · AETHER</div><h1>每一次调用，<br>清晰可见。</h1><p>为用户与 Agent 的工作提供一扇窗口。<br>从整体运行趋势，深入到每一个请求。</p><div class="orbit"><span></span><span></span><span></span><img src="/app-icon.png" alt="" width="130" height="130"></div></div><div class="login-foot">ADMIN CONSOLE <span>调用观测 · 模型配置</span></div></div><div class="login-form-wrap"><form class="login-form"><div class="eyebrow teal">管理员访问</div><h2>欢迎回来</h2><p class="muted">输入管理员密码，进入管理看板。</p><label>管理员密码<input id="password" type="password" autocomplete="current-password" placeholder="输入管理员密码" maxlength="1024" required></label><div class="form-error" role="alert"></div><button class="button primary" type="submit">进入看板 <span>→</span></button><p class="login-note">查看模型调用、请求与响应 JSON，并管理模型配置。</p></form></div></main>`;
  const form = root.querySelector('form');
  const password = root.querySelector('#password');
  const errorBox = root.querySelector('.form-error');
  const submit = root.querySelector('[type=submit]');
  errorBox.textContent = message;
  form.onsubmit = async (event) => {
    event.preventDefault(); submit.disabled = true; errorBox.textContent = '';
    try {
      const verified = await api('/admin/login', { method: 'POST', body: JSON.stringify({ password: password.value }) });
      password.value = '';
      state.token = verified.token; sessionStorage.setItem('aether_admin_token', state.token);
      state.me = await api('/admin/me'); shell(); await refresh();
    } catch (error) {
      errorBox.textContent = error.message;
      password.select();
    } finally { submit.disabled = false; }
  };
  password.focus();
}
function clearAdminData() {
  state.controller?.abort(); state.generation++;
  state.me = null; state.snapshot = null; state.calls = null;
  state.timeline = null; state.timelineOpen.clear(); state.timelineFull.clear();
  state.user = ''; state.agent = ''; state.model = ''; state.outcome = '';
  state.modelConfig = null; state.catalog = null; state.modelDraft = null; state.modelSaving = false;
  state.serviceConfig = null; state.serviceSaving = false;
  state.window = null; state.updated = null; state.agentOptionsUser = null; state.browsingPages = false;
  if (dialog.open) dialog.close();
  document.querySelector('#detail-content').replaceChildren();
}
function accessLost(error) {
  clearAdminData();
  if (error.status === 401) {
    state.token = null; sessionStorage.removeItem('aether_admin_token'); login(error.message); return;
  }
  root.replaceChildren();
  const box = el('main', 'access-denied');
  box.append(el('div', 'eyebrow teal', 'ACCESS RESTRICTED'), el('h1', '', '管理权限已不可用'), el('p', '', error.message), button('重新登录', logout, 'button primary'));
  root.append(box);
}
async function logout() {
  try { await api('/admin/logout', { method: 'POST' }); }
  catch (error) { if (error.status !== 401) { notify('暂时无法退出，请稍后重试。'); return; } }
  clearAdminData(); state.token = null; sessionStorage.removeItem('aether_admin_token'); login();
}
function shell() {
  root.innerHTML = `<div class="layout"><aside class="sidebar"><a class="brand" href="/"><img class="brand-icon" src="/app-icon.png" alt="阿芒" width="34" height="34"><span>阿芒<span class="brand-sub">CONTROL ROOM</span></span></a><div class="nav-label">工作空间</div><nav aria-label="管理导航"></nav><div class="sidebar-note"><span class="live-dot"></span> 管理员工作空间<p>调用观测与模型配置</p></div><div class="account"><div class="avatar"></div><div><strong></strong><span>管理员</span></div><button id="logout" title="退出登录" aria-label="退出登录">↗</button></div></aside><main class="main"><header class="topbar"><div class="breadcrumb">工作空间 <span>/</span> <strong>运行概览</strong></div><div class="topbar-right"><span class="readonly">◉ 管理员</span><span class="timezone"></span></div></header><section class="workspace"><div class="page-heading"><div><div class="eyebrow">OBSERVABILITY</div><h1></h1><p></p></div><div class="refresh-group"><span id="updated">尚未刷新</span><button id="refresh" class="button">↻ 刷新</button></div></div><div class="filters"><label>时间范围<select id="range"><option value="24">最近 24 小时</option><option value="168">最近 7 天</option><option value="720">最近 30 天</option></select></label><label>用户<select id="user-filter"><option value="">全部用户</option></select></label><label>Agent<select id="agent-filter"><option value="">全部 Agents</option></select></label><label>模型<input id="model-filter" list="model-options" placeholder="全部模型"><datalist id="model-options"></datalist></label><label id="outcome-label" hidden>请求结果<select id="outcome-filter"><option value="">全部结果</option><option value="succeeded">成功</option><option value="in_flight">请求中</option><option value="http_error">HTTP 错误</option><option value="transport_error">连接异常</option><option value="invalid_response">响应异常</option><option value="interrupted">中断</option></select></label><button id="reset" class="text-button">重置</button></div><div id="load-error" class="load-error" hidden role="alert"></div><div id="content" aria-live="polite"></div><footer class="footer"><span>按实际请求统计 · 调用详情可查看请求与响应 JSON</span><span>自动刷新 / 15 秒</span></footer></section></main></div>`;
  const nav = root.querySelector('nav');
  const glyphs = { overview: '◫', calls: '↗', timeline: '⋮', models: '⇄', services: '⚙' };
  for (const [key, value] of Object.entries(tabs)) {
    const item = button('', () => selectTab(key), 'nav-item'); item.dataset.tab = key; item.append(el('span', 'nav-icon', glyphs[key]), el('span', '', value[0])); nav.append(item);
  }
  root.querySelector('.account strong').textContent = state.me.name;
  root.querySelector('.avatar').textContent = state.me.name.charAt(0).toUpperCase();
  root.querySelector('.timezone').textContent = Intl.DateTimeFormat().resolvedOptions().timeZone;
  root.querySelector('#logout').onclick = logout;
  root.querySelector('#refresh').onclick = () => refresh();
  root.querySelector('#range').value = state.hours;
  root.querySelector('#range').onchange = (event) => { state.hours = Number(event.target.value); refresh(); };
  root.querySelector('#user-filter').onchange = (event) => { state.user = event.target.value; state.agent = ''; refresh(); };
  root.querySelector('#agent-filter').onchange = (event) => { state.agent = event.target.value; refresh(); };
  root.querySelector('#model-filter').onchange = (event) => { state.model = event.target.value.trim(); refresh(); };
  root.querySelector('#outcome-filter').onchange = (event) => { state.outcome = event.target.value; refresh(); };
  root.querySelector('#reset').onclick = () => { state.user = ''; state.agent = ''; state.model = ''; state.outcome = ''; state.hours = 24; root.querySelector('#range').value = '24'; root.querySelector('#model-filter').value = ''; root.querySelector('#outcome-filter').value = ''; refresh(); };
  updateTitle(); loading();
}
function updateTitle() {
  root.querySelector('.page-heading h1').textContent = tabs[state.tab][0];
  root.querySelector('.page-heading p').textContent = tabs[state.tab][1];
  root.querySelector('.breadcrumb strong').textContent = tabs[state.tab][0];
  for (const item of root.querySelectorAll('.nav-item')) item.classList.toggle('selected', item.dataset.tab === state.tab);
  // 时间线只按用户与 Agent 定位，时间、模型与结果筛选不适用。
  const timelinePage = state.tab === 'timeline';
  for (const selector of ['#range', '#model-filter', '#outcome-filter']) root.querySelector(selector).parentElement.hidden = timelinePage;
  const settingsPage = state.tab === 'models' || state.tab === 'services';
  root.querySelector('.filters').hidden = settingsPage;
  root.querySelector('.footer span:first-child').textContent = settingsPage ? '配置应用于所有用户 · 已发出的请求继续完成' : timelinePage ? '最新在上 · 失败、中断与请求中的调用按开始时间插入' : '按实际请求统计 · 调用详情可查看请求与响应 JSON';
  root.querySelector('.footer span:last-child').textContent = settingsPage ? '配置页面 / 手动刷新' : '自动刷新 / 15 秒';
}
function selectTab(tab) { if (state.tab === tab) return; state.tab = tab; updateTitle(); if (state.snapshot || state.tab === 'models' || state.tab === 'services') render(); refresh(false); }
function loading() { const box = root.querySelector('#content'); box.replaceChildren(el('div', 'loading-card', '正在读取运行数据…')); }
function query(extra = {}) {
  const params = new URLSearchParams({ from: state.window.from, to: state.window.to, limit: '50' });
  if (state.user) params.set('user_id', state.user);
  if (state.agent) params.set('agent', state.agent);
  if (state.model) params.set('model', state.model);
  if (state.outcome) params.set('outcome', state.outcome);
  for (const [key, value] of Object.entries(extra)) if (value != null) params.set(key, value);
  return params.toString();
}
async function refresh(newWindow = true) {
  if (!state.me) return;
  if (state.tab === 'models') return refreshModelSettings();
  if (state.tab === 'services') return refreshServiceSettings();
  state.browsingPages = false;
  state.controller?.abort(); const controller = new AbortController(); state.controller = controller;
  const generation = ++state.generation;
  if (newWindow || !state.window) { const now = new Date(); state.window = { from: new Date(now - state.hours * 3600000).toISOString(), to: now.toISOString() }; }
  const refreshButton = root.querySelector('#refresh'); refreshButton.disabled = true; refreshButton.textContent = '↻ 更新中';
  try {
    const requests = [api(`/admin/snapshot?${query()}`, { signal: controller.signal })];
    if (state.tab === 'calls') requests.push(api(`/admin/calls?${query()}`, { signal: controller.signal }));
    if (state.tab === 'timeline' && state.user) requests.push(api(`/admin/timeline?${new URLSearchParams({ user_id: state.user, agent: state.agent || 'main' })}`, { signal: controller.signal }));
    const [snapshot, list] = await Promise.all(requests);
    if (generation !== state.generation) return;
    state.snapshot = snapshot;
    if (state.tab === 'calls') state.calls = list;
    if (state.tab === 'timeline') state.timeline = list ? { ...list, key: timelineKey() } : null;
    state.updated = snapshot.generated_at;
    root.querySelector('.footer span:last-child').textContent = '自动刷新 / 15 秒';
    root.querySelector('#load-error').hidden = true;
    root.querySelector('#updated').textContent = `${time(state.updated)} 已更新`;
    updateFilters(); render();
  } catch (error) {
    if (error.name === 'AbortError' || generation !== state.generation) return;
    if (error.status === 401 || error.status === 403) { accessLost(error); return; }
    const box = root.querySelector('#load-error'); if (box) { box.hidden = false; box.textContent = `${error.message}${state.snapshot ? ' 当前保留上次成功的数据。' : ''}`; }
    if (!state.snapshot) root.querySelector('#content')?.replaceChildren(empty('暂时没有读取到数据', '恢复连接后点击右上角刷新。'));
  } finally {
    if (generation === state.generation && refreshButton.isConnected) { refreshButton.disabled = false; refreshButton.textContent = '↻ 刷新'; }
  }
}
function updateFilters() {
  const userSelect = root.querySelector('#user-filter');
  const known = new Map([...userSelect.options].filter((option) => option.value).map((option) => [option.value, option.textContent]));
  for (const user of state.snapshot.users.items) known.set(user.id, user.email);
  userSelect.replaceChildren(new Option('全部用户', ''), ...[...known].map(([id, email]) => new Option(email, id))); userSelect.value = state.user;
  const agentSelect = root.querySelector('#agent-filter');
  const agents = state.snapshot.agents.items;
  const knownAgents = state.agentOptionsUser === state.user ? new Map([...agentSelect.options].filter((option) => option.value).map((option) => [option.value, option.textContent])) : new Map();
  for (const agent of agents) knownAgents.set(agent.kind === 'main' ? 'main' : agent.id, agent.kind === 'main' ? 'Main' : `Subagent · ${agent.id.slice(0, 8)}`);
  // 从时间线跳转的 Subagent 可能不在当前分页里，仍要能显示为选中项。
  if (state.agent && !knownAgents.has(state.agent)) knownAgents.set(state.agent, `Subagent · ${state.agent.slice(0, 8)}`);
  state.agentOptionsUser = state.user;
  agentSelect.replaceChildren(new Option('全部 Agents', ''), ...[...knownAgents].map(([id, label]) => new Option(label, id)));
  agentSelect.disabled = !state.user; agentSelect.value = state.agent;
  root.querySelector('#model-options').replaceChildren(...state.snapshot.models.map((model) => new Option(model.model, model.model)));
}
function empty(title, description) { const box = el('div', 'empty'); box.append(el('div', 'empty-icon', '◇'), el('h3', '', title), el('p', '', description)); return box; }
function panel(title, description, child, right) { const box = el('section', 'panel'); const head = el('div', 'panel-head'); const names = el('div'); names.append(el('h2', '', title)); if (description) names.append(el('p', '', description)); head.append(names); if (right) head.append(right); box.append(head, child); return box; }
function card(label, value, note, className = '') { const node = el('article', `metric-card ${className}`); node.append(el('div', 'metric-label', label), el('strong', 'metric-value', value), el('p', 'metric-note', note)); return node; }
function metricCards() {
  const m = state.snapshot.metrics; const row = el('div', 'metrics');
  row.append(card('模型调用尝试', number(m.attempts), `${number(m.succeeded)} 次成功 · ${number(m.in_flight)} 次请求中`), card('已知模型费用', money(m.cost_known_usd), `${number(m.cost_unknown)} 次费用未知 · USD`, 'cost-card'), card('已知 Token', compact(m.input_tokens + m.output_tokens), `输入 ${compact(m.input_tokens)} / 输出 ${compact(m.output_tokens)}`), card('失败 / 中断', `${number(m.failed)} / ${number(m.interrupted)}`, '提供方或响应失败 / 结果未知'), card('平均请求耗时', duration(m.mean_duration_ms), `基于 ${number(m.duration_samples)} 个已知耗时样本`)); return row;
}
function render() {
  const content = root.querySelector('#content'); content.replaceChildren();
  if (state.tab === 'overview') overview(content);
  if (state.tab === 'calls') calls(content);
  if (state.tab === 'timeline') timelinePage(content);
  if (state.tab === 'models') modelSettingsPage(content);
  if (state.tab === 'services') serviceSettingsPage(content);
}
function trendChart() {
  const values = state.snapshot.trend; if (!values.length || !values.some((item) => item.attempts)) return empty('此时间段没有调用', '新调用将自动显示在趋势中。');
  const wrap = el('div', 'trend-chart'); const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg'); svg.setAttribute('viewBox', '0 0 760 210'); svg.setAttribute('role', 'img'); svg.setAttribute('aria-label', '模型请求调用趋势');
  const svgEl = (tag, attrs) => { const element = document.createElementNS(svg.namespaceURI, tag); for (const [key, value] of Object.entries(attrs)) element.setAttribute(key, value); return element; };
  const max = Math.max(...values.map((value) => value.attempts), 1); const width = 700 / Math.max(values.length, 1); const y = (value) => 170 - value / max * 140;
  for (let tick = 0; tick <= 4; tick++) { const height = 170 - tick * 35; svg.append(svgEl('line', { x1: 40, x2: 746, y1: height, y2: height, stroke: 'var(--line)' })); const label = svgEl('text', { x: 28, y: height + 4, 'text-anchor': 'end', fill: 'var(--text-muted)', 'font-size': 11 }); label.textContent = Math.round(max * tick / 4); svg.append(label); }
  values.forEach((item, index) => { const bar = svgEl('rect', { x: 43 + index * width, y: y(item.attempts), width: Math.max(2, width - 7), height: Math.max(0, 170 - y(item.attempts)), rx: 3, fill: 'var(--forest)' }); const tip = svgEl('title', {}); tip.textContent = `${time(item.at)} · ${item.attempts} 次调用`; bar.append(tip); svg.append(bar); if (item.failed) svg.append(svgEl('rect', { x: 43 + index * width, y: y(item.failed), width: Math.max(2, width - 7), height: 170 - y(item.failed), rx: 3, fill: 'var(--error-chart)' })); });
  for (const index of [...new Set([0, Math.floor((values.length - 1) / 2), values.length - 1])]) { const label = svgEl('text', { x: 43 + index * width, y: 200, fill: 'var(--text-muted)', 'font-size': 11 }); label.textContent = new Date(values[index].at).toLocaleString('zh-CN', state.hours <= 48 ? { hour: '2-digit', minute: '2-digit', hour12: false } : { month: '2-digit', day: '2-digit' }); svg.append(label); }
  wrap.append(svg); return wrap;
}
function distribution(items, labelKey, countKey, mode) {
  const list = el('div', 'distribution'); if (!items.length) return empty('暂无记录', '有新调用后将显示分布。'); const max = Math.max(...items.map((item) => item[countKey]), 1);
  for (const item of items.slice(0, 8)) { const row = el('div', 'distribution-row'); const line = el('div', 'distribution-top'); const name = el('span', '', mode === 'model' ? shortModel(item[labelKey]) : item[labelKey]); name.title = item[labelKey]; line.append(name, el('strong', '', number(item[countKey]))); const track = el('div', 'bar-track'); const bar = el('div', 'bar'); bar.style.width = `${item[countKey] / max * 100}%`; track.append(bar); row.append(line, track); list.append(row); } return list;
}
function overview(content) {
  content.append(metricCards());
  const m = state.snapshot.metrics; const coverage = el('div', 'coverage');
  coverage.append(el('span', 'coverage-icon', '◉'), el('span', '', `费用为提供方已知小计，${m.cost_unknown} 次未知；输入 / 输出 Token 分别有 ${m.input_unknown} / ${m.output_unknown} 次未知。`)); content.append(coverage);
  const legend = el('div', 'legend'); legend.append(el('span', 'legend-success', '调用尝试'), el('span', 'legend-error', '失败'));
  content.append(panel('调用趋势', '按请求开始时间统计', trendChart(), legend));
  const charts = el('div', 'two-columns'); charts.append(panel('模型使用分布', '调用次数 · 当前筛选范围', distribution(state.snapshot.models, 'model', 'attempts', 'model')), panel('工具调用分布', '模型提出的工具调用，不等于已执行成功', distribution(state.snapshot.tools, 'name', 'count'))); content.append(charts);
  const recent = state.snapshot.users.items.slice(0, 6);
  const rows = recent.map((user) => {
    const status = el('div', 'cell-stack'); status.append(pill(user.main_active ? 'processing' : 'idle'), el('small', '', `${user.active_subagents} / ${user.total_subagents} Subagents 处理中`));
    const cost = el('div', 'cell-stack'); cost.append(el('strong', '', money(user.cost_known_usd)), el('small', '', `${user.cost_unknown} 次未知`));
    const action = button('查看调用 →', () => { state.user = user.id; state.agent = ''; state.tab = 'calls'; updateTitle(); refresh(); }, 'text-button');
    return row([identity(user.email, user.id), status, `${number(user.attempts)} / ${number(user.failed)}`, compact(user.input_tokens + user.output_tokens), cost, time(user.last_activity_at), action]);
  });
  content.append(panel('用户使用情况', '按用户查看模型调用', rows.length ? table(['用户', 'Agent 状态', '调用 / 失败', '已知 Token', '已知费用', '最近活动', ''], rows) : empty('没有符合条件的用户', '调整筛选条件，或等待用户首次登录。'), button('查看调用 →', () => selectTab('calls'), 'text-button')));
  const note = el('div', 'data-note'); const first = state.snapshot.coverage.first_observed_at; note.textContent = `${first ? `首次观测到调用日志：${time(first)}。` : '尚未观测到调用日志。'} ${number(state.snapshot.coverage.legacy_steps)} 条历史步骤未计入调用统计；崩溃中断的请求可能已产生费用。`; content.append(note);
}
function identity(email, id) { const node = el('div', 'identity'); node.append(el('strong', '', email), el('small', '', id)); return node; }
function table(headers, rows) { const wrap = el('div', 'table-wrap'); const node = el('table'); const thead = el('thead'); const head = el('tr'); headers.forEach((header) => head.append(el('th', '', header))); thead.append(head); const body = el('tbody'); rows.forEach((row) => body.append(row)); node.append(thead, body); wrap.append(node); return wrap; }
function row(cells) { const tr = el('tr'); for (const value of cells) { const td = el('td'); td.append(value instanceof Node ? value : document.createTextNode(String(value))); tr.append(td); } return tr; }
function calls(content) {
  content.append(metricCards()); const list = state.calls;
  if (!list) { content.append(el('div', 'loading-card', '正在读取调用记录…')); return; }
  if (!list.items.length) { content.append(empty('没有符合条件的调用', '新日志不会根据历史步骤回填。可调整时间范围后重试。')); return; }
  const rows = list.items.map((call) => row([time(call.started_at), requestIds(call), identity(call.email, call.agent_id ? `Subagent · ${call.agent_id.slice(0, 8)}` : 'Main'), el('span', 'model-name', shortModel(call.requested_model)), call.purpose === 'compact' ? '压缩' : 'Agent', pill(call.outcome), `${call.input_tokens == null ? '未知' : number(call.input_tokens)} / ${call.output_tokens == null ? '未知' : number(call.output_tokens)}`, money(call.cost), duration(call.duration_ms), button('详情 →', () => callDetails(call), 'text-button')]));
  content.append(panel('请求日志', '每次重试单独计数 · 中断代表结果未知', table(['开始时间', '请求 ID', '用户 / Agent', '模型', '用途', '结果', '输入 / 输出 Token', '费用', '耗时', ''], rows)));
  if (list.next_cursor) content.append(button('加载更多调用', (event) => loadMoreCalls(event.currentTarget), 'button load-more'));
}
async function loadMoreCalls(target) {
  const list = state.calls;
  const generation = state.generation;
  const controller = state.controller;
  if (!list.next_cursor || controller.signal.aborted) return;
  target.disabled = true;
  try {
    const response = await api(`/admin/calls?${query({ cursor: list.next_cursor })}`, { signal: controller.signal });
    if (generation !== state.generation || controller.signal.aborted || !state.me) return;
    list.items.push(...response.items); list.next_cursor = response.next_cursor;
    state.browsingPages = true;
    root.querySelector('.footer span:last-child').textContent = '分页浏览中 / 手动刷新';
    render();
  } catch (error) {
    if (error.name === 'AbortError' || generation !== state.generation) return;
    if (error.status === 401 || error.status === 403) { accessLost(error); return; }
    notify(error.message); target.disabled = false;
  }
}
// timelineKey 标识当前时间线属于哪个用户和 Agent，切换后不再显示上一个 Agent 的数据。
function timelineKey() { return `${state.user}|${state.agent || 'main'}`; }
function agentLabel(agent) { return agent === 'main' ? 'Main' : `Subagent · ${agent.slice(0, 8)}`; }
function openAgentTimeline(agent) { state.agent = agent === 'main' ? '' : agent; refresh(); }
// timelinePage 按时间降序渲染单个 Agent 的时间线：最新在上，工具结果并入对应的工具调用卡片，调用不在本页的结果单独显示。
function timelinePage(content) {
  if (!state.user) { content.append(empty('选择一个用户', '在上方选择用户与 Agent，查看该 Agent 的完整运行过程。')); return; }
  const timeline = state.timeline;
  if (!timeline || timeline.key !== timelineKey()) { content.append(el('div', 'loading-card', '正在读取运行过程…')); return; }
  const agent = state.agent || 'main';
  const summary = state.snapshot.agents.items.find((item) => (item.kind === 'main' ? 'main' : item.id) === agent);
  const email = state.snapshot.users.items.find((user) => user.id === state.user)?.email ?? state.user;
  const list = el('ol', 'timeline');
  if (timeline.system_prompt) {
    const node = timelineNode('runtime', '系统提示词', timeline.system_prompt.from_call_at, [el('span', 'timeline-meta', '来自最近一次请求，每次请求附带的当前时间会不同')]);
    node.append(foldable('system-prompt', '展开查看', timeline.system_prompt.text)); list.append(node);
  }
  const results = new Map(timeline.items.filter((item) => item.role === 'tool').map((item) => [item.tool_call_id, item]));
  const paired = new Set(timeline.items.flatMap((item) => (item.tool_calls ?? []).map((call) => call.id)).filter((id) => results.has(id)));
  for (const item of [...timeline.items].reverse()) {
    if (item.type === 'call') list.append(failedCallNode(item, email));
    else if (item.role === 'assistant') list.append(assistantNode(item, results, email));
    else if (item.role === 'tool') { if (!paired.has(item.tool_call_id)) list.append(toolResultNode(item)); }
    else list.append(inputNode(item));
  }
  if (!timeline.items.length) list.append(el('li', 'timeline-empty', '这个 Agent 还没有已读取的消息。'));
  if (timeline.next_cursor) { const item = el('li', 'timeline-more'); item.append(button('加载更早的记录', (event) => loadEarlierTimeline(event.currentTarget), 'button')); list.append(item); }
  content.append(panel(`${agentLabel(agent)} · ${email}`, '最新在上，按消息顺序倒序排列；失败、中断与请求中的调用按开始时间插入。待读消息不显示。', list, summary ? pill(summary.status) : null));
}
// timelineNode 是时间线上一个节点的外框：左侧圆点表示类型，标题行显示时间与附加信息。
function timelineNode(kind, title, at, extras = []) {
  const node = el('li', `timeline-node ${kind}`); const head = el('div', 'timeline-head');
  head.append(el('span', 'timeline-title', title), el('time', 'timeline-time', time(at)), ...extras.filter(Boolean));
  node.append(head); return node;
}
// foldable 把长文本放进可折叠区域；展开状态按 key 记住，自动刷新重绘后保持不变。短文本默认展开。
function foldable(key, label, text, className = '') {
  const box = el('details', `timeline-fold ${className}`);
  box.open = state.timelineOpen.get(key) ?? text.length <= 280;
  const summary = el('summary'); summary.append(el('span', 'fold-label', label), el('span', 'fold-preview', text.replace(/\s+/g, ' ').slice(0, 120)));
  box.append(summary, el('pre', 'timeline-text', text));
  box.ontoggle = () => state.timelineOpen.set(key, box.open);
  return box;
}
// fullView 优先使用已加载的全文；仍被截断时返回加载全文的按钮。
function fullView(item) { return state.timelineFull.get(item.id) ?? item; }
function loadFullButton(item) {
  if (!item.truncated || state.timelineFull.has(item.id)) return null;
  return button(item.image_count ? '加载全文与截图' : '加载全文', (event) => loadTimelineMessage(item.id, event.currentTarget), 'text-button timeline-load');
}
function prettyArguments(text) { try { return JSON.stringify(JSON.parse(text), null, 2); } catch { return text; } }
function callMeta(call) { return `${shortModel(call.response_model || call.requested_model)} · ${call.input_tokens == null ? '未知' : number(call.input_tokens)} / ${call.output_tokens == null ? '未知' : number(call.output_tokens)} Token · ${money(call.cost)} · ${duration(call.duration_ms)}`; }
function inputNode(item) {
  const view = fullView(item);
  const senders = { user: ['input', '用户输入'], main: ['input', '来自 Main'], subagent: ['input', `来自 ${agentLabel(item.sender_subagent_id ?? '')}`], system: ['runtime', '系统通知'] };
  const [kind, title] = item.is_summary ? ['summary', '上下文压缩摘要'] : item.name === 'sandbox_observation' ? ['runtime', '截图观察'] : senders[item.sender] ?? ['runtime', '运行时提示'];
  const jump = item.sender === 'subagent' && item.sender_subagent_id ? button('打开该 Subagent →', () => openAgentTimeline(item.sender_subagent_id), 'text-button') : item.sender === 'main' && state.agent ? button('回到 Main →', () => openAgentTimeline('main'), 'text-button') : null;
  const node = timelineNode(kind, title, item.at, [jump]);
  if (item.is_summary) node.append(foldable(`${item.id}:text`, '摘要内容', view.text));
  else if (view.text) node.append(el('div', 'timeline-bubble', view.text));
  if (view.images?.length) { const images = el('div', 'timeline-images'); for (const url of view.images) { const image = el('img'); image.src = url; image.alt = '截图'; images.append(image); } node.append(images); }
  else if (item.image_count) node.append(el('p', 'timeline-muted', `包含 ${item.image_count} 张截图`));
  const more = loadFullButton(item); if (more) node.append(more);
  return node;
}
function assistantNode(item, results, email) {
  const view = fullView(item); const call = item.call;
  const node = timelineNode('assistant', '模型回复', item.at, [el('span', 'timeline-meta', call ? callMeta(call) : '未关联请求日志'), call ? button('请求详情 →', () => callDetails({ ...call, email }), 'text-button') : null]);
  if (call) node.append(requestIds(call));
  const reasoning = view.reasoning;
  if (reasoning.state === 'text') node.append(foldable(`${item.id}:reasoning`, '推理过程', reasoning.text, 'reasoning'));
  else node.append(el('p', 'timeline-muted', reasoning.state === 'encrypted' ? '推理已加密，供应商未返回原文' : '模型未返回推理'));
  // Main 的正文生成结束后直接发给用户；Subagent 的正文只是内部记录，它通过 send_message 向 Main 汇报。
  if (view.text) node.append(foldable(`${item.id}:text`, state.agent ? '正文（不直接发给用户）' : '正文（直接发给用户）', view.text));
  for (const toolCall of view.tool_calls ?? []) node.append(toolCard(toolCall, results.get(toolCall.id)));
  const more = loadFullButton(item); if (more) node.append(more);
  return node;
}
// toolCard 把一次工具调用与它的结果放在一起；assign_subagents 的结果可直接跳到对应 Subagent。
function toolCard(toolCall, result) {
  const card = el('div', 'tool-card'); const head = el('div', 'tool-card-head');
  head.append(el('strong', '', toolCall.name), result ? el('span', 'timeline-meta', time(result.at)) : el('span', 'timeline-meta pending', '尚未返回结果'));
  card.append(head, foldable(`${toolCall.id}:args`, '参数', prettyArguments(toolCall.arguments)));
  if (!result) return card;
  const view = fullView(result);
  card.append(foldable(`${toolCall.id}:result`, '结果', view.text));
  const more = loadFullButton(result); if (more) card.append(more);
  if (toolCall.name === 'assign_subagents') {
    let ids = []; try { ids = JSON.parse(view.text).subagent_ids ?? []; } catch {}
    if (ids.length) { const links = el('div', 'tool-links'); for (const id of ids) links.append(button(`${agentLabel(id)} →`, () => openAgentTimeline(id), 'text-button')); card.append(links); }
  }
  return card;
}
function toolResultNode(item) {
  const view = fullView(item);
  const node = timelineNode('tool', '工具结果', item.at, [el('span', 'timeline-meta', '对应的调用在更早的记录中')]);
  node.append(foldable(`${item.id}:text`, '结果', view.text));
  const more = loadFullButton(item); if (more) node.append(more);
  return node;
}
function failedCallNode(item, email) {
  const call = item.call;
  const node = timelineNode(`call ${call.outcome}`, call.purpose === 'compact' ? '上下文压缩请求' : '模型请求', item.at, [pill(call.outcome), button('请求详情 →', () => callDetails({ ...call, email }), 'text-button')]);
  node.append(requestIds(call), el('p', 'timeline-muted', `${shortModel(call.requested_model)} · HTTP ${call.http_status ?? '—'} · ${call.failure_kind || '进行中'} · ${duration(call.duration_ms)}`));
  return node;
}
async function loadTimelineMessage(id, target) {
  const generation = state.generation; target.disabled = true;
  try {
    const full = await api(`/admin/timeline/messages/${id}`, { signal: state.controller.signal });
    if (generation !== state.generation || !state.me) return;
    state.timelineFull.set(id, full); render();
  } catch (error) {
    if (error.name === 'AbortError' || generation !== state.generation) return;
    if (error.status === 401 || error.status === 403) { accessLost(error); return; }
    notify(error.message); target.disabled = false;
  }
}
// loadEarlierTimeline 向前翻页并暂停自动刷新，避免刷新把已加载的更早记录替换掉。
async function loadEarlierTimeline(target) {
  const timeline = state.timeline; const generation = state.generation; const controller = state.controller;
  target.disabled = true;
  try {
    const page = await api(`/admin/timeline?${new URLSearchParams({ user_id: state.user, agent: state.agent || 'main', before: timeline.next_cursor })}`, { signal: controller.signal });
    if (generation !== state.generation || controller.signal.aborted || !state.me) return;
    timeline.items.unshift(...page.items); timeline.next_cursor = page.next_cursor;
    state.browsingPages = true;
    root.querySelector('.footer span:last-child').textContent = '分页浏览中 / 手动刷新';
    render();
  } catch (error) {
    if (error.name === 'AbortError' || generation !== state.generation) return;
    if (error.status === 401 || error.status === 403) { accessLost(error); return; }
    notify(error.message); target.disabled = false;
  }
}
// copyChip 把一条 ID 显示在按钮外面，点击「复制」写入完整值，不必从长 JSON 里挑选。
function copyChip(label, value) {
  const node = el('span', 'copy-id');
  node.append(el('span', 'copy-id-label', label), el('code', 'copy-id-value', value));
  const action = button('复制', () => {
    const copySelection = () => {
      const area = document.createElement('textarea');
      area.value = value; area.setAttribute('readonly', ''); area.style.position = 'fixed'; area.style.left = '-9999px';
      document.body.append(area); area.select();
      const copied = document.execCommand('copy');
      area.remove();
      return copied;
    };
    const finish = (copied) => {
      if (!copied) { notify('无法写入剪贴板，请手动选择 ID'); return; }
      action.textContent = '已复制';
      setTimeout(() => { if (action.isConnected) action.textContent = '复制'; }, 1200);
    };
    // 选中复制必须发生在点击当次，不能等 Clipboard API 异步失败后再做。页面没有焦点时直接走这条。
    if (!navigator.clipboard?.writeText || !document.hasFocus()) { finish(copySelection()); return; }
    navigator.clipboard.writeText(value).then(() => finish(true), () => finish(copySelection()));
  }, 'copy-id-button');
  action.setAttribute('aria-label', `复制${label}`);
  node.append(action);
  return node;
}
// requestIds 把这次模型请求在本系统中的调用 ID 放到列表、时间线与详情顶部，供直接复制。
function requestIds(call) {
  const box = el('div', 'request-ids');
  box.append(copyChip('调用 ID', call.id));
  return box;
}
function detailFrame(title, subtitle) {
  const content = document.querySelector('#detail-content'); content.replaceChildren(); const header = el('div', 'detail-header'); const names = el('div'); names.append(el('div', 'eyebrow teal', 'MODEL REQUEST'), el('h2', '', title), el('p', '', subtitle)); header.append(names, button('×', () => dialog.close(), 'close-button')); content.append(header); if (!dialog.open) dialog.showModal(); return content;
}
function detailsGrid(items) { const grid = el('dl', 'details-grid'); for (const [label, value] of items) { const group = el('div'); group.append(el('dt', '', label), value instanceof Node ? value : el('dd', '', value == null ? '未知' : String(value))); grid.append(group); } return grid; }
async function callDetails(call) {
  const content = detailFrame('模型调用详情', '调用元数据与实际请求、响应 JSON');
  content.append(requestIds(call));
  content.append(detailsGrid([['用途', call.purpose === 'compact' ? '上下文压缩' : 'Agent 推理'], ['压缩原因', ({user_message:'用户新消息', idle:'回答后超时', tokens:'上下文阈值', capacity:'模型容量'})[call.compact_reason] || '—'], ['压缩前 Token（上次官方用量）', call.context_tokens_before == null ? '—' : number(call.context_tokens_before)], ['摘要覆盖序号', call.summary_through_sequence ?? '—'], ['用户', call.email], ['Agent', call.agent_id || 'Main'], ['请求模型', call.requested_model], ['响应模型', call.response_model], ['结果', pill(call.outcome)], ['HTTP 状态', call.http_status], ['失败分类', call.failure_kind || '—'], ['开始时间', time(call.started_at)], ['结束时间', time(call.finished_at)], ['请求耗时', duration(call.duration_ms)], ['输入 / 输出 Token', `${call.input_tokens == null ? '未知' : number(call.input_tokens)} / ${call.output_tokens == null ? '未知' : number(call.output_tokens)}`], ['提供方费用 USD', money(call.cost)], ['模型提出的工具', call.tool_names?.join('、') || '无'], ['逻辑步骤 ID', call.step_id]]));
  const note = el('div', 'data-note', '请求中断或连接失败时，提供方可能已执行并计费。未知费用不等于 0。'); content.append(note);
  const payloadPanel = el('section', 'request-json');
  payloadPanel.append(el('h3', '', '请求与响应 JSON'), el('p', 'muted', '按本次实际调用保存，包含消息上下文与工具定义；不含 HTTP 认证头。'));
  const payloadContent = el('div', 'request-json-content', '正在读取请求与响应…');
  payloadPanel.append(payloadContent); content.append(payloadPanel);
  const generation = state.generation;
  try {
    const detail = await api(`/admin/calls/${call.id}`, { signal: state.controller.signal });
    if (generation !== state.generation || !payloadPanel.isConnected || !dialog.open || !state.me) return;
    payloadContent.replaceChildren();
    for (const [key, label] of [['request', '请求 JSON'], ['response', '响应 JSON']]) {
      const section = el('section', 'payload-section');
      section.append(el('h4', '', label));
      payloadContent.append(section);
      if (key === 'response' && detail.response_truncated) section.append(el('p', 'data-note', '响应超过 8 MiB 接收上限，以下仅为已接收的部分正文。'));
      const rawText = key === 'response' ? detail.response_text : null;
      if (detail[key] == null && rawText == null) {
        section.append(el('p', 'data-note', key === 'request' ? '该日志产生时尚未记录请求 JSON，无法还原当时的实际请求。新调用会自动保存。' : '该调用未记录响应正文，或尚未收到响应。新调用收到正文后会自动保存。'));
        continue;
      }
      const jsonText = rawText ?? JSON.stringify(detail[key], null, 2);
      if (rawText != null) section.append(el('p', 'data-note', '提供方返回的正文不是有效 JSON，以下展示原始文本。'));
      const code = el('pre', 'json-code'); code.tabIndex = 0; code.setAttribute('aria-label', label); code.textContent = jsonText;
      section.append(code);
    }
  } catch (error) {
    if (error.name === 'AbortError' || generation !== state.generation || !payloadPanel.isConnected || !dialog.open) return;
    if (error.status === 401 || error.status === 403) { accessLost(error); return; }
    payloadContent.replaceChildren(el('p', 'form-error', error.message), button('重新加载', () => callDetails(call)));
  }
}
dialog.addEventListener('click', (event) => { if (event.target === dialog) dialog.close(); });
document.addEventListener('visibilitychange', () => { if (!document.hidden && state.me && state.tab !== 'models' && !state.browsingPages) refresh(); });
setInterval(() => { if (!document.hidden && state.me && state.tab !== 'models' && state.tab !== 'services' && !state.browsingPages && !dialog.open && !root.querySelector('input:focus')) refresh(); }, 15_000);
if (state.token) {
  root.append(el('div', 'boot', '正在验证管理员身份…'));
  try { state.me = await api('/admin/me'); shell(); await refresh(); }
  catch (error) { state.token = null; sessionStorage.removeItem('aether_admin_token'); login(error.message); }
} else login();

async function refreshModelSettings() {
  state.controller?.abort();
  const controller = new AbortController(); state.controller = controller;
  const generation = ++state.generation;
  const target = root.querySelector('#refresh'); target.disabled = true;
  try {
    const [settings, catalog] = await Promise.all([
      api('/admin/model-settings', { signal: controller.signal }),
      api('/admin/models', { signal: controller.signal }),
    ]);
    if (generation !== state.generation || !state.me) return;
    state.modelConfig = settings; state.catalog = catalog;
    state.modelDraft = { main_model: settings.main_model, subagent_model: settings.subagent_model, compact_at_tokens: settings.compact_at_tokens };
    root.querySelector('#updated').textContent = `${time(catalog.fetched_at)} 已更新目录`;
    root.querySelector('#load-error').hidden = true;
    render();
  } catch (error) {
    if (error.name === 'AbortError' || generation !== state.generation) return;
    if (error.status === 401 || error.status === 403) { accessLost(error); return; }
    const box = root.querySelector('#load-error'); box.hidden = false;
    box.textContent = '模型配置或目录暂时无法读取，请点击刷新重试。当前生效配置保持不变。';
    if (!state.modelConfig) root.querySelector('#content').replaceChildren(empty('暂时无法读取模型配置', '恢复连接后可重新加载。'));
  } finally { if (generation === state.generation && target.isConnected) target.disabled = false; }
}

function modelSettingsPage(content) {
  if (!state.modelConfig) { content.append(el('div', 'loading-card', '正在读取当前配置与可用模型…')); return; }
  const settings = state.modelConfig;
  const intro = el('section', 'model-intro');
  intro.append(el('div', 'eyebrow teal', 'MODEL ROUTING'), el('h2', '', '选择 Main 与 Subagent 的运行模型'), el('p', '', '应用于所有用户。配置保存后立即生效，服务重启后继续保留；正在执行的请求会正常完成。'));
  if (settings.main_output_limit) intro.append(el('p', '', `当前请求输出上限：Main ${number(settings.main_output_limit)} / Subagent ${number(settings.subagent_output_limit)} Token（包含推理 Token）。`));
  const meta = el('div', 'model-config-meta');
  meta.append(pill(settings.source === 'database' ? '已保存' : '部署默认'), el('span', '', settings.updated_at ? `最近修改 ${time(settings.updated_at)} · ${settings.updated_by}` : '当前使用部署时配置的默认模型'));
  intro.append(meta); content.append(intro);
  for (const warning of state.catalog.warnings ?? []) content.append(el('p', 'model-catalog-note', warning));
  const form = el('form', 'model-config-form');
  const cards = el('div', 'model-role-grid');
  for (const role of ['main', 'subagent']) {
    const isMain = role === 'main'; const key = `${role}_model`;
    const box = el('section', 'panel model-role-card');
    const heading = el('div', 'model-role-heading');
    heading.append(el('span', 'model-role-icon', isMain ? '◈' : '◇'), el('h3', '', isMain ? 'Main Agent' : 'Subagent'));
    box.append(heading, el('p', 'muted', isMain ? '负责每位用户的主对话、执行与任务派发。' : '负责 Main 派发的任务，可独立选型或跟随 Main。'));
    const active = el('div', 'model-active');
    active.append(el('span', '', '当前生效'), el('strong', '', isMain ? settings.main_model : settings.effective_subagent_model));
    if (!isMain && settings.subagent_model == null) active.append(el('small', '', '跟随 Main'));
    box.append(active);
    const label = el('label', 'model-picker', isMain ? '选择 Main 模型' : '选择 Subagent 模型');
    const input = el('input'); input.id = `${role}-model-choice`; input.setAttribute('list', `${role}-model-list`); input.autocomplete = 'off'; input.placeholder = '输入提供方或模型名称搜索';
    input.value = state.modelDraft[key] ?? '';
    input.setAttribute('aria-label', isMain ? '选择 Main 模型' : '选择 Subagent 模型');
    const options = el('datalist'); options.id = `${role}-model-list`;
    for (const model of state.catalog.items) { const option = new Option(model.name, model.id); options.append(option); }
    label.append(input, options); box.append(label);
    if (!isMain) {
      const followLabel = el('label', 'model-follow'); const follow = el('input'); follow.type = 'checkbox'; follow.checked = state.modelDraft.subagent_model == null;
      followLabel.append(follow, el('span', '', '跟随 Main 模型')); box.append(followLabel);
      input.disabled = follow.checked;
      follow.onchange = () => { input.disabled = follow.checked; if (!follow.checked && !input.value) input.value = settings.effective_subagent_model; state.modelDraft.subagent_model = follow.checked ? null : input.value; updatePreview(); };
    }
    const preview = el('div', 'model-price');
    function updatePreview() {
      if (isMain || !input.disabled) state.modelDraft[key] = input.value.trim();
      const selected = state.catalog.items.find((model) => model.id === (input.disabled ? state.modelDraft.main_model : state.modelDraft[key]));
      preview.replaceChildren();
      if (input.disabled) preview.append(el('span', '', 'Main 切换时自动同步，无需再次设置。'));
      else if (selected) {
        const perMillion = (value) => value !== '' && Number.isFinite(Number(value)) ? `$${Number(value) * 1000000}` : '未知';
        const capacity = selected.context_length > 0 ? `${number(selected.context_length)} ${selected.provider === 'ikun Gemini' ? '输入上限' : '上下文'} Token` : '模型容量未知';
        preview.append(el('span', '', `${selected.provider} · ${capacity}`), el('span', '', `输入 ${perMillion(selected.pricing.prompt)} / 输出 ${perMillion(selected.pricing.completion)} · USD / 百万 Token`));
      } else preview.append(el('span', '', '请从可用模型目录中选择一个完整模型 ID。'));
      updateModelSaveButton();
    }
    input.oninput = updatePreview;
    box.append(preview); cards.append(box); updatePreview();
  }
  form.append(cards);
  const compaction = el('section', 'panel model-role-card compact-settings');
  compaction.append(el('h3', '', '自动压缩上下文'), el('p', 'muted', '上下文达到阈值后，在下一次调用模型前压缩；恰好是用户新消息触发时，只压它之前的历史并保留这条消息原文。空闲时不产生请求，完整聊天历史保留。'));
  const fields = el('div', 'model-role-grid');
  for (const [key, title, minimum, maximum] of [['compact_at_tokens', '上下文阈值（Token）', 1000, 2000000]]) {
    const label = el('label', 'model-picker', title);
    const input = el('input'); input.type = 'number'; input.id = key; input.min = minimum; input.max = maximum; input.step = 1; input.required = true; input.value = state.modelDraft[key];
    input.oninput = () => { state.modelDraft[key] = Number(input.value); updateModelSaveButton(); };
    label.append(input); fields.append(label);
  }
  compaction.append(fields, el('p', 'muted', 'Token 取上一次同模型成功请求的官方输入用量，按各 Agent 的阈值和已知输入容量触发压缩，不再看距上次回答的时间。'));
  form.append(compaction);
  const actions = el('div', 'model-save-bar');
  const note = el('div'); note.append(el('strong', '', '下一次模型请求使用新配置'), el('p', 'muted', '目录价格供估算，实际费用以请求日志为准。'));
  const save = button('应用模型配置', () => {} , 'button primary'); save.type = 'submit'; save.id = 'save-model-settings';
  actions.append(note, save); form.append(actions);
  const error = el('div', 'form-error'); error.id = 'model-save-error'; error.setAttribute('role', 'alert'); form.append(error);
  form.onsubmit = (event) => { event.preventDefault(); saveModelSettings(); };
  content.append(form);
  content.append(el('p', 'model-catalog-note', `来自已配置的 OpenRouter 与 ikun Gemini 实时目录，共 ${number(state.catalog.items.length)} 个模型候选。Gemini 目录不提供工具能力证明及渠道价格，实际可用性以请求结果为准，未返回的费用显示未知。`));
  updateModelSaveButton();
}
function updateModelSaveButton() {
  const target = root.querySelector('#save-model-settings'); if (!target) return;
  const changed = state.modelDraft.main_model !== state.modelConfig.main_model || state.modelDraft.subagent_model !== state.modelConfig.subagent_model || state.modelDraft.compact_at_tokens !== state.modelConfig.compact_at_tokens;
  target.disabled = !!state.modelSaving || !changed;
  target.textContent = state.modelSaving ? '正在应用…' : changed ? '应用模型配置 →' : '配置已生效';
}
async function saveModelSettings() {
  if (state.modelSaving) return;
  const draft = { ...state.modelDraft, version: state.modelConfig.version };
  const errorBox = root.querySelector('#model-save-error'); errorBox.textContent = '';
  if ([draft.main_model, draft.subagent_model].some((id) => id != null && !state.catalog.items.some((model) => model.id === id))) {
    errorBox.textContent = '请选择目录中的完整模型 ID，或刷新目录后重试。'; return;
  }
  if (!Number.isInteger(draft.compact_at_tokens) || draft.compact_at_tokens < 1000 || draft.compact_at_tokens > 2000000) {
    errorBox.textContent = '压缩阈值需为 1000–2000000 Token 的整数。'; return;
  }
  const generation = state.generation;
  state.modelSaving = true; updateModelSaveButton();
  for (const input of root.querySelectorAll('.model-config-form input')) input.disabled = true;
  try {
    const saved = await api('/admin/model-settings', { method: 'PUT', body: JSON.stringify(draft), signal: state.controller.signal });
    if (generation !== state.generation || !state.me) return;
    state.modelConfig = saved; state.modelDraft = { main_model: saved.main_model, subagent_model: saved.subagent_model, compact_at_tokens: saved.compact_at_tokens };
    render(); notify('模型配置已应用，下一次请求开始生效。');
  } catch (error) {
    if (error.name === 'AbortError' || generation !== state.generation) return;
    if (error.status === 401 || error.status === 403) { accessLost(error); return; }
    errorBox.textContent = error.status === 409 ? '其他管理员已修改配置，请刷新后重新选择。' : error.status === 400 ? '配置无效，或模型已不可用，请检查压缩阈值与模型选择。' : '未能确认保存结果，请刷新查看当前生效配置后重试。';
  } finally { state.modelSaving = false; if (state.me && state.tab === 'models') {
    for (const input of root.querySelectorAll('.model-config-form input')) input.disabled = input.id === 'subagent-model-choice' && state.modelDraft.subagent_model == null;
    updateModelSaveButton();
  } }
}

// refreshServiceSettings 只读取脱敏状态；页面不自动刷新，避免覆盖正在输入的新 Key。
async function refreshServiceSettings() {
  state.controller?.abort();
  const controller = new AbortController(); state.controller = controller;
  const generation = ++state.generation;
  const refreshButton = root.querySelector('#refresh'); refreshButton.disabled = true;
  try {
    const config = await api('/admin/services', { signal: controller.signal });
    if (generation !== state.generation || !state.me) return;
    state.serviceConfig = config;
    root.querySelector('#load-error').hidden = true;
    root.querySelector('#updated').textContent = `${time(new Date().toISOString())} 已更新`;
    render();
  } catch (error) {
    if (error.name === 'AbortError' || generation !== state.generation) return;
    if (error.status === 401 || error.status === 403) { accessLost(error); return; }
    const box = root.querySelector('#load-error'); box.hidden = false;
    box.textContent = '暂时无法读取服务配置，请刷新重试。';
    if (!state.serviceConfig) root.querySelector('#content').replaceChildren(empty('无法读取服务配置', '连接恢复后可重新加载。'));
  } finally { if (generation === state.generation && refreshButton.isConnected) refreshButton.disabled = false; }
}

// serviceSettingsPage 沿用看板现有卡片，每个服务独立保存，只输入待替换的新 Key。
function serviceSettingsPage(content) {
  if (!state.serviceConfig) { content.append(el('div', 'loading-card', '正在读取服务配置…')); return; }
  const intro = el('section', 'model-intro');
  intro.append(el('div', 'eyebrow teal', 'SERVICE SETTINGS'), el('h2', '', '统一管理服务凭证'), el('p', '', '这些配置供所有用户共用。保存后从下一次请求生效；已保存的 Key 不回显完整内容，留空表示保留当前 Key。'));
  content.append(intro);
  const descriptions = { openrouter: '模型目录与 OpenRouter 模型请求。', ikun_gemini: '通过 ikun 使用 Gemini 原生模型接口。', resend: '发送正式环境的登录验证码邮件。', serper: 'Google 搜索。', parallel: '普通网页与 Reddit 正文读取。', everyinfra: '微信公众号与小红书搜索、正文读取，以及 X（Twitter）推文搜索。' };
  const grid = el('div', 'model-role-grid service-settings-grid');
  for (const service of state.serviceConfig.items) {
    const form = el('form', 'panel model-role-card service-card'); form.dataset.service = service.id;
    const heading = el('div', 'service-heading');
    const labels = { saved: '已保存 · 未验证', not_configured: '未配置', unreadable: '无法解密' };
    heading.append(el('h3', '', service.name), el('span', `service-status ${service.status}`, labels[service.status]));
    form.append(heading, el('p', 'muted', descriptions[service.id]));
    const existing = service.status === 'not_configured' ? '当前没有 API Key' : service.key_suffix ? `当前 Key · •••• ${service.key_suffix}` : '当前 Key 已隐藏';
    form.append(el('p', 'service-current', existing), el('p', 'service-updated', service.updated_at ? `最近修改 ${time(service.updated_at)}` : '尚未保存'));
    if (service.status === 'unreadable') form.append(el('p', 'form-error', '无法读取当前凭证。请核对部署加密主密钥，或重新输入 Key 替换。'));
    const label = el('label', 'model-picker', '新的 API Key');
    const key = el('input'); key.type = 'password'; key.autocomplete = 'new-password'; key.maxLength = 8192; key.spellcheck = false; key.placeholder = '输入新 Key；留空保留当前值'; key.setAttribute('aria-label', `${service.name} 新的 API Key`);
    label.append(key); form.append(label);
    let emailFrom;
    if (service.id === 'resend') {
      const field = el('label', 'model-picker', '发件人'); emailFrom = el('input'); emailFrom.value = service.email_from; emailFrom.maxLength = 320; emailFrom.placeholder = 'Aether <noreply@已验证域名>';
      field.append(emailFrom); form.append(field);
    }
    const errorBox = el('div', 'form-error'); errorBox.setAttribute('role', 'alert');
    const actions = el('div', 'service-actions');
    const save = button('保存配置', () => {}, 'button primary'); save.type = 'submit';
    const clear = button('清除 Key', () => submit(true), 'button'); clear.disabled = service.status === 'not_configured'; clear.dataset.clearKey = service.id;
    actions.append(save, clear); form.append(errorBox, actions);
    form.onsubmit = (event) => { event.preventDefault(); submit(false); };
    // 新 Key 只存在输入框和本次请求中，成功或失败后都清空，不写入全局状态或浏览器存储。
    async function submit(clearKey) {
      if (state.serviceSaving) return;
      errorBox.textContent = '';
      if (!clearKey && service.status !== 'saved' && !key.value) { errorBox.textContent = '请输入新的 API Key。'; return; }
      const body = clearKey ? { api_key: '' } : { api_key: key.value || null, ...(emailFrom ? { email_from: emailFrom.value } : {}) };
      const generation = state.generation;
      state.serviceSaving = true;
      const controls = [...grid.querySelectorAll('input, button')].map((control) => ({ control, disabled: control.disabled }));
      for (const { control } of controls) control.disabled = true;
      refreshControl(true);
      try {
        await api(`/admin/services/${service.id}`, { method: 'PUT', body: JSON.stringify(body), signal: state.controller.signal });
        const config = await api('/admin/services', { signal: state.controller.signal });
        if (generation !== state.generation || !state.me) return;
        state.serviceConfig = config; render();
        notify(clearKey ? `${service.name} Key 已清除，后续请求将停止使用此服务。` : `${service.name} 配置已保存，下次请求生效；尚未验证供应商可用性。`);
      } catch (error) {
        if (error.name === 'AbortError' || generation !== state.generation) return;
        if (error.status === 401 || error.status === 403) { accessLost(error); return; }
        errorBox.textContent = error.status === 400 ? '配置未保存。请检查 Key 是否包含空白、发件人格式是否正确。' : '未能确认保存结果，请刷新查看当前配置后重试。';
      } finally {
        key.value = ''; state.serviceSaving = false;
        if (generation === state.generation && state.me && state.tab === 'services') {
          for (const { control, disabled } of controls) control.disabled = disabled;
          refreshControl(false);
        }
      }
    }
    // 保存期间禁用手动刷新，避免同一页的提交与刷新互相覆盖。
    function refreshControl(disabled) { root.querySelector('#refresh').disabled = disabled; }
    grid.append(form);
  }
  content.append(grid, el('p', 'model-catalog-note', '“已保存”仅表示凭证已保存，不代表权限、额度或服务可用性已经验证。'));
}
