// 本文件是用户消息气泡组件：按原顺序展示可折叠的文字、引用与图片，发送状态仍由输入框负责。

import { ImagePreview } from './ImagePreview.js';
import { CollapsibleMessage } from './CollapsibleMessage.js';
import type { MessageQuote } from './messageAttachments.js';
import type { ChatMessage } from './ChatController.js';

// UserMessageBubble 共用整条正文的折叠容器，展开后仍可回看引用与预览图片。
export function UserMessageBubble({
  message,
  joinedWithPrevious,
  endsGroup,
  onQuoteNavigate,
  expanded,
  onExpandedChange,
}: {
  message: ChatMessage;
  joinedWithPrevious: boolean;
  endsGroup: boolean;
  onQuoteNavigate: (quote: MessageQuote) => void;
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
}) {
  return (
    <div className={`message-row user-row ${joinedWithPrevious ? 'joined' : ''}`}>
      <article className="message-column" aria-label="你">
        <div
          className={`bubble user-bubble ${joinedWithPrevious ? 'join-top' : ''} ${endsGroup ? 'group-end' : ''}`}
        >
          <CollapsibleMessage expanded={expanded} onExpandedChange={onExpandedChange}>
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
          </CollapsibleMessage>
        </div>
      </article>
    </div>
  );
}
