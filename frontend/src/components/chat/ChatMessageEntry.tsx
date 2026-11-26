// 本文件共用聊天消息的时间分隔、说话人分组和气泡渲染，供实时列表、历史上下文窗口与公开分享页使用。

import { memo, useCallback } from 'react';
import type { ChatMessage } from '../../lib/ChatController.js';
import type { MessageQuote } from '../../lib/messageAttachments.js';
import { AssistantMessageBubble } from './AssistantMessageBubble.js';
import { UserMessageBubble } from './UserMessageBubble.js';
import { MessageTimeSeparator, sameDay } from './MessageTimeSeparator.js';

// getMessageGroupState 根据完整列表中的相邻消息计算时间分隔与连续气泡圆角分组。
export function getMessageGroupState(messages: readonly ChatMessage[], index: number) {
  const message = messages[index];
  const previous = messages[index - 1];
  const next = messages[index + 1];
  const showTime =
    !previous ||
    !sameDay(previous.createdAt, message.createdAt) ||
    Math.abs(message.createdAt.getTime() - previous.createdAt.getTime()) >= 30 * 60000;
  const joinedWithPrevious = Boolean(previous && sameGroup(previous, message));
  const endsGroup = !(next && sameGroup(message, next));
  return { showTime, joinedWithPrevious, endsGroup };
}

// ChatMessageEntry 渲染一条消息及其时间分隔；按单条消息与分组布尔值做 memo，避免其他行更新时整表重渲染。
export const ChatMessageEntry = memo(function ChatMessageEntry({
  message,
  showTime,
  joinedWithPrevious,
  endsGroup,
  onQuoteNavigate,
  expanded,
  onExpandedChange,
}: {
  message: ChatMessage;
  showTime: boolean;
  joinedWithPrevious: boolean;
  endsGroup: boolean;
  onQuoteNavigate: (quote: MessageQuote) => void;
  expanded: boolean;
  onExpandedChange: (messageID: string, expanded: boolean) => void;
}) {
  const handleExpandedChange = useCallback(
    (nextExpanded: boolean) => {
      onExpandedChange(message.id, nextExpanded);
    },
    [message.id, onExpandedChange],
  );
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
          onExpandedChange={handleExpandedChange}
        />
      ) : (
        <AssistantMessageBubble
          message={message}
          joinedWithPrevious={joinedWithPrevious}
          endsGroup={endsGroup}
          expanded={expanded}
          onExpandedChange={handleExpandedChange}
        />
      )}
    </>
  );
});

// sameGroup 只把同日、两分钟内的同一说话人消息连成一组，和实时列表保持相同的气泡衔接。
function sameGroup(a: ChatMessage, b: ChatMessage) {
  return (
    a.isUser === b.isUser &&
    sameDay(a.createdAt, b.createdAt) &&
    Math.abs(a.createdAt.getTime() - b.createdAt.getTime()) < 120000
  );
}
