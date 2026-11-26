// 本文件是首屏连接加载组件：在 HTML 预渲染外壳与 WebSocket 握手期间展示静态 aether 角色与连接提示，零外部图片请求。

// LoadingScreen 在应用初始化与异步页面加载期间渲染，保持唤醒阶段视觉稳定不闪屏。
export function LoadingScreen() {
  return (
    <main className="loading" aria-busy="true">
      <div className="loading-stage" role="status" aria-live="polite" aria-label="正在连接 aether">
        <div className="loading-figure" aria-hidden="true">
          <span className="loading-glow" />
          <svg className="loading-mascot" viewBox="0 0 120 120" fill="none">
            <defs>
              <radialGradient id="loading-mascot-coat" cx="36%" cy="26%" r="74%">
                <stop offset="0%" stopColor="#fffaf0" />
                <stop offset="52%" stopColor="#f6d794" />
                <stop offset="86%" stopColor="#e8be6f" />
                <stop offset="100%" stopColor="#d8a855" />
              </radialGradient>
            </defs>
            <ellipse className="loading-mascot-shadow" cx="60" cy="104" rx="23" ry="4.8" />
            <path
              d="M 60 22 C 75 22, 94 54, 94 78 C 94 93, 80 96, 60 96 C 40 96, 26 93, 26 78 C 26 54, 45 22, 60 22 Z"
              fill="url(#loading-mascot-coat)"
            />
            <ellipse
              cx="48"
              cy="36"
              rx="9.5"
              ry="5"
              transform="rotate(-28 48 36)"
              fill="#ffffff"
              fillOpacity="0.45"
            />
            <circle cx="49" cy="52" r="4.1" fill="#18271e" />
            <circle cx="47.7" cy="50.6" r="1.35" fill="#fffdf6" />
            <circle cx="50.3" cy="53.2" r="0.65" fill="#fffdf6" fillOpacity="0.55" />
            <circle cx="71" cy="52" r="4.1" fill="#18271e" />
            <circle cx="69.7" cy="50.6" r="1.35" fill="#fffdf6" />
            <circle cx="72.3" cy="53.2" r="0.65" fill="#fffdf6" fillOpacity="0.55" />
          </svg>
        </div>
        <p className="loading-label" aria-hidden="true">
          正在连接…
        </p>
      </div>
    </main>
  );
}
