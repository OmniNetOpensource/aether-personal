// 本文件是本地设计画布的浏览器入口：带 scene 参数时独立挂载单个真实界面画板，否则挂载全景设计工作台。

import { createRoot } from 'react-dom/client';

document.documentElement.lang = 'zh-CN';

const rootElement = document.getElementById('root');
if (!rootElement) {
  throw new Error('缺少设计画布挂载节点 #root');
}

const searchParams = new URLSearchParams(location.search);
const sceneId = searchParams.get('scene');

// 按查询参数动态隔离代码分包与全局样式：画板分支加载聊天全屏固定视口样式，外层工作台分支只加载画布自身样式。
async function bootstrap(container: HTMLElement) {
  if (sceneId) {
    const { renderScene } = await import('./scenes.js');
    renderScene(container, sceneId, searchParams.get('theme'), window.top !== window.self);
    return;
  }

  const { DesignCanvas } = await import('./DesignCanvas.js');
  createRoot(container).render(<DesignCanvas />);
}

void bootstrap(rootElement);
