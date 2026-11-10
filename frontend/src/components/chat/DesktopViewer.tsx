// 本文件显示当前用户的只读实时桌面；页面隐藏或窗口关闭时释放连接，休眠时仅轮询状态。

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type RFB from '@novnc/novnc';
import { z } from 'zod';
import './desktopViewer.css';

// 观察状态来自后端边界；运行中的连接在 ready 后切换为二进制 RFB，不与聊天消息混用。
const desktopStateSchema = z.enum([
  'unconfigured',
  'unallocated',
  'paused',
  'missing',
  'needs_update',
  'killing',
  'killed',
]);
const desktopFrameSchema = z.discriminatedUnion('type', [
  z.object({ type: z.literal('ready') }),
  z.object({ type: z.literal('state'), state: desktopStateSchema }),
]);
const desktopStateLabels: Record<z.infer<typeof desktopStateSchema>, string> = {
  unconfigured: '电脑尚未配置，请联系维护者配置长期工作区。',
  unallocated: 'Agent 还没有使用过电脑，首次使用后即可查看。',
  paused: '电脑已休眠，Agent 下次使用时会自动恢复。',
  missing: '电脑实例已释放，Agent 下次使用时会重建并接回原文件。',
  needs_update: '电脑环境需要更新，维护者更新后即可查看实时画面。',
  killing: '电脑正在释放，等待 Agent 下次使用。',
  killed: '电脑实例已释放，Agent 下次使用时会重建并接回原文件。',
};
type ViewerState =
  | { kind: 'connecting' }
  | { kind: 'live' }
  | { kind: 'hidden' }
  | { kind: 'waiting'; state: z.infer<typeof desktopStateSchema> }
  | { kind: 'error'; message: string };

