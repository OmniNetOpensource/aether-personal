// 本文件按需加载 Aether Start 同款 Shiki 语言包，避免将完整语言目录放进聊天首屏。

import { createBundledHighlighter, createSingletonShorthands } from '@shikijs/core';
import { createJavaScriptRegexEngine } from '@shikijs/engine-javascript';

// 常用聊天语言和别名共用同一个异步加载器，未知语言由代码块显示原文。
const bundledLanguagesInfo = [
  {
    id: 'javascript',
    name: 'JavaScript',
    aliases: ['js', 'cjs', 'mjs'],
    import: () => import('@shikijs/langs/javascript'),
  },
  {
    id: 'typescript',
    name: 'TypeScript',
    aliases: ['ts', 'cts', 'mts'],
    import: () => import('@shikijs/langs/typescript'),
  },
  { id: 'jsx', name: 'JSX', import: () => import('@shikijs/langs/jsx') },
  { id: 'tsx', name: 'TSX', import: () => import('@shikijs/langs/tsx') },
  { id: 'html', name: 'HTML', import: () => import('@shikijs/langs/html') },
  { id: 'css', name: 'CSS', import: () => import('@shikijs/langs/css') },
  { id: 'json', name: 'JSON', import: () => import('@shikijs/langs/json') },
  { id: 'yaml', name: 'YAML', aliases: ['yml'], import: () => import('@shikijs/langs/yaml') },
  {
    id: 'markdown',
    name: 'Markdown',
    aliases: ['md'],
    import: () => import('@shikijs/langs/markdown'),
  },
  {
    id: 'shellscript',
    name: 'Shell',
    aliases: ['bash', 'sh', 'shell', 'zsh'],
    import: () => import('@shikijs/langs/shellscript'),
  },
  { id: 'python', name: 'Python', aliases: ['py'], import: () => import('@shikijs/langs/python') },
  { id: 'sql', name: 'SQL', import: () => import('@shikijs/langs/sql') },
  { id: 'go', name: 'Go', import: () => import('@shikijs/langs/go') },
  { id: 'rust', name: 'Rust', aliases: ['rs'], import: () => import('@shikijs/langs/rust') },
  { id: 'java', name: 'Java', import: () => import('@shikijs/langs/java') },
  { id: 'c', name: 'C', import: () => import('@shikijs/langs/c') },
  { id: 'cpp', name: 'C++', aliases: ['c++'], import: () => import('@shikijs/langs/cpp') },
  {
    id: 'csharp',
    name: 'C#',
    aliases: ['c#', 'cs'],
    import: () => import('@shikijs/langs/csharp'),
  },
  { id: 'php', name: 'PHP', import: () => import('@shikijs/langs/php') },
];

// 合并标准语言名与别名，让围栏中的 js、py 等写法使用对应语法。
const bundledLanguagesBase = Object.fromEntries(bundledLanguagesInfo.map((i) => [i.id, i.import]));
const bundledLanguagesAlias = Object.fromEntries(
  bundledLanguagesInfo.flatMap((i) => i.aliases?.map((a) => [a, i.import]) ?? []),
);
const bundledLanguages = { ...bundledLanguagesBase, ...bundledLanguagesAlias };

const bundledThemes = {
  'github-light': () => import('@shikijs/themes/github-light'),
};

// JavaScript 正则引擎无需额外 WASM 文件，也可随 PWA 静态资源离线使用。
const createHighlighter = createBundledHighlighter({
  langs: bundledLanguages,
  themes: bundledThemes,
  engine: () => createJavaScriptRegexEngine({ forgiving: true }),
});

const { codeToTokens } = createSingletonShorthands(createHighlighter);

export { bundledLanguagesInfo, bundledLanguages, codeToTokens, createHighlighter };
