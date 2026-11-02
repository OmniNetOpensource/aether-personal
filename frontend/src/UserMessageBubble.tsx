// 本文件是用户消息气泡组件：渲染用户发出的一条已保存消息；发送中与发送失败的状态由输入框展示，气泡不带状态。

import { ImagePreview } from './ImagePreview.js';
import type { MessageQuote } from './messageAttachments.js';
import type { ChatMessage } from './ChatController.js';

export function UserMessageBubble({
  message,
  joinedWithPrevious,
  endsGroup,
  onQuoteNavigate,
}: {
  message: ChatMessage;
  joinedWithPrevious: boolean;
  endsGroup: boolean;
  onQuoteNavigate: (quote: MessageQuote) => void;
}) {
  return (
    <div className={`message-row user-row ${joinedWithPrevious ? 'joined' : ''}`}>
      <article className="message-column" aria-label="你">
        <div
          className={`bubble user-bubble ${joinedWithPrevious ? 'join-top' : ''} ${endsGroup ? 'group-end' : ''}`}
        >
          {message.parts.map((part, index) =>
            part.type === 'quote' ? (
              <button
                type="button"
                className="sent-quote"
                key={index}
                onClick={() => onQuoteNavigate(part.quote)}
                aria-label="查看引用原文"
              >
                <blockquote>{part.quote.text}</blockquote>
              </button>
            ) : part.type === 'image' ? (
              <div className="message-images" key={index}>
                <ImagePreview image={part.image} />
              </div>
            ) : (
              <p className="user-text" key={index}>
                {part.text}
              </p>
            ),
          )}
        </div>
      </article>
    </div>
  );
}
