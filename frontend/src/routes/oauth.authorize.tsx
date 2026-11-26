// 本文件定义 /oauth/authorize 远程记忆 MCP 授权确认路由，由路由器按需分包加载。

import { createFileRoute } from '@tanstack/react-router';
import { OAuthAuthorizePage } from '../components/oauth/OAuthAuthorizePage.js';

export const Route = createFileRoute('/oauth/authorize')({
  component: OAuthAuthorizePage,
});
