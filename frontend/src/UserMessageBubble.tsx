// 本文件是用户消息气泡组件：渲染用户发出的一条消息，并在最后一条或失败时显示送达状态与重试入口。

import type { ChatMessage } from './ChatController.js';

export function UserMessageBubble({
  message,
  isLastUserMessage,
  joinedWithPrevious,
  endsGroup,
  onRetry,
}: {
  message: ChatMessage;
  isLastUserMessage: boolean;
  joinedWithPrevious: boolean;
  endsGroup: boolean;
  onRetry: () => void;
}) {
  const showDeliveryStatus =
    isLastUserMessage || message.delivery === 'sending' || message.delivery === 'failed';
  return (
    <div className={`message-row user-row ${joinedWithPrevious ? 'joined' : ''}`}>
      <article className="message-column" aria-label="你">
        <div
          className={`bubble user-bubble ${joinedWithPrevious ? 'join-top' : ''} ${endsGroup ? 'group-end' : ''}`}
        >
          <p className="user-text">{message.text}</p>
        </div>
        {showDeliveryStatus &&
          (message.delivery === 'failed' ? (
            <button type="button" className="retry error delivery" onClick={onRetry}>
              ⓘ 发送失败，点击重发
            </button>
          ) : (
            <span className="delivery">
              {message.delivery === 'sending'
                ? '发送中…'
                : message.delivery === 'read'
                  ? '已读'
                  : '已送达'}
            </span>
          ))}
      </article>
    </div>
  );
}
