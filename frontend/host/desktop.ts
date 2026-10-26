import RFB from '@novnc/novnc';
import { z } from 'zod';

const statusSchema = z.object({ state: z.string(), observed_at: z.iso.datetime() });
const readySchema = z.object({ type: z.literal('ready') });
const descriptions: Record<string, [string, string]> = {
  disabled: ['桌面尚未启用', '当前服务没有配置沙盒。'],
  unallocated: ['还没有沙盒桌面', '阿芒首次使用电脑工具时，会为你创建独立环境。'],
  pending: ['正在准备沙盒', '等待阿芒完成环境准备后，画面会自动连接。'],
  creating: ['正在创建沙盒', '环境准备完成后，画面会自动连接。'],
  starting: ['沙盒正在启动', '启动完成后，画面会自动连接。'],
  resuming: ['沙盒正在恢复', '恢复完成后，画面会自动连接。'],
  paused: ['沙盒已暂停', '阿芒下次使用电脑时会自动恢复，观看面板随后连接。'],
  pausing: ['沙盒正在暂停', '空闲环境暂停后，任务和文件会保留。'],
  missing: ['暂时找不到原来的沙盒', '原环境映射已保留，需要服务管理员检查。'],
  unknown: ['沙盒状态待确认', '暂时无法确认环境，稍后会自动重试。'],
};

