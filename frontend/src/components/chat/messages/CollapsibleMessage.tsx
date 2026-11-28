// 本文件为用户和助手消息提供按实际高度折叠与展开的正文容器，保留完整内容、引用位置与普通滚动容器的收起按钮锚定。

import { useId, useLayoutEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';

// 折叠阈值按渲染高度计算，文字换行、代码块和图片都计入正文。
const collapsedMessageHeight = 360;

// CollapsibleMessage 负责正文测量与折叠切换；展开状态由列表按消息 ID 保存，避免虚拟行卸载后丢失。
export function CollapsibleMessage({
  expanded,
  onExpandedChange,
  children,
}: {
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
  children: ReactNode;
}) {
  const clip = useRef<HTMLDivElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const toggleButton = useRef<HTMLButtonElement>(null);
  const collapseButtonTop = useRef<number | null>(null);
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

  // 历史上下文窗口使用普通滚动容器：收起后按点击前按钮距视口顶部的位置补齐 scrollTop，让按钮和下方内容停在原位。
  // 主聊天列表的展开与收起锚定由 ChatMessageList 里的 TanStack Virtual 同帧处理，不在这里重复改 scrollTop。
  useLayoutEffect(() => {
    const previousButtonTop = collapseButtonTop.current;
    collapseButtonTop.current = null;
    if (expanded || previousButtonTop === null) return;
    const button = toggleButton.current;
    const viewport = clip.current?.closest<HTMLElement>('.history-context-viewport');
    if (!button || !viewport) return;
    const currentButtonTop =
      button.getBoundingClientRect().top - viewport.getBoundingClientRect().top;
    const delta = currentButtonTop - previousButtonTop;
    if (Math.abs(delta) >= 0.5) viewport.scrollTop += delta;
  }, [expanded]);

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
            ref={toggleButton}
            type="button"
            className="message-collapse-toggle"
            aria-expanded={expanded}
            aria-controls={bodyId}
            onClick={() => {
              if (expanded) {
                const viewport = clip.current?.closest<HTMLElement>('.history-context-viewport');
                const button = toggleButton.current;
                if (viewport && button) {
                  collapseButtonTop.current =
                    button.getBoundingClientRect().top - viewport.getBoundingClientRect().top;
                }
              }
              onExpandedChange(!expanded);
            }}
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
