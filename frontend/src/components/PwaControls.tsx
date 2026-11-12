// 本文件注册 Service Worker，并在打开、重新联网或回到前台时检查最新版本。

import { useEffect, useState } from 'react';

// PwaControls 为活跃页面定期检查更新；Service Worker 安装完成后负责切换应用页面。
export function PwaControls() {
  const [offlineUnavailable, setOfflineUnavailable] = useState(false);
  useEffect(() => {
    if (process.env.NODE_ENV !== 'production' || !('serviceWorker' in navigator)) return;
    let disposed = false;
    let registration: ServiceWorkerRegistration | undefined;
    let checking = false;

    // 一分钟检查一次活跃标签页，焦点、前后台和网络变化时立即再检查。
    const refresh = () => {
      if (!registration || checking || document.hidden || !navigator.onLine) return;
      checking = true;
      void registration
        .update()
        .catch(() => {})
        .finally(() => {
          checking = false;
        });
    };
    void navigator.serviceWorker
      .register('/sw.js', { updateViaCache: 'none' })
      .then((value) => {
        if (disposed) return;
        registration = value;
        refresh();
      })
      .catch(() => {
        if (!disposed) setOfflineUnavailable(true);
      });

    const interval = window.setInterval(refresh, 60_000);
    document.addEventListener('visibilitychange', refresh);
    window.addEventListener('online', refresh);
    window.addEventListener('focus', refresh);
    window.addEventListener('pageshow', refresh);
    return () => {
      disposed = true;
      window.clearInterval(interval);
      document.removeEventListener('visibilitychange', refresh);
      window.removeEventListener('online', refresh);
      window.removeEventListener('focus', refresh);
      window.removeEventListener('pageshow', refresh);
    };
  }, []);

  return offlineUnavailable ? (
    <span className="connection-status" role="status">
      离线启动暂不可用
    </span>
  ) : null;
}
