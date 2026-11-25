// 本文件统一封装原生 <dialog> 模态框，集中处理挂载显示、Escape 键收起、阻止关闭态与点击遮罩关闭。

import { useLayoutEffect, useRef } from 'react';
import type { ReactNode } from 'react';

// ModalDialog 包装原生模态框的焦点约束与关闭交互，避免各浮层重复实现 showModal 与关闭监听。
export function ModalDialog({
  open = true,
  canClose = true,
  closeOnBackdrop = true,
  className,
  ariaLabel,
  ariaLabelledBy,
  onClose,
  children,
}: {
  open?: boolean;
  canClose?: boolean;
  closeOnBackdrop?: boolean;
  className?: string;
  ariaLabel?: string;
  ariaLabelledBy?: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const dialog = useRef<HTMLDialogElement>(null);

  // 随 open 属性同步原生顶层模态状态，组件卸载时立即关闭以恢复页面交互。
  useLayoutEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (open && !element.open) element.showModal();
    else if (!open && element.open) element.close();
    return () => {
      if (element.open) element.close();
    };
  }, [open]);

  return (
    <dialog
      ref={dialog}
      className={className}
      aria-label={ariaLabel}
      aria-labelledby={ariaLabelledBy}
      onKeyDown={(event) => {
        // 输入法组词或嵌套子弹窗打开时，不拦截属于内层元素的 Escape 按键。
        if (
          event.key === 'Escape' &&
          !event.nativeEvent.isComposing &&
          event.target instanceof Element &&
          event.target.closest('dialog') === event.currentTarget
        ) {
          event.preventDefault();
          event.stopPropagation();
          if (canClose) onClose();
        }
      }}
      onCancel={(event) => {
        event.preventDefault();
        if (canClose) onClose();
      }}
      onClick={(event) => {
        if (!closeOnBackdrop || !canClose || event.target !== event.currentTarget) return;
        const rect = event.currentTarget.getBoundingClientRect();
        if (
          event.clientX < rect.left ||
          event.clientX > rect.right ||
          event.clientY < rect.top ||
          event.clientY > rect.bottom
        ) {
          onClose();
        }
      }}
    >
      {children}
    </dialog>
  );
}