export function openDesktop(baseURL: string, token: string): () => void {
  const pane = document.createElement('aside');
  pane.className = 'desktop-pane';
  pane.setAttribute('aria-label', '沙盒实时桌面');
  pane.innerHTML = `<header class="desktop-heading"><div><h2>沙盒桌面 <span>仅查看</span></h2><p>阿芒正在使用的电脑</p></div><div class="desktop-actions"><button type="button" data-action="retry">重新连接</button><button type="button" data-action="close" aria-label="关闭桌面">✕</button></div></header>`;
  const stage = document.createElement('div');
  stage.className = 'desktop-stage';
  const screen = document.createElement('div');
  screen.className = 'desktop-screen';
  const placeholder = document.createElement('div');
  placeholder.className = 'desktop-placeholder';
  const symbol = document.createElement('span');
  symbol.className = 'desktop-symbol';
  symbol.textContent = '▣';
  const title = document.createElement('h3');
  const detail = document.createElement('p');
  placeholder.append(symbol, title, detail);
  stage.append(screen, placeholder);
  const footer = document.createElement('footer');
  footer.className = 'desktop-footer';
  const connection = document.createElement('span');
  connection.className = 'desktop-connection';
  connection.setAttribute('role', 'status');
  connection.setAttribute('aria-live', 'polite');
  const note = document.createElement('span');
  note.textContent = '鼠标与键盘由阿芒操作';
  footer.append(connection, note);
  pane.append(stage, footer);
  document.body.append(pane);
  document.body.classList.add('desktop-open');
  let closed = false;
  let expired = false;
  let socket: WebSocket | undefined;
  let viewer: RFB | undefined;
  let polling: ReturnType<typeof setTimeout> | undefined;
  let authTimeout: ReturnType<typeof setTimeout> | undefined;
  let controller: AbortController | undefined;
  let generation = 0;

  function display(heading: string, description: string, live = false) {
    title.textContent = heading;
    detail.textContent = description;
    placeholder.hidden = live;
    screen.hidden = !live;
    connection.textContent = live ? '● 实时画面' : heading;
    pane.classList.toggle('is-live', live);
  }
  function disconnect() {
    generation++;
    clearTimeout(authTimeout);
    viewer?.disconnect();
    viewer = undefined;
    if (socket) {
      socket.onclose = null;
      socket.onmessage = null;
      socket.onerror = null;
      socket.close();
    }
    socket = undefined;
    screen.replaceChildren();
  }
  function connect() {
    display('正在连接桌面', '正在建立安全的画面连接…');
    const version = generation;
    const endpoint = new URL(`${baseURL}/sandbox/desktop`);
    endpoint.protocol = endpoint.protocol === 'https:' ? 'wss:' : 'ws:';
    const channel = new WebSocket(endpoint);
    socket = channel;
    const failed = () => {
      if (closed || version !== generation) return;
      disconnect();
      display('桌面连接已断开', '正在检查沙盒状态，稍后重新连接。');
    };
    channel.onopen = () => channel.send(JSON.stringify({ type: 'auth', token }));
    channel.onclose = failed;
    channel.onerror = failed;
    authTimeout = setTimeout(failed, 12000);
    channel.onmessage = (event: MessageEvent<unknown>) => {
      if (closed || version !== generation || typeof event.data !== 'string') return;
      try {
        readySchema.parse(JSON.parse(event.data));
        channel.onmessage = null;
        clearTimeout(authTimeout);
        const rfb = new RFB(screen, channel);
        viewer = rfb;
        rfb.viewOnly = true;
        rfb.scaleViewport = true;
        rfb.resizeSession = false;
        rfb.showDotCursor = false;
        rfb.addEventListener('connect', () => {
          if (!closed && version === generation) display('实时画面', '', true);
        });
        rfb.addEventListener('disconnect', failed);
        rfb.addEventListener('securityfailure', failed);
      } catch {
        failed();
      }
    };
  }
  async function refresh() {
    if (closed || expired || document.hidden) return;
    controller?.abort();
    const request = new AbortController();
    controller = request;
    const timeout = setTimeout(() => request.abort(), 10000);
    try {
      const response = await fetch(`${baseURL}/sandbox`, {
        headers: { Authorization: `Bearer ${token}` },
        cache: 'no-store',
        signal: request.signal,
      });
      if (closed || controller !== request) return;
      if (response.status === 401) {
        expired = true;
        disconnect();
        display('登录已过期', '请刷新聊天页面并重新登录。');
        return;
      }
      if (!response.ok) throw new Error('status unavailable');
      const status = statusSchema.parse(await response.json());
      if (closed || controller !== request) return;
      if (status.state === 'running') {
        if (!socket) connect();
      } else {
        disconnect();
        const [heading, description] = descriptions[status.state] ?? [
          '沙盒暂不可用',
          '环境就绪后，画面会自动连接。',
        ];
        display(heading, description);
      }
    } catch {
      if (closed || controller !== request || document.hidden) return;
      disconnect();
      display('暂时无法连接沙盒', '请检查网络，面板会自动重试。');
    } finally {
      clearTimeout(timeout);
      if (!closed && !expired && controller === request && !document.hidden)
        polling = setTimeout(() => void refresh(), 5000);
    }
  }
  function visibilityChanged() {
    clearTimeout(polling);
    if (document.hidden) {
      controller?.abort();
      disconnect();
      display('画面已停止', '回到页面后自动重新连接。');
    } else void refresh();
  }
  function close() {
    if (closed) return;
    closed = true;
    clearTimeout(polling);
    controller?.abort();
    disconnect();
    document.removeEventListener('visibilitychange', visibilityChanged);
    window.removeEventListener('pagehide', close);
    window.removeEventListener('storage', sessionChanged);
    pane.remove();
    document.body.classList.remove('desktop-open');
    window.dispatchEvent(new Event('resize'));
  }
  function sessionChanged(event: StorageEvent) {
    if (event.key === 'aether_session' && event.newValue !== token) close();
  }
  pane.querySelector('[data-action="close"]')?.addEventListener('click', close);
  pane.querySelector('[data-action="retry"]')?.addEventListener('click', () => {
    clearTimeout(polling);
    disconnect();
    void refresh();
  });
  document.addEventListener('visibilitychange', visibilityChanged);
  window.addEventListener('pagehide', close);
  window.addEventListener('storage', sessionChanged);
  display('正在读取沙盒状态', '正在确认你的工作环境…');
  window.dispatchEvent(new Event('resize'));
  void refresh();
  return close;
}
