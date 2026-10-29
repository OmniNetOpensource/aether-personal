// 本文件是前端入口：把 React 应用挂载到页面根节点、引入全局样式，并同步可视视口尺寸供移动端布局使用。

import { createRoot } from 'react-dom/client';
import { App } from './App.js';
import './style.css';

const container = document.getElementById('root');
if (!container) throw new Error('Missing app root');
createRoot(container).render(<App />);

const resize = () => {
  const viewport = window.visualViewport;
  document.documentElement.style.setProperty(
    '--app-height',
    `${viewport?.height ?? innerHeight}px`,
  );
  document.documentElement.style.setProperty('--app-top', `${viewport?.offsetTop ?? 0}px`);
};
window.visualViewport?.addEventListener('resize', resize);
window.visualViewport?.addEventListener('scroll', resize);
window.addEventListener('resize', resize);
resize();
