// 本文件提供轻量 SPA 导航与当前路径订阅，供聊天主页与记忆查看页在不刷新页面的前提下互相切换。

import { useEffect, useState } from 'react';
import type { AnchorHTMLAttributes } from 'react';

// navigate 切换浏览器地址并派发 popstate，让入口路由按路径渲染对应页面。
export function navigate(path: string) {
  if (location.pathname === path) return;
  history.pushState(null, '', path);
  window.dispatchEvent(new PopStateEvent('popstate'));
}

// usePathname 订阅浏览器前进后退与编程式导航事件，返回当前路径名。
export function usePathname(): string {
  const [pathname, setPathname] = useState(() => location.pathname);
  useEffect(() => {
    const update = () => setPathname(location.pathname);
    window.addEventListener('popstate', update);
    return () => window.removeEventListener('popstate', update);
  }, []);
  return pathname;
}

// NavLink 渲染原生链接，普通左键点击时转为客户端导航，保留修饰键新开标签与无障碍语义。
export function NavLink({
  to,
  onClick,
  ...props
}: Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href'> & { to: string }) {
  return (
    <a
      {...props}
      href={to}
      onClick={(event) => {
        onClick?.(event);
        if (
          event.defaultPrevented ||
          event.button !== 0 ||
          event.metaKey ||
          event.ctrlKey ||
          event.shiftKey ||
          event.altKey
        ) {
          return;
        }
        event.preventDefault();
        navigate(to);
      }}
    />
  );
}
