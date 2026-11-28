// 本文件为草稿与历史消息提供同一个图片预览：缩略图点击后打开原图，支持 Escape 和关闭按钮。

import { useState } from 'react';
import type { MessageImage } from '../../../lib/messageAttachments.js';
import { ModalDialog } from '../../ModalDialog.js';

// ImagePreview 使用受控模态对话框管理焦点，使键盘与触摸用户都能查看附件。
export function ImagePreview({ image }: { image: MessageImage }) {
  const [open, setOpen] = useState(false);
  if (!image.url) return <span className="image-unavailable">图片暂时不可用，请重新加载历史</span>;
  return (
    <>
      <button
        type="button"
        className="image-thumbnail"
        aria-label={`查看图片：${image.name}`}
        onClick={() => setOpen(true)}
      >
        <img src={image.url} alt={image.name} loading="lazy" />
      </button>
      <ModalDialog
        open={open}
        className="image-preview"
        ariaLabel={image.name}
        onClose={() => setOpen(false)}
      >
        {open && (
          <>
            <button
              type="button"
              className="image-preview-close"
              aria-label="关闭图片"
              onClick={() => setOpen(false)}
            >
              ×
            </button>
            <img src={image.url} alt={image.name} />
          </>
        )}
      </ModalDialog>
    </>
  );
}
