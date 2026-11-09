// 本文件渲染带语言标签、复制反馈、语法高亮和行号的代码块，按需加载语法以保持首屏轻量。

import { useEffect, useRef, useState } from 'react';
import type { TokensResult } from '@shikijs/core';

// 高亮结果携带输入标识，内容更新时先显示新原文，避免短暂显示旧代码。
type Highlight = { key: string; result: TokensResult };

// 代码复制由浏览器剪贴板完成；失败反馈留在按钮内，允许用户直接重试。
export function MarkdownCodeBlock({ code, language }: { code: string; language: string }) {
  const [highlight, setHighlight] = useState<Highlight>();
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle');
  const copyTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const key = `${language}\0${code}`;
  const result = highlight?.key === key ? highlight.result : undefined;

  // 一次生成深浅两套高亮变量，系统切换外观时由 CSS 即时切换，无需重新解析代码。
  // 加载失败或未知语言仍保留可读、可复制的代码原文。
  useEffect(() => {
    let active = true;
    void import('./shiki.js')
      .then(async ({ bundledLanguages, codeToTokens }) => {
        if (language && !Object.hasOwn(bundledLanguages, language)) return;
        const tokens = await codeToTokens(code, {
          lang: language || 'text',
          themes: { light: 'github-light', dark: 'github-dark' },
          defaultColor: false,
        });
        if (active) setHighlight({ key, result: tokens });
      })
      .catch(() => {
        /* 高亮是增强功能，代码原文已显示。 */
      });
    return () => {
      active = false;
    };
  }, [code, language, key]);

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
            ? result.tokens.map((line, lineIndex) => (
                <span className="markdown-code-line" key={lineIndex}>
                  {line.map((token, tokenIndex) => (
                    <span key={tokenIndex} style={token.htmlStyle}>
                      {token.content}
                    </span>
                  ))}
                  {line.length === 0 ? '\n' : null}
                </span>
              ))
            : code}
        </code>
      </pre>
    </section>
  );
}
