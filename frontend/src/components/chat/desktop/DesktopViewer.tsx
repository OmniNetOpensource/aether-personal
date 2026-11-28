// 本文件显示当前用户的只读实时桌面；用 Effect Schema 校验状态帧，并用 Effect Scope 托管连接、握手超时与 noVNC 资源释放。

import { useEffect, useRef, useState } from 'react';
import { Deferred, Effect, Fiber, FiberHandle, Result, Schema } from 'effect';
import type RFB from '@novnc/novnc';
import { ModalDialog } from '../../ModalDialog.js';
import './desktopViewer.css';

// 观察状态来自后端边界；运行中的连接在 ready 后切换为二进制 RFB，不与聊天消息混用。
const DesktopStateSchema = Schema.Literals([
  'unconfigured',
  'unallocated',
  'paused',
  'missing',
  'needs_update',
  'killing',
  'killed',
]);
type DesktopState = typeof DesktopStateSchema.Type;

const DesktopFrameFromJsonSchema = Schema.fromJsonString(
  Schema.Union([
    Schema.Struct({ type: Schema.Literal('ready') }),
    Schema.Struct({ type: Schema.Literal('state'), state: DesktopStateSchema }),
  ]),
);
const decodeDesktopFrame = Schema.decodeUnknownResult(DesktopFrameFromJsonSchema);

const desktopStateLabels: Record<DesktopState, string> = {
  unconfigured: '电脑尚未配置，请联系维护者配置长期工作区。',
  unallocated: 'Agent 还没有使用过电脑，首次使用后即可查看。',
  paused: '电脑已休眠，Agent 下次使用时会自动恢复。',
  missing: '电脑实例已释放，Agent 下次使用时会重建并接回原文件。',
  needs_update: '电脑环境需要更新，维护者更新后即可查看实时画面。',
  killing: '电脑正在释放，等待 Agent 下次使用。',
  killed: '电脑实例已释放，Agent 下次使用时会重建并接回原文件。',
};

type ViewerState =
  | { readonly kind: 'connecting' }
  | { readonly kind: 'live' }
  | { readonly kind: 'hidden' }
  | { readonly kind: 'waiting'; readonly state: DesktopState }
  | { readonly kind: 'error'; readonly message: string };

