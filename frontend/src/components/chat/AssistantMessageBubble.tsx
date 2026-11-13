// 本文件是助手消息气泡组件：渲染可折叠的 Markdown 正文，或展示 aether 从工作区发回的独立图片与可下载文件气泡。

import type { ChatMessage } from '../../lib/ChatController.js';
import { Markdown } from '../markdown/Markdown.js';
import { CollapsibleMessage } from './CollapsibleMessage.js';
import { FileAttachmentCard } from './FileAttachmentCard.js';
import { ImagePreview } from './ImagePreview.js';

// AssistantMessageBubble 把折叠按钮放在引用正文之外，Markdown 标成可引用正文，确保引用位置只计算原文；文件与图片附件独立渲染。
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
  const attachments = message.parts.filter(
    (part) => part.type === 'image' || part.type === 'file',
  );
  return (
    <div className={`message-row ${joinedWithPrevious ? 'joined' : ''}`}>
      <article className="message-column assistant-column" aria-label="aether">
        <div
          className={`bubble ${joinedWithPrevious ? 'join-top' : ''} ${endsGroup ? 'group-end' : ''}`}
        >
          <CollapsibleMessage expanded={expanded} onExpandedChange={onExpandedChange}>
            {message.text ? (
              <div data-quote-body data-quote-text data-message-id={message.id}>
                <Markdown text={message.text} />
              </div>
            ) : null}
            {attachments.map((part, index) =>
              part.type === 'image' ? (
                <div className="message-images" key={index}>
                  <ImagePreview image={part.image} />
                </div>
              ) : (
                <div className="message-files" key={index}>
                  <FileAttachmentCard file={part.file} />
                </div>
              ),
            )}
          </CollapsibleMessage>
        </div>
      </article>
    </div>
  );
}
