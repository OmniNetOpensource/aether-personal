// 本文件是助手消息气泡组件：渲染阿芒发出的一条消息，含 Markdown 正文与同组连续消息的衔接样式。

import type { ChatMessage } from './ChatController.js';
import { Markdown } from './Markdown.js';

export function AssistantMessageBubble({
  message,
  joinedWithPrevious,
  endsGroup,
}: {
  message: ChatMessage;
  joinedWithPrevious: boolean;
  endsGroup: boolean;
}) {
  return (
    <div className={`message-row ${joinedWithPrevious ? 'joined' : ''}`}>
      <article className="message-column" aria-label="阿芒">
        <div
          className={`bubble ${joinedWithPrevious ? 'join-top' : ''} ${endsGroup ? 'group-end' : ''}`}
        >
          <Markdown text={message.text} />
        </div>
      </article>
    </div>
  );
}
