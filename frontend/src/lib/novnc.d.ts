// 本文件声明 noVNC 1.7 官方 RFB API 中本项目使用的只读接口；该版本 npm 包尚未附带 TypeScript 声明。

declare module '@novnc/novnc' {
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, channel: WebSocket, options: { shared: boolean });
    viewOnly: boolean;
    focusOnClick: boolean;
    scaleViewport: boolean;
    resizeSession: boolean;
    background: string;
    disconnect(): void;
  }
}
