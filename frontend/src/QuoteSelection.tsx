// 本文件负责选中助手原文后的引用入口，以及引用位置的记录与回看高亮；位置只依赖可见正文文本。

import { useEffect, useState } from 'react';
import type { RefObject } from 'react';
import type { MessageQuote } from './messageAttachments.js';

// quoteTextNodes 排除代码工具栏与公式隐藏文本，保证记录的是用户看见的原文。
function quoteTextNodes(root: Element): Text[] {
  const nodes: Text[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  let node = walker.nextNode();
  while (node) {
    if (
      node instanceof Text &&
      !node.parentElement?.closest('button, header, .katex-mathml, [hidden]')
    )
      nodes.push(node);
    node = walker.nextNode();
  }
  return nodes;
}

// QuoteSelection 只允许同一条助手回复内的选择；按钮固定在选区上边中点，不挤动消息布局。
export function QuoteSelection({
  container,
  onQuote,
}: {
  container: RefObject<HTMLDivElement | null>;
  onQuote: (quote: MessageQuote) => void;
}) {
  const [selection, setSelection] = useState<{
    quote: MessageQuote;
    left: number;
    top: number;
  } | null>(null);
  useEffect(() => {
    const update = () => {
      const selected = window.getSelection();
      if (!selected || selected.isCollapsed || selected.rangeCount === 0) return setSelection(null);
      const range = selected.getRangeAt(0);
      const startElement =
        range.startContainer instanceof Element
          ? range.startContainer
          : range.startContainer.parentElement;
      const endElement =
        range.endContainer instanceof Element
          ? range.endContainer
          : range.endContainer.parentElement;
      const body = startElement?.closest('[data-quote-body]');
      if (
        !body ||
        body !== endElement?.closest('[data-quote-body]') ||
        !container.current?.contains(body)
      )
        return setSelection(null);
      const messageId = body.getAttribute('data-message-id');
      if (!messageId) return setSelection(null);
      const nodes = quoteTextNodes(body);
      let offset = 0,
        from: number | undefined,
        to = 0;
      for (const node of nodes) {
        if (range.intersectsNode(node)) {
          const start = range.startContainer === node ? range.startOffset : 0;
          const end = range.endContainer === node ? range.endOffset : node.length;
          if (end > start) {
            from ??= offset + start;
            to = offset + end;
          }
        }
        offset += node.length;
      }
      if (from === undefined) return setSelection(null);
      const text = nodes
        .map((node) => node.data)
        .join('')
        .slice(from, to);
      if (!text.trim()) return setSelection(null);
      // 锚点是选区外接矩形的上边中点；样式再把按钮水平居中并抬到选区上方。
      const rect = range.getBoundingClientRect();
      const center = rect.left + rect.width / 2;
      const left = Math.round(Math.min(window.innerWidth - 48, Math.max(48, center)));
      const top = Math.round(Math.max(80, rect.top));
      setSelection((current) =>
        current &&
        current.left === left &&
        current.top === top &&
        current.quote.message_id === messageId &&
        current.quote.start === from &&
        current.quote.end === to &&
        current.quote.text === text
          ? current
          : { quote: { text, message_id: messageId, start: from, end: to }, left, top },
      );
    };
    // 滚动和窗口尺寸变化时重算锚点，按钮跟着选区移动，不因滚动收起。
    let frame = 0;
    const reposition = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(update);
    };
    document.addEventListener('selectionchange', update);
    window.addEventListener('resize', reposition);
    const element = container.current;
    element?.addEventListener('scroll', reposition, { passive: true });
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener('selectionchange', update);
      window.removeEventListener('resize', reposition);
      element?.removeEventListener('scroll', reposition);
    };
  }, [container]);
  if (!selection) return null;
  return (
    <button
      type="button"
      className="quote-toolbar"
      style={{ left: selection.left, top: selection.top }}
      onPointerDown={(event) => event.preventDefault()}
      onClick={() => {
        onQuote(selection.quote);
        window.getSelection()?.removeAllRanges();
        setSelection(null);
      }}
    >
      引用
    </button>
  );
}

// highlightQuote 按引用范围定位长消息中的原文；不支持文本高亮时仍滚动到对应文字并提示整条消息。
export function highlightQuote(
  body: HTMLElement,
  quote: MessageQuote,
  viewport: HTMLElement,
): () => void {
  const nodes = quoteTextNodes(body);
  body.classList.add('quote-target');
  const ranges: Range[] = [];
  if (
    nodes
      .map((node) => node.data)
      .join('')
      .slice(quote.start, quote.end) === quote.text
  ) {
    let offset = 0;
    for (const node of nodes) {
      const start = Math.max(0, quote.start - offset),
        end = Math.min(node.length, quote.end - offset);
      if (end > start) {
        const range = document.createRange();
        range.setStart(node, start);
        range.setEnd(node, end);
        ranges.push(range);
      }
      offset += node.length;
    }
  }
  if ('highlights' in CSS && typeof Highlight !== 'undefined') {
    CSS.highlights.set('quote-source', new Highlight(...ranges));
  }
  // 按引用首段的矩形滚动，避免很长的正文按整条消息居中后，引用仍在屏幕外。
  const target = ranges[0]?.getBoundingClientRect() ?? body.getBoundingClientRect();
  viewport.scrollBy({
    top:
      target.top -
      viewport.getBoundingClientRect().top -
      (viewport.clientHeight - target.height) / 2,
    behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth',
  });
  return () => {
    body.classList.remove('quote-target');
    if ('highlights' in CSS) CSS.highlights.delete('quote-source');
  };
}
