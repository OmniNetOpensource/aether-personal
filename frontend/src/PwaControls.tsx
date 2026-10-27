import { useEffect, useState } from 'react';

export function PwaControls() {
  const [waiting, setWaiting] = useState<ServiceWorker | null>(null);
  const [updating, setUpdating] = useState(false);
  const [offlineUnavailable, setOfflineUnavailable] = useState(false);
  useEffect(() => {
    if (process.env.NODE_ENV !== 'production' || !('serviceWorker' in navigator)) return;
    let disposed = false;
    let registration: ServiceWorkerRegistration | undefined;
    let installing: ServiceWorker | null = null;
    const installed = () => {
      if (!disposed && installing?.state === 'installed' && navigator.serviceWorker.controller)
        setWaiting(registration?.waiting ?? null);
    };
    const updateFound = () => {
      installing?.removeEventListener('statechange', installed);
      installing = registration?.installing ?? null;
      installing?.addEventListener('statechange', installed);
    };
    const refresh = () => {
      if (!document.hidden && navigator.onLine) void registration?.update().catch(() => {});
    };
    void navigator.serviceWorker
      .register('/sw.js', { updateViaCache: 'none' })
      .then((value) => {
        if (disposed) return;
        registration = value;
        if (value.waiting) setWaiting(value.waiting);
        value.addEventListener('updatefound', updateFound);
        updateFound();
      })
      .catch(() => {
        if (!disposed) setOfflineUnavailable(true);
      });
    document.addEventListener('visibilitychange', refresh);
    window.addEventListener('online', refresh);
    return () => {
      disposed = true;
      registration?.removeEventListener('updatefound', updateFound);
      installing?.removeEventListener('statechange', installed);
      document.removeEventListener('visibilitychange', refresh);
      window.removeEventListener('online', refresh);
    };
  }, []);
  const update = () => {
    if (!waiting) return;
    if (waiting.state === 'activated') {
      location.reload();
      return;
    }
    if (waiting.state === 'redundant') {
      setWaiting(null);
      return;
    }
    setUpdating(true);
    navigator.serviceWorker.addEventListener('controllerchange', () => location.reload(), {
      once: true,
    });
    waiting.postMessage({ type: 'SKIP_WAITING' });
  };
  return (
    <>
      {offlineUnavailable && (
        <span className="connection-status" role="status">
          离线启动暂不可用
        </span>
      )}
      {waiting && (
        <button type="button" className="app-button" disabled={updating} onClick={update}>
          {updating ? '更新中…' : '更新应用'}
        </button>
      )}
    </>
  );
}
