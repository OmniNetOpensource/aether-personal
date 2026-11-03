// 本文件定义管理看板的 HTML 外壳，集中维护页面元信息、品牌图标与全局样式。

import { createRootRoute, HeadContent, Outlet, Scripts } from '@tanstack/react-router';
import '../style.css';

export const Route = createRootRoute({
  head: () => ({
    meta: [
      { charSet: 'UTF-8' },
      { title: '阿芒 · 管理看板' },
      { name: 'viewport', content: 'width=device-width, initial-scale=1' },
      { name: 'color-scheme', content: 'light dark' },
      { name: 'description', content: '阿芒 用户与 Agent 调用观察看板' },
    ],
    links: [
      { rel: 'icon', type: 'image/png', href: '/app-icon.png' },
      { rel: 'apple-touch-icon', href: '/app-icon.png' },
    ],
  }),
  component: RootDocument,
});

// Start 在构建时渲染此外壳，浏览器再加载看板并恢复管理员会话。
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
        <noscript>请开启 JavaScript，进入管理看板。</noscript>
        <Scripts />
      </body>
    </html>
  );
}
