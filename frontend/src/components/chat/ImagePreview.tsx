// 本文件为草稿与历史消息提供同一个图片预览：缩略图点击后打开原图，支持 Escape 和关闭按钮。

import { useEffect, useRef, useState } from 'react';
import type { MessageImage } from '../../lib/messageAttachments.js';

// ImagePreview 使用原生模态对话框管理焦点，使键盘与触摸用户都能查看附件。
export function ImagePreview({ image }: { image: MessageImage }) {
  const [open, setOpen] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    if (open) dialog.current?.showModal();
    else dialog.current?.close();
  }, [open]);
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
      <dialog
        ref={dialog}
        className="image-preview"
        aria-label={image.name}
        onClose={() => setOpen(false)}
        onClick={(event) => {
          if (event.target === event.currentTarget) setOpen(false);
        }}
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
      </dialog>
    </>
  );
}
