// 本文件共用聊天消息的时间分隔、说话人分组和气泡渲染，供实时列表与历史上下文窗口使用。

import type { ChatMessage } from '../../lib/ChatController.js';
import type { MessageQuote } from '../../lib/messageAttachments.js';
import { AssistantMessageBubble } from './AssistantMessageBubble.js';
import { UserMessageBubble } from './UserMessageBubble.js';
import { MessageTimeSeparator, sameDay } from './MessageTimeSeparator.js';

// ChatMessageEntry 渲染一条消息及其时间分隔；是否与前后连成一组要看完整列表，不能只看视口里的邻居。
export function ChatMessageEntry({
  messages,
  index,
  onQuoteNavigate,
  expanded,
  onExpandedChange,
}: {
  messages: ChatMessage[];
  index: number;
  onQuoteNavigate: (quote: MessageQuote) => void;
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
}) {
  const message = messages[index];
  const previous = messages[index - 1];
  const next = messages[index + 1];
  const showTime =
    !previous ||
    !sameDay(previous.createdAt, message.createdAt) ||
    Math.abs(message.createdAt.getTime() - previous.createdAt.getTime()) >= 30 * 60000;
  const joinedWithPrevious = Boolean(previous && sameGroup(previous, message));
  const endsGroup = !(next && sameGroup(message, next));
  return (
    <>
      {showTime && <MessageTimeSeparator createdAt={message.createdAt} />}
      {message.isUser ? (
        <UserMessageBubble
          message={message}
          joinedWithPrevious={joinedWithPrevious}
          endsGroup={endsGroup}
          onQuoteNavigate={onQuoteNavigate}
          expanded={expanded}
          onExpandedChange={onExpandedChange}
        />
      ) : (
        <AssistantMessageBubble
          message={message}
          joinedWithPrevious={joinedWithPrevious}
          endsGroup={endsGroup}
          expanded={expanded}
          onExpandedChange={onExpandedChange}
        />
      )}
    </>
  );
}

// sameGroup 只把同日、两分钟内的同一说话人消息连成一组，和实时列表保持相同的气泡衔接。
function sameGroup(a: ChatMessage, b: ChatMessage) {
  return (
    a.isUser === b.isUser &&
    sameDay(a.createdAt, b.createdAt) &&
    Math.abs(a.createdAt.getTime() - b.createdAt.getTime()) < 120000
  );
}
