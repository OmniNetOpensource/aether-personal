import { marked } from 'marked';
import { decodeHTML } from 'entities';
import type { Token, Tokens } from 'marked';
import type { ReactNode } from '@lynx-js/react';
import { host } from './platform.js';

function inline(tokens: Token[]): ReactNode {
  return tokens.map((token, index) => {
    switch (token.type) {
      case 'strong':
        return (
          <text key={index} className="strong">
            {inline(token.tokens ?? [])}
          </text>
        );
      case 'em':
        return (
          <text key={index} className="em">
            {inline(token.tokens ?? [])}
          </text>
        );
      case 'del':
        return (
          <text key={index} className="deleted">
            {inline(token.tokens ?? [])}
          </text>
        );
      case 'codespan':
        return (
          <text key={index} className="code-inline">
            {token.text}
          </text>
        );
      case 'link':
        return (
          <text key={index} className="link" bindtap={() => host.openLink(token.href)}>
            {inline(token.tokens ?? [])}
          </text>
        );
      case 'image':
        return (
          <text key={index} className="muted">
            {token.text || '图片链接'}
          </text>
        );
      case 'br':
        return '\n';
      case 'escape':
        return decodeHTML(token.text);
      case 'html':
        return token.raw;
      default:
        return 'tokens' in token && token.tokens
          ? inline(token.tokens ?? [])
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
          <text key={index} className={`md-block heading h${token.depth}`}>
            {inline(token.tokens ?? [])}
          </text>
        );
      case 'code':
        return (
          <scroll-view key={index} scroll-orientation="horizontal" className="code-block">
            <text className="code-text">{token.text}</text>
          </scroll-view>
        );
      case 'blockquote':
        return (
          <view key={index} className="quote">
            {blocks(token.tokens ?? [])}
          </view>
        );
      case 'list':
        return (
          <view key={index} className="md-block">
            {token.items.map((item: Tokens.ListItem, itemIndex: number) => (
              <view key={itemIndex} className="list-row">
                <text className="list-marker">
                  {item.task
                    ? item.checked
                      ? '☑'
                      : '☐'
                    : token.ordered
                      ? `${Number(token.start) + itemIndex}.`
                      : '•'}
                </text>
                <view className="list-body">{blocks(item.tokens)}</view>
              </view>
            ))}
          </view>
        );
      case 'table':
        return (
          <scroll-view key={index} scroll-orientation="horizontal" className="md-block">
            <view className="table">
              <view className="table-row">
                {token.header.map((cell: Tokens.TableCell, i: number) => (
                  <text key={i} className="table-cell strong">
                    {inline(cell.tokens)}
                  </text>
                ))}
              </view>
              {token.rows.map((row: Tokens.TableCell[], i: number) => (
                <view key={i} className="table-row">
                  {row.map((cell, j) => (
                    <text key={j} className="table-cell">
                      {inline(cell.tokens)}
                    </text>
                  ))}
                </view>
              ))}
            </view>
          </scroll-view>
        );
      case 'hr':
        return <view key={index} className="rule" />;
      default:
        return (
          <text key={index} className="md-block">
            {'tokens' in token && token.tokens
              ? inline(token.tokens ?? [])
              : 'text' in token
                ? decodeHTML(token.text)
                : token.raw}
          </text>
        );
    }
  });
}

export function Markdown({ text }: { text: string }) {
  return (
    <view className="markdown">{blocks(marked.lexer(text, { gfm: true, breaks: true }))}</view>
  );
}
