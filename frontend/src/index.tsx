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
