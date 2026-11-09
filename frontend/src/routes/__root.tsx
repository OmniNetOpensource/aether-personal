// 本文件定义 TanStack Start 的 HTML 外壳，集中维护页面元信息、PWA 清单、全局样式与脚本注入。

import { createRootRoute, HeadContent, Outlet, Scripts } from '@tanstack/react-router';
import '../styles/style.css';

export const Route = createRootRoute({
  head: () => ({
    meta: [
      { charSet: 'UTF-8' },
      { title: '阿芒' },
      {
        name: 'viewport',
        content:
          'width=device-width, initial-scale=1, viewport-fit=cover, interactive-widget=resizes-content',
      },
      { name: 'color-scheme', content: 'light dark' },
      { name: 'apple-mobile-web-app-capable', content: 'yes' },
      { name: 'apple-mobile-web-app-status-bar-style', content: 'black-translucent' },
      { name: 'apple-mobile-web-app-title', content: '阿芒' },
      { name: 'description', content: '和阿芒聊聊，你的个人 AI 助手。' },
    ],
    links: [
      { rel: 'icon', type: 'image/png', href: '/app-icon.png' },
      { rel: 'apple-touch-icon', href: '/icons/apple-touch-icon.png' },
      { rel: 'manifest', href: '/manifest.webmanifest' },
    ],
  }),
  component: RootDocument,
});

// Start 在构建时渲染此外壳，浏览器再加载首页并恢复登录与实时连接。
function RootDocument() {
  return (
    <html lang="zh-CN">
      <head>
        {/* Router 按 name 去重 meta，这两条按系统外观匹配的主题色直接放进外壳。 */}
        <meta name="theme-color" content="#204b32" media="(prefers-color-scheme: light)" />
        <meta name="theme-color" content="#11251c" media="(prefers-color-scheme: dark)" />
        <HeadContent />
      </head>
      <body>
        <div id="root">
          <Outlet />
        </div>
        <noscript>请开启 JavaScript，和阿芒继续对话。</noscript>
        <Scripts />
      </body>
    </html>
  );
}
