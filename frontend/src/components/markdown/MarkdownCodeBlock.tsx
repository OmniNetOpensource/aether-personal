// 本文件渲染带语言标签、复制反馈、语法高亮和行号的代码块，使用 GPU Lexer 着色并缓存结果以避免滚动重挂载跳动。

import { useEffect, useRef, useState } from 'react';
import { getRouteApi } from '@tanstack/react-router';
import type { parse } from 'gpu-lexer';
import { createLruCache } from './cache.js';

// 模型返回 UTF-16 源码区间，逐行保存文本和类别供行号与主题共用。
type SyntaxSpan = Awaited<ReturnType<typeof parse>>[number];
type CodeLine = { content: string; type: SyntaxSpan['type'] }[];

// 重挂载时按源码复用结果，模型不依赖语言标签。
const tokenCache = createLruCache<CodeLine[]>(100);
const rootRoute = getRouteApi('__root__');

// 代码块先展示可复制的原文，WebGPU 完成推理后再着色。
export function MarkdownCodeBlock({ code, language }: { code: string; language: string }) {
  const { parseCode } = rootRoute.useRouteContext();
  const plainText = ['text', 'txt', 'plaintext'].includes(language.toLowerCase());
  const [highlight, setHighlight] = useState<{ code: string; lines: CodeLine[] }>();
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle');
  const copyTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const result = plainText
    ? undefined
    : highlight?.code === code
      ? highlight.lines
      : tokenCache.get(code);

  // 跨行区间按原文换行拆开，连续换行与末尾空行也各占一行。
  useEffect(() => {
    if (plainText || !code || tokenCache.get(code)) return;
    let active = true;
    void parseCode(code)
      .then((spans) => {
        let line: CodeLine = [];
        const lines = [line];
        for (const span of spans) {
          const pieces = code.slice(span.start, span.end).split('\n');
          for (const [index, content] of pieces.entries()) {
            if (index > 0) {
              line = [];
              lines.push(line);
            }
            if (content) line.push({ content, type: span.type });
          }
        }
        tokenCache.set(code, lines);
        if (active) setHighlight({ code, lines });
      })
      .catch(() => {
        /* WebGPU 不可用或推理失败时继续显示原文。 */
      });
    return () => {
      active = false;
    };
  }, [code, plainText, parseCode]);

  // 卸载或内容变化时清除复制反馈计时器，避免回调写入已经移除的代码块。
  useEffect(() => {
    setCopyState('idle');
    return () => {
      if (copyTimer.current !== undefined) clearTimeout(copyTimer.current);
    };
  }, [code]);

  // 只有实际写入剪贴板成功后才显示“已复制”，两秒后恢复入口。
  async function copyCode() {
    try {
      await navigator.clipboard.writeText(code);
      setCopyState('copied');
    } catch {
      setCopyState('failed');
    }
    if (copyTimer.current !== undefined) clearTimeout(copyTimer.current);
    copyTimer.current = setTimeout(() => setCopyState('idle'), 2000);
  }

  const fallbackLines = result ? undefined : code.split('\n');
  return (
    <section className="markdown-code" data-language={language}>
      <header>
        <span>{language || 'text'}</span>
        <button type="button" onClick={() => void copyCode()} aria-label="复制代码">
          <span aria-live="polite">
            {copyState === 'copied' ? '已复制' : copyState === 'failed' ? '复制失败，重试' : '复制'}
          </span>
        </button>
      </header>
      <pre tabIndex={0}>
        <code>
          {result
            ? result.map((line, lineIndex) => (
                <span className="markdown-code-line" key={lineIndex}>
                  {line.map((token, tokenIndex) => (
                    <span key={tokenIndex} data-syntax={token.type}>
                      {token.content}
                    </span>
                  ))}
                  {line.length === 0 ? '\n' : null}
                </span>
              ))
            : fallbackLines?.map((line, lineIndex) => (
                <span className="markdown-code-line" key={lineIndex}>
                  {line || '\n'}
                </span>
              ))}
        </code>
      </pre>
    </section>
  );
}