// DesktopViewer 独立持有观察连接；不改变聊天草稿、消息滚动位置或沙箱的空闲计时。
export function DesktopViewer({ token, onClose }: { token: string; onClose: () => void }) {
  const screen = useRef<HTMLDivElement>(null);
  const [state, setState] = useState<ViewerState>({ kind: 'connecting' });
  const [expanded, setExpanded] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const program = Effect.scoped(
      Effect.gen(function* () {
        const connectionHandle = yield* FiberHandle.make<void, never>();

        // 单次连接循环：先加载 noVNC，再建立认证 WebSocket；ready 后移交二进制 RFB，断线按 5 秒间隔重试。
        const runSessionLoop: Effect.Effect<void> = Effect.gen(function* () {
          while (!document.hidden) {
            if (!navigator.onLine) {
              setState({ kind: 'error', message: '网络已断开，恢复网络后会重新连接。' });
              return;
            }
            setState((current) => (current.kind === 'waiting' ? current : { kind: 'connecting' }));

            const loaded = yield* Effect.tryPromise(() => import('@novnc/novnc')).pipe(
              Effect.option,
            );
            if (loaded._tag === 'None') {
              setState({ kind: 'error', message: '无法加载桌面查看器，请刷新页面重试。' });
              return;
            }
            const target = screen.current;
            if (!target || document.hidden) return;
            const RFBConstructor = loaded.value.default;

            const shouldRetry = yield* Effect.scoped(
              Effect.gen(function* () {
                const closed = yield* Deferred.make<{
                  readonly code: number;
                  readonly keepStatus: boolean;
                  readonly retryable: boolean;
                }>();
                const handshakeHandle = yield* FiberHandle.make<void, never>();
                let viewer: RFB | null = null;
                let keepStatus = false;
                let retryable = true;

                const channel = yield* Effect.acquireRelease(
                  Effect.sync(() => {
                    const url = new URL('/sandbox/desktop', location.origin);
                    url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
                    const ws = new WebSocket(url);
                    ws.binaryType = 'arraybuffer';
                    return ws;
                  }),
                  (ws) =>
                    Effect.sync(() => {
                      ws.onmessage = null;
                      viewer?.disconnect();
                      viewer = null;
                      ws.close();
                    }),
                );

                yield* FiberHandle.run(
                  handshakeHandle,
                  Effect.sleep('40 seconds').pipe(
                    Effect.andThen(Effect.sync(() => channel.close())),
                  ),
                );

                channel.addEventListener('open', () => {
                  try {
                    channel.send(JSON.stringify({ type: 'hello', token }));
                  } catch {
                    channel.close();
                  }
                });

                channel.addEventListener('close', (event) => {
                  Effect.runFork(
                    Deferred.succeed(closed, {
                      code: event.code,
                      keepStatus,
                      retryable,
                    }),
                  );
                });

                channel.onmessage = (event: MessageEvent<unknown>) => {
                  if (typeof event.data !== 'string') {
                    keepStatus = true;
                    retryable = false;
                    setState({ kind: 'error', message: '无法读取桌面画面，请重新连接。' });
                    channel.close(4002, 'invalid desktop response');
                    return;
                  }
                  const decoded = decodeDesktopFrame(event.data);
                  if (Result.isFailure(decoded)) {
                    keepStatus = true;
                    retryable = false;
                    setState({ kind: 'error', message: '无法读取桌面画面，请重新连接。' });
                    channel.close(4002, 'invalid desktop response');
                    return;
                  }
                  if (decoded.success.type === 'state') {
                    keepStatus = true;
                    setState({ kind: 'waiting', state: decoded.success.state });
                    return;
                  }
                  // 后端也过滤 RFB 控制消息；前端只读设置用于避免捕获键盘和触屏输入。
                  const remote = new RFBConstructor(target, channel, { shared: true });
                  viewer = remote;
                  remote.viewOnly = true;
                  remote.focusOnClick = false;
                  remote.scaleViewport = true;
                  remote.resizeSession = false;
                  remote.background = '#101913';
                  remote.addEventListener('connect', () => {
                    Effect.runFork(FiberHandle.clear(handshakeHandle));
                    setState({ kind: 'live' });
                  });
                };

                const outcome = yield* Deferred.await(closed);
                yield* FiberHandle.clear(handshakeHandle);
                if (outcome.code === 1008) {
                  setState({ kind: 'error', message: '登录已失效，请重新登录后查看电脑。' });
                  return false;
                }
                if (!outcome.keepStatus) {
                  setState({ kind: 'error', message: '画面连接已断开，正在重新连接…' });
                }
                return outcome.retryable;
              }),
            );

            if (!shouldRetry || document.hidden) return;
            yield* Effect.sleep('5 seconds');
          }
        });

        const start = () => {
          Effect.runFork(
            FiberHandle.run(connectionHandle, runSessionLoop, { onlyIfMissing: true }),
          );
        };
        const stop = (nextState: ViewerState) => {
          Effect.runFork(FiberHandle.clear(connectionHandle));
          setState(nextState);
        };

        // 仅在页面可见时观察；返回页面后重新查询真实状态，不沿用先前的“运行中”判断。
        yield* Effect.acquireRelease(
          Effect.sync(() => {
            const onVisibility = () => {
              if (document.hidden) stop({ kind: 'hidden' });
              else start();
            };
            const onOffline = () => {
              stop({ kind: 'error', message: '网络已断开，恢复网络后会重新连接。' });
            };
            const onPageHide = () => stop({ kind: 'hidden' });

            document.addEventListener('visibilitychange', onVisibility);
            window.addEventListener('offline', onOffline);
            window.addEventListener('online', start);
            window.addEventListener('pagehide', onPageHide);
            window.addEventListener('pageshow', start);
            start();

            return () => {
              document.removeEventListener('visibilitychange', onVisibility);
              window.removeEventListener('offline', onOffline);
              window.removeEventListener('online', start);
              window.removeEventListener('pagehide', onPageHide);
              window.removeEventListener('pageshow', start);
            };
          }),
          (cleanup) => Effect.sync(cleanup),
        );

        yield* Effect.never;
      }),
    );

    const fiber = Effect.runFork(program);
    return () => {
      Effect.runFork(Fiber.interrupt(fiber));
    };
  }, [token, attempt]);

  return (
    <ModalDialog
      closeOnBackdrop={false}
      className={`desktop-dialog${expanded ? ' desktop-dialog-expanded' : ''}`}
      ariaLabelledBy="desktop-viewer-title"
      onClose={onClose}
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
            {state.kind === 'connecting' && <span className="desktop-spinner" aria-hidden="true" />}
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
    </ModalDialog>
  );
}
