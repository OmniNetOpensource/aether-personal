import '@lynx-js/web-core/client';
import type { LynxViewElement } from '@lynx-js/web-core/client';
import { z } from 'zod';
import './style.css';
import { openDesktop } from './desktop.js';

declare global {
  interface HTMLElementTagNameMap {
    'lynx-view': LynxViewElement;
  }
}
const lynxView = document.createElement('lynx-view');
const sockets = new Map<number, WebSocket>();
const sessionSchema = z.object({ token: z.string(), email: z.string() });
const socketSchema = z.object({ id: z.number(), url: z.url(), protocols: z.array(z.string()) });
const sendSchema = z.object({ id: z.number(), message: z.string() });
const closeSchema = z.object({ id: z.number(), code: z.number(), reason: z.string() });
const baseURL = (process.env.PUBLIC_API_BASE_URL || location.origin).replace(/\/$/, '');
let closeDesktop: (() => void) | undefined;

lynxView.nativeModulesMap = {
  AetherHost: '/native-modules.js',
  LynxWebSocketModule: '/native-modules.js',
};
lynxView.onNativeModulesCall = (name: string, value: unknown) => {
  switch (name) {
    case 'session': {
      let token: string | null = null;
      let email = '';
      try {
        const readString = (key: string) => {
          const stored = localStorage.getItem(key);
          if (stored === null) return null;
          if (stored.startsWith('"')) return z.string().parse(JSON.parse(stored));
          return stored;
        };
        // SharedPreferencesAsync JSON-encodes unprefixed values; older Flutter uses flutter.*.
        token = readString('aether_session') ?? readString('flutter.aether_session');
        email = readString('aether_email') ?? readString('flutter.aether_email') ?? '';
        if (token) {
          localStorage.setItem('aether_session', token);
          localStorage.setItem('aether_email', email);
        }
      } catch {
        /* Login remains available when browser storage is unavailable. */
      }
      return { baseURL, token, email };
    }
    case 'saveSession': {
      const session = sessionSchema.parse(value);
      closeDesktop?.();
      try {
        localStorage.setItem('aether_session', session.token);
        localStorage.setItem('aether_email', session.email);
        return true;
      } catch {
        return false;
      }
    }
    case 'clearSession':
      closeDesktop?.();
      try {
        localStorage.removeItem('aether_session');
        localStorage.removeItem('flutter.aether_session');
      } catch {
        /* Storage can be disabled. */
      }
      return;
    case 'openDesktop': {
      const { token } = z.object({ token: z.string().min(1).max(128) }).parse(value);
      closeDesktop?.();
      closeDesktop = openDesktop(baseURL, token);
      return;
    }
    case 'uuid':
      return crypto.randomUUID();
    case 'resizeComposer':
      resizeComposer();
      return;
    case 'openLink': {
      const { url } = z.object({ url: z.url() }).parse(value);
      const target = new URL(url);
      if (['https:', 'http:'].includes(target.protocol) && !target.username && !target.password)
        window.open(target.href, '_blank', 'noopener,noreferrer');
      return;
    }
    case 'connect': {
      const { id, url, protocols } = socketSchema.parse(value);
      const socket = new WebSocket(url, protocols);
      sockets.set(id, socket);
      socket.onopen = () =>
        lynxView.sendGlobalEvent('websocketOpen', [{ id, protocol: socket.protocol }]);
      socket.onmessage = (event) => {
        if (typeof event.data === 'string')
          lynxView.sendGlobalEvent('websocketMessage', [{ id, type: 'text', data: event.data }]);
      };
      socket.onclose = (event) => {
        sockets.delete(id);
        lynxView.sendGlobalEvent('websocketClosed', [
          { id, code: event.code, reason: event.reason, wasClean: event.wasClean },
        ]);
      };
      socket.onerror = () =>
        lynxView.sendGlobalEvent('websocketFailed', [{ id, message: '连接失败' }]);
      return;
    }
    case 'send': {
      const { id, message } = sendSchema.parse(value);
      sockets.get(id)?.send(message);
      return;
    }
    case 'close': {
      const { id, code, reason } = closeSchema.parse(value);
      sockets.get(id)?.close(code, reason);
      return;
    }
    default:
      throw new Error(`Unknown host method: ${name}`);
  }
};
lynxView.url = '/main.web.bundle';
lynxView.injectStyleRules = [
  'x-text { user-select: text; overflow-wrap: anywhere; }',
  'x-textarea::part(textarea), x-input::part(input) { font-family: inherit; box-sizing: border-box !important; }',
  '.message-input::part(textarea) { resize: none; }',
  '@media (max-width: 699px) { .message-content { padding-left:16px !important; padding-right:16px !important; } .composer-padding { padding-left:14px !important; padding-right:14px !important; padding-bottom:max(14px, env(safe-area-inset-bottom)) !important; } }',
  '@media (prefers-reduced-motion: reduce) { .dot { animation:none !important; } }',
];
document.body.append(lynxView);

// Keyboard and viewport APIs belong to the browser host, outside the Lynx worker.
lynxView.addEventListener(
  'keydown',
  (event) => {
    if (
      !(event instanceof KeyboardEvent) ||
      event.key !== 'Enter' ||
      event.shiftKey ||
      event.ctrlKey ||
      event.metaKey ||
      event.isComposing ||
      event.keyCode === 229
    )
      return;
    const input = event
      .composedPath()
      .find(
        (target) => target instanceof HTMLTextAreaElement || target instanceof HTMLInputElement,
      );
    if (!input) return;
    event.preventDefault();
    lynxView.sendGlobalEvent('aetherSubmit', []);
  },
  true,
);
const resize = () => {
  const viewport = window.visualViewport;
  lynxView.style.height = `${viewport?.height ?? window.innerHeight}px`;
  lynxView.style.top = `${viewport?.offsetTop ?? 0}px`;
  resizeComposer();
  lynxView.sendGlobalEvent('aetherResize', []);
};
window.visualViewport?.addEventListener('resize', resize);
window.visualViewport?.addEventListener('scroll', resize);
window.addEventListener('resize', resize);
window.addEventListener('offline', () => sockets.forEach((socket) => socket.close()));
window.addEventListener('online', () => lynxView.sendGlobalEvent('aetherOnline', []));
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) lynxView.sendGlobalEvent('aetherOnline', []);
});
resize();
window.addEventListener('pagehide', () => sockets.forEach((socket) => socket.close()));

function resizeComposer() {
  const element = lynxView.shadowRoot?.querySelector('#message-input');
  const input = element?.shadowRoot?.querySelector('textarea');
  if (!(element instanceof HTMLElement) || !(input instanceof HTMLTextAreaElement)) return;
  element.style.height = '44px';
  input.style.height = '0px';
  const height = Math.max(44, Math.min(input.scrollHeight, 164));
  element.style.height = `${height}px`;
  input.style.height = `${height}px`;
}
