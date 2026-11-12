// 本文件定义公开对话分享路由 /s/$shareId，免登录直接渲染选中的聊天内容，不建立主聊天 WebSocket 连接。

import { createFileRoute } from '@tanstack/react-router';
import { SharedMessagesPage } from '../components/chat/SharedMessagesPage.js';

export const Route = createFileRoute('/s/$shareId')({
  ssr: false,
  component: SharedRoutePage,
});

// SharedRoutePage 提取路径中的分享令牌并交给公开分享页面渲染。
function SharedRoutePage() {
  const { shareId } = Route.useParams();
  return <SharedMessagesPage shareToken={shareId} />;
}
