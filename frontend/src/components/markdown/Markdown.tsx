// 本文件使用 Aether Start 同款 remark 管线渲染助手 Markdown，支持中文、公式、表格和可复制的高亮代码块。

import { memo, type ComponentProps } from 'react';
import { Fragment, jsx, jsxs } from 'react/jsx-runtime';
import { toJsxRuntime, type Components } from 'hast-util-to-jsx-runtime';
import type { Element } from 'hast';
import rehypeKatex from 'rehype-katex';
import rehypeSanitize from 'rehype-sanitize';
import remarkCjkFriendly from 'remark-cjk-friendly';
import remarkGfmStrikethroughCjkFriendly from 'remark-cjk-friendly-gfm-strikethrough';
import remarkGfm from 'remark-gfm';
import remarkMath from 'remark-math';
import remarkParse from 'remark-parse';
import remarkRehype from 'remark-rehype';
import { unified } from 'unified';
import { MarkdownCodeBlock } from './MarkdownCodeBlock.js';
import { safeLink } from '../../lib/platform.js';
import 'katex/dist/katex.min.css';
import './markdown.css';

// 整条消息一次解析，保留跨段落的列表、引用和引用式链接；清洗先于 KaTeX 生成可信公式节点。
const markdownProcessor = unified()
  .use(remarkParse)
  .use(remarkCjkFriendly)
  .use(remarkGfm)
  .use(remarkGfmStrikethroughCjkFriendly)
  .use(remarkMath, { singleDollarTextMath: true })
  .use(remarkRehype)
  .use(rehypeSanitize)
  .use(rehypeKatex);

// 链接沿用当前应用的 HTTP(S) 边界，拒绝带账号密码或可执行协议的地址。
function MarkdownLink({
  children,
  href,
  node: _node,
  ...props
}: ComponentProps<'a'> & { node?: unknown }) {
  const url = href && safeLink(href);
  return url ? (
    <a {...props} href={url} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  ) : (
    <span>{children}</span>
  );
}

// 图片与 Start 一样直接展示，限定 HTTP(S)，并避免向远端发送页面来源。
function MarkdownImage({ src, alt, title }: ComponentProps<'img'>) {
  const url = typeof src === 'string' && safeLink(src);
  return url ? (
    <img src={url} alt={alt ?? ''} title={title} loading="lazy" referrerPolicy="no-referrer" />
  ) : (
    <span className="muted">{alt || '图片链接'}</span>
  );
}

// 表格只在自己的容器内横向滚动，避免长单元格撑宽移动端消息。
function MarkdownTable({
  children,
  node: _node,
  ...props
}: ComponentProps<'table'> & { node?: unknown }) {
  return (
    <div className="markdown-table" tabIndex={0} role="region" aria-label="表格">
      <table {...props}>{children}</table>
    </div>
  );
}

// HAST 来自上方的解析管线，直接按其已确定的节点类型读取围栏语言和原始代码。
function MarkdownPre({ node, children }: ComponentProps<'pre'> & { node?: Element }) {
  const codeNode = node?.children[0];
  if (codeNode?.type !== 'element' || codeNode.tagName !== 'code') return <pre>{children}</pre>;
  const classes = codeNode.properties.className;
  const languageClass = Array.isArray(classes)
    ? classes.find((name) => typeof name === 'string' && name.startsWith('language-'))
    : undefined;
  const language = typeof languageClass === 'string' ? languageClass.slice('language-'.length) : '';
  const code = codeNode.children
    .map((child) => (child.type === 'text' ? child.value : ''))
    .join('')
    .replace(/\n$/, '');
  return <MarkdownCodeBlock code={code} language={language} />;
}

// 只替换需要交互或安全约束的元素，其余保留语义化原生标题、段落和列表。
const markdownComponents = {
  a: MarkdownLink,
  img: MarkdownImage,
  table: MarkdownTable,
  pre: MarkdownPre,
} satisfies Partial<Components>;

// 当前后端整条推送回复，直接使用最终 React 渲染，无需流式 DOM 动画或未闭合语法修补。
export const Markdown = memo(function Markdown({ text }: { text: string }) {
  const tree = markdownProcessor.runSync(markdownProcessor.parse(text));
  return (
    <div className="markdown">
      {toJsxRuntime(tree, { Fragment, jsx, jsxs, components: markdownComponents, passNode: true })}
    </div>
  );
});
