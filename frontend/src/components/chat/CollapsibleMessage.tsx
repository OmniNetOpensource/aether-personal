// 本文件为用户和助手消息提供按实际高度折叠与展开的正文容器，保留完整内容、引用位置与收起滚动锚定。

import { useId, useLayoutEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';

// 折叠阈值按渲染高度计算，文字换行、代码块和图片都计入正文。
const collapsedMessageHeight = 360;

// CollapsibleMessage 负责正文测量与折叠切换；展开状态由列表按消息 ID 保存，避免虚拟行卸载后丢失。
export function CollapsibleMessage({
  expanded,
  onExpandedChange,
  onScrollBy,
  children,
}: {
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
  onScrollBy?: (top: number) => void;
  children: ReactNode;
}) {
  const clip = useRef<HTMLDivElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const bodyId = useId();
  const [overflows, setOverflows] = useState(false);
  const previousExpanded = useRef(expanded);
  const scrollByRef = useRef(onScrollBy);
  scrollByRef.current = onScrollBy;

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

  // 收起已滚过头部的长消息时，将消息顶部拉回视口上方留白处，避免收起后失去阅读位置。
  useLayoutEffect(() => {
    const wasExpanded = previousExpanded.current;
    previousExpanded.current = expanded;
    if (!wasExpanded || expanded || !overflows) return;
    const clipElement = clip.current;
    if (!clipElement) return;
    const row =
      clipElement.closest<HTMLElement>('[data-chat-row], [data-history-message]') ?? clipElement;
    const viewport = clipElement.closest<HTMLElement>(
      '[data-virtuoso-scroller], .history-context-viewport',
    );
    if (!viewport) return;
    const rowTop = row.getBoundingClientRect().top - viewport.getBoundingClientRect().top;
    if (rowTop < 16) {
      if (scrollByRef.current) scrollByRef.current(rowTop - 16);
      else viewport.scrollTop += rowTop - 16;
    }
  }, [expanded, overflows]);

  // 首次挂载就限制未展开的高度，避免虚拟列表先量全文、再因折叠反复移动与重挂载。
  const collapsed = overflows && !expanded;
  return (
    <>
      <div
        id={bodyId}
        ref={clip}
        className={collapsed ? 'message-body-clip is-collapsed' : 'message-body-clip'}
        style={!expanded ? { maxHeight: collapsedMessageHeight, overflow: 'clip' } : undefined}
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