// DesktopViewer 独立持有观察连接；不改变聊天草稿、消息滚动位置或沙箱的空闲计时。
export function DesktopViewer({ token, onClose }: { token: string; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const screen = useRef<HTMLDivElement>(null);
  const [state, setState] = useState<ViewerState>({ kind: 'connecting' });
  const [expanded, setExpanded] = useState(false);
  const [attempt, setAttempt] = useState(0);

  // 原生 dialog 约束焦点并支持 Escape；关闭后浏览器把焦点还给“查看电脑”入口。
  useLayoutEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => element?.close();
  }, []);

  useEffect(() => {
    let disposed = false;
    let socket: WebSocket | null = null;
    let viewer: RFB | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let handshakeTimer: ReturnType<typeof setTimeout> | undefined;

    // 页面切到后台、网络断开和组件卸载共用释放逻辑，旧连接不能继续安排重连。
    const disconnect = () => {
      clearTimeout(retryTimer);
      clearTimeout(handshakeTimer);
      const channel = socket;
      socket = null;
      viewer?.disconnect();
      viewer = null;
      channel?.close();
    };

    // 先加载查看器再建立认证连接，ready 回执到达时即可同步安装 RFB 接收器。
    const connect = async () => {
      if (disposed || document.hidden || socket) return;
      if (!navigator.onLine) {
        setState({ kind: 'error', message: '网络已断开，恢复网络后会重新连接。' });
        return;
      }
      setState((current) => (current.kind === 'waiting' ? current : { kind: 'connecting' }));
      try {
        const { default: RFB } = await import('@novnc/novnc');
        if (disposed || document.hidden || socket) return;
        const target = screen.current;
        if (!target) return;
        const url = new URL('/sandbox/desktop', location.origin);
        url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
        const channel = new WebSocket(url);
        channel.binaryType = 'arraybuffer';
        socket = channel;
        let keepStatus = false;
        let retryable = true;
        handshakeTimer = setTimeout(() => channel.close(), 40000);
        channel.addEventListener('open', () => {
          if (!disposed && socket === channel)
            channel.send(JSON.stringify({ type: 'hello', token }));
        });
        channel.addEventListener('close', (event) => {
          if (disposed || socket !== channel) return;
          clearTimeout(handshakeTimer);
          socket = null;
          viewer?.disconnect();
          viewer = null;
          if (event.code === 1008) {
            setState({ kind: 'error', message: '登录已失效，请重新登录后查看电脑。' });
            return;
          }
          if (!keepStatus) setState({ kind: 'error', message: '画面连接已断开，正在重新连接…' });
          if (retryable) retryTimer = setTimeout(() => void connect(), 5000);
        });
        channel.onmessage = (event: MessageEvent<unknown>) => {
          if (disposed || socket !== channel) return;
          try {
            if (typeof event.data !== 'string') throw new Error('Expected desktop status');
            const frame = desktopFrameSchema.parse(JSON.parse(event.data));
            if (frame.type === 'state') {
              keepStatus = true;
              setState({ kind: 'waiting', state: frame.state });
              return;
            }
            // 后端也过滤 RFB 控制消息；前端只读设置用于避免捕获键盘和触屏输入。
            const remote = new RFB(target, channel, { shared: true });
            viewer = remote;
            remote.viewOnly = true;
            remote.focusOnClick = false;
            remote.scaleViewport = true;
            remote.resizeSession = false;
            remote.background = '#101913';
            remote.addEventListener('connect', () => {
              clearTimeout(handshakeTimer);
              if (!disposed && socket === channel) setState({ kind: 'live' });
            });
          } catch {
            keepStatus = true;
            retryable = false;
            setState({ kind: 'error', message: '无法读取桌面画面，请重新连接。' });
            channel.close(4002, 'invalid desktop response');
          }
        };
      } catch {
        if (!disposed) setState({ kind: 'error', message: '无法加载桌面查看器，请刷新页面重试。' });
      }
    };

    // 仅在页面可见时观察；返回页面后重新查询真实状态，不沿用先前的“运行中”判断。
    const suspend = () => {
      disconnect();
      setState({ kind: 'hidden' });
    };
    const visibility = () => {
      if (document.hidden) suspend();
      else void connect();
    };
    const offline = () => {
      disconnect();
      setState({ kind: 'error', message: '网络已断开，恢复网络后会重新连接。' });
    };
    const online = () => void connect();
    document.addEventListener('visibilitychange', visibility);
    window.addEventListener('offline', offline);
    window.addEventListener('online', online);
    window.addEventListener('pagehide', suspend);
    window.addEventListener('pageshow', online);
    void connect();
    return () => {
      disposed = true;
      disconnect();
      document.removeEventListener('visibilitychange', visibility);
      window.removeEventListener('offline', offline);
      window.removeEventListener('online', online);
      window.removeEventListener('pagehide', suspend);
      window.removeEventListener('pageshow', online);
    };
  }, [token, attempt]);

  return (
    <dialog
      ref={dialog}
      className={`desktop-dialog${expanded ? ' desktop-dialog-expanded' : ''}`}
      aria-labelledby="desktop-viewer-title"
      onCancel={onClose}
    >
      <header className="desktop-viewer-header">
        <div>
          <h2 id="desktop-viewer-title">电脑</h2>
          <span>仅查看 · {state.kind === 'live' ? '实时画面' : '等待连接'}</span>
        </div>
        <button type="button" onClick={() => setExpanded((value) => !value)}>
          {expanded ? '还原窗口' : '全屏查看'}
        </button>
        <button type="button" onClick={onClose} aria-label="关闭电脑画面">
          关闭
        </button>
      </header>
      <div className="desktop-viewer-content">
        <div ref={screen} className="desktop-screen" aria-label="Agent 的只读桌面画面" />
        {state.kind !== 'live' && (
          <div className="desktop-viewer-status" role="status">
            <p>
              {state.kind === 'waiting'
                ? desktopStateLabels[state.state]
                : state.kind === 'error'
                  ? state.message
                  : state.kind === 'hidden'
                    ? '页面回到前台后恢复查看。'
                    : '正在连接电脑画面…'}
            </p>
            {state.kind === 'error' && (
              <button type="button" onClick={() => setAttempt((value) => value + 1)}>
                重新连接
              </button>
            )}
          </div>
        )}
      </div>
      <footer className="desktop-viewer-footer">
        观看不会唤醒电脑或延长运行时间。后台命令不会自动出现在桌面中。
      </footer>
    </dialog>
  );
}
