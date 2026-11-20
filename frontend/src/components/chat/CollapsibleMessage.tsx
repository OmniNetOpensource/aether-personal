// 本文件为用户和助手消息提供按实际高度平滑折叠与展开的正文容器，保留完整内容与引用位置。

import { useId, useLayoutEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';

// 折叠阈值按渲染高度计算，文字换行、代码块和图片都计入正文。
const collapsedMessageHeight = 360;
const collapseAnimationDurationMs = 240;

// CollapsibleMessage 负责正文测量与平滑高度过渡；展开状态由列表按消息 ID 保存，避免虚拟行卸载后丢失。
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
  const animatedHeight = useRef<number | null>(null);
  const animationFrame = useRef(0);
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

  // 用户切换展开或收起时，用 ease-out 曲线驱动高度过渡；收起已滚过头部的长消息时同帧闭环锚定滚动位置。
  useLayoutEffect(() => {
    const wasExpanded = previousExpanded.current;
    previousExpanded.current = expanded;
    const clipElement = clip.current;
    const bodyElement = body.current;
    if (!clipElement || !bodyElement) return;

    const applyStaticStyle = (isExpanded: boolean) => {
      animatedHeight.current = null;
      clipElement.style.height = '';
      clipElement.style.maxHeight = isExpanded ? '' : `${collapsedMessageHeight}px`;
      clipElement.style.overflow = isExpanded ? '' : 'clip';
    };

    if (wasExpanded === expanded) {
      if (animationFrame.current === 0) applyStaticStyle(expanded);
      return;
    }

    cancelAnimationFrame(animationFrame.current);
    animationFrame.current = 0;

    const fullHeight = bodyElement.getBoundingClientRect().height;
    if (fullHeight <= collapsedMessageHeight) {
      applyStaticStyle(expanded);
      return;
    }

    const row =
      clipElement.closest<HTMLElement>(
        '[data-chat-row], [data-history-message], [data-shared-message]',
      ) ?? clipElement;
    const viewport = clipElement.closest<HTMLElement>('[data-virtuoso-scroller], .shared-viewport');
    const scrollContainerBy = (top: number) => {
      if (scrollByRef.current) scrollByRef.current(top);
      else if (viewport) viewport.scrollTop += top;
    };

    const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)').matches;
    if (reducedMotion) {
      const initialRowTop =
        !expanded && viewport
          ? row.getBoundingClientRect().top - viewport.getBoundingClientRect().top
          : 0;
      applyStaticStyle(expanded);
      if (!expanded && viewport && initialRowTop < 16) {
        const currentRowTop =
          row.getBoundingClientRect().top - viewport.getBoundingClientRect().top;
        scrollContainerBy(currentRowTop - 16);
      }
      return;
    }

    const startHeight =
      animatedHeight.current ??
      (expanded ? collapsedMessageHeight : Math.max(fullHeight, collapsedMessageHeight));
    const endHeight = expanded
      ? Math.min(fullHeight, startHeight + window.innerHeight)
      : collapsedMessageHeight;

    if (Math.abs(endHeight - startHeight) < 1) {
      applyStaticStyle(expanded);
      return;
    }

    const viewportRect = viewport?.getBoundingClientRect();
    const rowRect = row.getBoundingClientRect();
    const initialRowTop = viewportRect ? rowRect.top - viewportRect.top : 0;
    const initialRowBottom = viewportRect ? rowRect.bottom - viewportRect.top : 0;
    const shrinkTotal = Math.max(0, startHeight - endHeight);
    const finalRowHeight = rowRect.height - shrinkTotal;
    let anchorScroll = !expanded && Boolean(viewport) && initialRowTop < 16;
    const desiredFinalRowTop = viewportRect
      ? Math.max(
          16,
          Math.min(viewportRect.height - finalRowHeight - 16, initialRowBottom - finalRowHeight),
        )
      : 16;

    const cancelScrollAnchor = () => {
      anchorScroll = false;
    };
    if (anchorScroll && viewport) {
      viewport.addEventListener('wheel', cancelScrollAnchor, { passive: true });
      viewport.addEventListener('touchmove', cancelScrollAnchor, { passive: true });
    }

    clipElement.style.maxHeight = 'none';
    clipElement.style.overflow = 'clip';
    clipElement.style.height = `${startHeight}px`;
    animatedHeight.current = startHeight;

    const startTime = performance.now();
    const step = (now: number) => {
      const progress = Math.min(1, (now - startTime) / collapseAnimationDurationMs);
      const eased = 1 - Math.pow(1 - progress, 3);
      const nextHeight = startHeight + (endHeight - startHeight) * eased;
      if (progress < 1) {
        animatedHeight.current = nextHeight;
        clipElement.style.height = `${nextHeight}px`;
      } else {
        applyStaticStyle(expanded);
      }
      if (anchorScroll && viewport) {
        const targetRowTop = initialRowTop + (desiredFinalRowTop - initialRowTop) * eased;
        const actualRowTop = row.getBoundingClientRect().top - viewport.getBoundingClientRect().top;
        const delta = actualRowTop - targetRowTop;
        if (Math.abs(delta) >= 0.5) scrollContainerBy(delta);
      }
      if (progress < 1) {
        animationFrame.current = requestAnimationFrame(step);
      } else {
        animationFrame.current = 0;
        viewport?.removeEventListener('wheel', cancelScrollAnchor);
        viewport?.removeEventListener('touchmove', cancelScrollAnchor);
      }
    };

    animationFrame.current = requestAnimationFrame(step);
    return () => {
      if (animationFrame.current !== 0) {
        cancelAnimationFrame(animationFrame.current);
        animationFrame.current = 0;
      }
      viewport?.removeEventListener('wheel', cancelScrollAnchor);
      viewport?.removeEventListener('touchmove', cancelScrollAnchor);
    };
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
