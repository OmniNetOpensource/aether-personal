// 本文件为用户和助手消息提供按实际高度折叠的正文容器，保留完整内容与引用位置。

import { useId, useLayoutEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';

// 折叠阈值按渲染高度计算，文字换行、代码块和图片都计入正文。
const collapsedMessageHeight = 360;

// CollapsibleMessage 只负责测量和展示；展开状态由列表按消息 ID 保存，避免虚拟行卸载后丢失。
export function CollapsibleMessage({
  expanded,
  onExpandedChange,
  children,
}: {
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
  children: ReactNode;
}) {
  const body = useRef<HTMLDivElement>(null);
  const bodyId = useId();
  const [overflows, setOverflows] = useState(false);

  // 测量不受外层裁切影响的完整正文；图片加载、代码高亮和窗口换行后重新判断。
  useLayoutEffect(() => {
    const element = body.current;
    if (!element) return;
    const measure = () => {
      setOverflows(element.getBoundingClientRect().height > collapsedMessageHeight);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  const collapsed = overflows && !expanded;
  return (
    <>
      <div
        id={bodyId}
        className={collapsed ? 'message-body-clip is-collapsed' : 'message-body-clip'}
        style={collapsed ? { maxHeight: collapsedMessageHeight } : undefined}
        onFocusCapture={(event) => {
          // 键盘移到被裁切的链接或附件时展开正文，让获得焦点的内容始终可见。
          const focused = event.target.getBoundingClientRect();
          const visible = event.currentTarget.getBoundingClientRect();
          if (collapsed && (focused.bottom > visible.bottom || focused.top < visible.top)) {
            onExpandedChange(true);
          }
        }}
      >
        <div className="message-body" ref={body}>
          {children}
        </div>
      </div>
      {overflows && (
        <div className="message-collapse-footer">
          <button
            type="button"
            className="message-collapse-toggle"
            aria-expanded={expanded}
            aria-controls={bodyId}
            onClick={() => onExpandedChange(!expanded)}
          >
            {expanded ? '收起' : '展开全文'}
            <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
              <path d="m6 9 6 6 6-6" stroke="currentColor" strokeWidth="1.8" />
            </svg>
          </button>
        </div>
      )}
    </>
  );
}
