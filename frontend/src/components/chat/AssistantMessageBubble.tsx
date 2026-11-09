// 本文件是助手消息气泡组件：渲染可折叠的 Markdown 正文，保留引用原文与同组消息的衔接样式。

import type { ChatMessage } from '../../lib/ChatController.js';
import { Markdown } from '../markdown/Markdown.js';
import { CollapsibleMessage } from './CollapsibleMessage.js';

// AssistantMessageBubble 把折叠按钮放在引用正文之外，确保引用位置只计算原文。
export function AssistantMessageBubble({
  message,
  joinedWithPrevious,
  endsGroup,
  expanded,
  onExpandedChange,
}: {
  message: ChatMessage;
  joinedWithPrevious: boolean;
  endsGroup: boolean;
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
}) {
  return (
    <div className={`message-row ${joinedWithPrevious ? 'joined' : ''}`}>
      <article className="message-column assistant-column" aria-label="阿芒">
        <div
          className={`bubble ${joinedWithPrevious ? 'join-top' : ''} ${endsGroup ? 'group-end' : ''}`}
        >
          <CollapsibleMessage expanded={expanded} onExpandedChange={onExpandedChange}>
            <div data-quote-body data-message-id={message.id}>
              <Markdown text={message.text} />
            </div>
          </CollapsibleMessage>
        </div>
      </article>
    </div>
  );
}
