// 本文件是 Markdown 渲染组件：把助手消息文本解析为安全的 React 元素，只放行 http/https 链接。

import { marked } from 'marked';
import { decodeHTML } from 'entities';
import type { Token, Tokens } from 'marked';
import type { ReactNode } from 'react';
import { memo } from 'react';
import { safeLink } from './platform.js';

function inline(tokens: Token[]): ReactNode {
  return tokens.map((token, index) => {
    switch (token.type) {
      case 'strong':
        return <strong key={index}>{inline(token.tokens ?? [])}</strong>;
      case 'em':
        return <em key={index}>{inline(token.tokens ?? [])}</em>;
      case 'del':
        return <del key={index}>{inline(token.tokens ?? [])}</del>;
      case 'codespan':
        return (
          <code key={index} className="code-inline">
            {token.text}
          </code>
        );
      case 'link': {
        const href = safeLink(token.href);
        return href ? (
          <a key={index} className="link" href={href} target="_blank" rel="noopener noreferrer">
            {inline(token.tokens ?? [])}
          </a>
        ) : (
          <span key={index}>{inline(token.tokens ?? [])}</span>
        );
      }
      case 'image':
        return (
          <span key={index} className="muted">
            {token.text || '图片链接'}
          </span>
        );
      case 'br':
        return <br key={index} />;
      case 'escape':
        return decodeHTML(token.text);
      case 'html':
        return token.raw;
      default:
        return 'tokens' in token && token.tokens
          ? inline(token.tokens)
          : 'text' in token
            ? decodeHTML(token.text)
            : token.raw;
    }
  });
}

function blocks(tokens: Token[]): ReactNode {
  return tokens.map((token, index) => {
    switch (token.type) {
      case 'space':
        return null;
      case 'heading':
        return (
          <div
            key={index}
            role="heading"
            aria-level={token.depth}
            className={`md-block heading h${token.depth}`}
          >
            {inline(token.tokens ?? [])}
          </div>
        );
      case 'code':
        return (
          <pre key={index} className="code-block" tabIndex={0}>
            <code className="code-text">{token.text}</code>
          </pre>
        );
      case 'blockquote':
        return (
          <blockquote key={index} className="quote">
            {blocks(token.tokens ?? [])}
          </blockquote>
        );
      case 'list':
        return (
          <div key={index} className="md-block" role="list">
            {token.items.map((item: Tokens.ListItem, i: number) => (
              <div key={i} className="list-row" role="listitem">
                <span className="list-marker">
                  {item.task
                    ? item.checked
                      ? '☑'
                      : '☐'
                    : token.ordered
                      ? `${Number(token.start) + i}.`
                      : '•'}
                </span>
                <div className="list-body">{blocks(item.tokens)}</div>
              </div>
            ))}
          </div>
        );
      case 'table':
        return (
          <div key={index} className="table-scroll" tabIndex={0} role="region" aria-label="表格">
            <table>
              <thead>
                <tr>
                  {token.header.map((cell: Tokens.TableCell, i: number) => (
                    <th key={i} style={{ textAlign: token.align[i] ?? undefined }}>
                      {inline(cell.tokens)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {token.rows.map((row: Tokens.TableCell[], i: number) => (
                  <tr key={i}>
                    {row.map((cell, j) => (
                      <td key={j} style={{ textAlign: token.align[j] ?? undefined }}>
                        {inline(cell.tokens)}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        );
      case 'hr':
        return <hr key={index} className="rule" />;
      default:
        return (
          <div key={index} className="md-block">
            {'tokens' in token && token.tokens
              ? inline(token.tokens)
              : 'text' in token
                ? decodeHTML(token.text)
                : token.raw}
          </div>
        );
    }
  });
}

export const Markdown = memo(function Markdown({ text }: { text: string }) {
  return <div className="markdown">{blocks(marked.lexer(text, { gfm: true, breaks: true }))}</div>;
});
