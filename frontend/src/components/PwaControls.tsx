// 本文件用 Effect Scope 与 Schedule 注册 Service Worker，并在打开、重新联网、回到前台或每分钟检查最新版本。

import { useEffect, useState } from 'react';
import { Effect, Fiber, Schedule } from 'effect';

// PwaControls 为活跃页面定期检查更新；Service Worker 安装完成后负责切换应用页面。
export function PwaControls() {
  const [offlineUnavailable, setOfflineUnavailable] = useState(false);

  useEffect(() => {
    if (!import.meta.env.PROD || !('serviceWorker' in navigator)) return;

    const program = Effect.scoped(
      Effect.gen(function* () {
        const registration = yield* Effect.tryPromise(() =>
          navigator.serviceWorker.register('/sw.js', { updateViaCache: 'none' }),
        ).pipe(
          Effect.catch(() =>
            Effect.sync(() => {
              setOfflineUnavailable(true);
            }).pipe(Effect.andThen(Effect.fail('register_failed' as const))),
          ),
        );

        let checking = false;
        const refresh = Effect.gen(function* () {
          if (checking || document.hidden || !navigator.onLine) return;
          checking = true;
          yield* Effect.tryPromise(() => registration.update()).pipe(
            Effect.ignore,
            Effect.ensuring(
              Effect.sync(() => {
                checking = false;
              }),
            ),
          );
        });

        const triggerRefresh = () => {
          Effect.runFork(refresh);
        };

        yield* Effect.acquireRelease(
          Effect.sync(() => {
            document.addEventListener('visibilitychange', triggerRefresh);
            window.addEventListener('online', triggerRefresh);
            window.addEventListener('focus', triggerRefresh);
            window.addEventListener('pageshow', triggerRefresh);
            return () => {
              document.removeEventListener('visibilitychange', triggerRefresh);
              window.removeEventListener('online', triggerRefresh);
              window.removeEventListener('focus', triggerRefresh);
              window.removeEventListener('pageshow', triggerRefresh);
            };
          }),
          (cleanup) => Effect.sync(cleanup),
        );

        // 打开后立即检查一次，之后每 60 秒轮询一次直到组件卸载中断。
        yield* Effect.repeat(refresh, Schedule.spaced('60 seconds'));
      }),
    ).pipe(Effect.ignore);

    const fiber = Effect.runFork(program);
    return () => {
      Effect.runFork(Fiber.interrupt(fiber));
    };
  }, []);

  return offlineUnavailable ? (
    <span className="connection-status" role="status">
      离线启动暂不可用
    </span>
  ) : null;
}
