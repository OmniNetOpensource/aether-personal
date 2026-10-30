// 本文件是聊天业务控制器：管理登录状态、消息列表、发送与重发，并驱动服务器事件中转，是前端聊天状态的唯一来源。

import { clearSession, readSession, saveSession } from './platform.js';
import { ServerEventRelay } from './ServerEventRelay.js';
import type { ClientCommand } from './ServerEventRelay.js';
import type { ChatPage, RequestFailedCode, ServerEvent } from './serverEvents.js';

type Delivery = 'sending' | 'failed' | 'delivered' | 'read';
export interface ChatMessage {
  id: string;
  text: string;
  isUser: boolean;
  createdAt: Date;
  order: number;
  position?: string;
  clientId?: string;
  delivery: Delivery;
}

export class RequestError extends Error {
  constructor(readonly code: RequestFailedCode) {
    super(requestFailedMessage(code));
  }
}

function requestFailedMessage(code: RequestFailedCode): string {
  switch (code) {
    case 'invalid_email':
      return '请输入有效的邮箱地址';
    case 'too_frequent':
      return '请求过于频繁，请稍后再试';
    case 'invalid_code':
      return '验证码不正确或已过期';
    case 'dev_login_unavailable':
      return '当前环境不开放开发测试登录';
    case 'unauthorized':
      return '登录已过期，请重新登录';
    case 'already_authenticated':
      return '已经登录';
    case 'internal':
      return '服务暂时不可用，请稍后重试';
  }
}

interface PendingRequest {
  resolve: (event: ServerEvent) => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

// ChatController 只维护界面状态：它订阅 ServerEventRelay 中转的服务器事件，
// 并通过中转发送命令，不再直接使用 HTTP 或原始 WebSocket 帧。
export class ChatController {
  messages: ChatMessage[] = [];
  initializing = true;
  connected = false;
  mainThinking = false;
  loadingHistory = false;
  hasMoreHistory = true;
  email = '';
  iconURL = '/app-icon.png';
  devLoginAvailable = false;
  error: string | null = null;
  token: string | null = null;
  private baseURL = '';
  private relay = new ServerEventRelay();
  private reconnectTimer?: ReturnType<typeof setTimeout>;
  private readyTimer?: ReturnType<typeof setTimeout>;
  private deliveryTimers = new Map<string, ReturnType<typeof setTimeout>>();
  private pendingRequests = new Map<string, PendingRequest>();
  private nextOrder = 0;
  private reconnectAttempt = 0;
  private historyLoaded = false;
  private socketOpen = false;
  private disposed = false;
  private suspended = false;
  private devLoginAttempted = false;
  private lastPosition?: string;
  private readUpTo?: string;
  private listeners = new Set<() => void>();

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private notify() {
    if (!this.disposed) this.listeners.forEach((listener) => listener());
  }

  async initialize() {
    this.relay.subscribe(
      (event) => this.handleServerEvent(event),
      (reason) => this.handleSocketClosed(reason),
    );
    try {
      const session = await readSession();
      this.baseURL = session.baseURL;
      this.token = session.token;
      this.email = session.email;
      this.iconURL = session.iconURL;
    } catch (error) {
      this.error = error instanceof Error ? error.message : '无法读取登录信息，请重新登录';
    }
    this.notify();
    if (navigator.onLine) this.connect();
  }

  private connect() {
    if (this.disposed || this.suspended || !navigator.onLine || this.socketOpen) return;
    this.socketOpen = true;
    clearTimeout(this.reconnectTimer);
    clearTimeout(this.readyTimer);
    this.readyTimer = setTimeout(() => this.closeSocket(), 10000);
    try {
      this.relay.connect(this.baseURL, this.token, this.lastPosition);
    } catch {
      this.handleSocketClosed('');
    }
  }

  private closeSocket() {
    this.relay.close();
    this.handleSocketClosed('');
  }

  private handleSocketClosed(reason: string) {
    if (this.disposed) return;
    this.socketOpen = false;
    this.connected = this.mainThinking = false;
    clearTimeout(this.readyTimer);
    this.rejectPendingRequests(new Error('连接已断开，请稍后重试'));
    this.messages = this.messages.map((message) =>
      message.delivery === 'sending' ? { ...message, delivery: 'failed' } : message,
    );
    this.deliveryTimers.forEach(clearTimeout);
    this.deliveryTimers.clear();
    if (reason === 'unauthorized') {
      this.clearLocalSession();
      this.error = '登录已过期，请重新登录';
      this.initializing = false;
      this.notify();
      return;
    }
    if (!this.suspended) {
      const delay = [1, 2, 4, 8, 15][Math.min(this.reconnectAttempt++, 4)] ?? 15;
      this.reconnectTimer = setTimeout(() => this.connect(), delay * 1000);
    }
    this.notify();
  }

  reconnect() {
    this.suspended = false;
    clearTimeout(this.reconnectTimer);
    this.connect();
  }

  suspend() {
    this.suspended = true;
    clearTimeout(this.reconnectTimer);
    this.relay.close();
    if (this.socketOpen) this.handleSocketClosed('');
  }

  private handleServerEvent(event: ServerEvent) {
    if (this.disposed) return;
    switch (event.type) {
      case 'ready': {
        clearTimeout(this.readyTimer);
        this.reconnectAttempt = 0;
        this.devLoginAvailable = event.dev_login;
        this.error = null;
        if (event.authenticated) {
          this.connected = true;
          this.email = event.email ?? this.email;
          if (event.history) {
            this.merge(event.history);
            this.historyLoaded = true;
            this.hasMoreHistory = event.history.messages.length > 0;
          }
          this.initializing = false;
        } else {
          this.connected = false;
          if (this.token) {
            this.token = null;
            clearSession();
          }
          if (event.dev_login && !this.devLoginAttempted) {
            this.devLoginAttempted = true;
            void this.devSignIn().catch((error: unknown) => {
              this.error = error instanceof Error ? error.message : '开发测试登录失败，请重试';
              this.initializing = false;
              this.notify();
            });
          } else {
            this.initializing = false;
          }
        }
        break;
      }
      case 'authenticated': {
        const pending = this.takePendingRequest(event.request_id);
        void saveSession(event.token, event.email)
          .then(() => {
            this.token = event.token;
            this.email = event.email;
            this.connected = true;
            this.merge(event.history);
            this.historyLoaded = true;
            this.hasMoreHistory = event.history.messages.length > 0;
            this.error = null;
            this.initializing = false;
            pending?.resolve(event);
            this.notify();
          })
          .catch((error: unknown) => {
            pending?.reject(
              error instanceof Error ? error : new Error('无法保存登录信息，请检查浏览器存储设置'),
            );
            this.initializing = false;
            this.notify();
          });
        return;
      }
      case 'code_sent':
        this.resolvePendingRequest(event.request_id, event);
        break;
      case 'history':
        this.merge(event);
        this.historyLoaded = true;
        this.hasMoreHistory = event.messages.length > 0;
        this.error = null;
        this.resolvePendingRequest(event.request_id, event);
        break;
      case 'messages':
        this.connected = true;
        this.mainThinking = event.main_thinking === true;
        this.error = null;
        this.merge(event);
        break;
      case 'delivered':
        clearTimeout(this.deliveryTimers.get(event.client_message_id));
        this.deliveryTimers.delete(event.client_message_id);
        this.setDelivery(event.client_message_id, 'delivered');
        break;
      case 'logged_out':
        this.clearLocalSession();
        this.resolvePendingRequest(event.request_id, event);
        break;
      case 'request_failed': {
        const pending = this.takePendingRequest(event.request_id);
        pending?.reject(new RequestError(event.code));
        if (!pending) {
          this.error = requestFailedMessage(event.code);
        }
        break;
      }
    }
    this.notify();
  }

  private clearLocalSession() {
    this.token = null;
    this.mainThinking = false;
    this.historyLoaded = false;
    this.loadingHistory = false;
    this.lastPosition = this.readUpTo = undefined;
    this.hasMoreHistory = true;
    this.messages = [];
    this.deliveryTimers.forEach(clearTimeout);
    this.deliveryTimers.clear();
    clearSession();
  }

  private sendRequest(
    command: Extract<ClientCommand, { request_id: string }>,
  ): Promise<ServerEvent> {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pendingRequests.delete(command.request_id);
        reject(new Error('连接超时，请检查网络后重试'));
      }, 15000);
      this.pendingRequests.set(command.request_id, { resolve, reject, timer });
      if (!this.socketOpen || !this.relay.send(command)) {
        clearTimeout(timer);
        this.pendingRequests.delete(command.request_id);
        reject(new Error('连接已断开，请稍后重试'));
      }
    });
  }

  private takePendingRequest(requestID: string): PendingRequest | undefined {
    const pending = this.pendingRequests.get(requestID);
    if (pending) {
      clearTimeout(pending.timer);
      this.pendingRequests.delete(requestID);
    }
    return pending;
  }

  private resolvePendingRequest(requestID: string, event: ServerEvent) {
    this.takePendingRequest(requestID)?.resolve(event);
  }

  private rejectPendingRequests(error: Error) {
    this.pendingRequests.forEach((pending) => {
      clearTimeout(pending.timer);
      pending.reject(error);
    });
    this.pendingRequests.clear();
  }

  async requestCode(email: string) {
    await this.sendRequest({
      type: 'request_code',
      request_id: crypto.randomUUID(),
      email: email.trim(),
    });
  }

  async signIn(email: string, code: string) {
    await this.sendRequest({
      type: 'verify_code',
      request_id: crypto.randomUUID(),
      email: email.trim(),
      code: code.trim(),
    });
  }

  async devSignIn() {
    await this.sendRequest({ type: 'dev_login', request_id: crypto.randomUUID() });
  }

  async signOut() {
    let loggedOut = false;
    try {
      if (this.token && this.socketOpen) {
        await this.sendRequest({ type: 'logout', request_id: crypto.randomUUID() });
        loggedOut = true;
      }
    } catch {
      // 本地退出照常完成；连接状态在下方统一处理。
    } finally {
      this.clearLocalSession();
      // 未收到 logged_out 时服务端仍把连接绑在旧会话上，断开重连后重新以未登录身份进入。
      if (!loggedOut) this.closeSocket();
      this.notify();
    }
  }

  async loadHistory(older = false) {
    if (this.loadingHistory || !this.token || (older && !this.hasMoreHistory)) return;
    this.loadingHistory = true;
    this.notify();
    const positions = this.messages
      .flatMap((message) => (message.position ? [message.position] : []))
      .sort();
    const before = older ? positions[0] : undefined;
    const token = this.token;
    try {
      await this.sendRequest({
        type: 'load_history',
        request_id: crypto.randomUUID(),
        ...(before ? { before } : {}),
      });
    } catch (error) {
      if (this.token === token) {
        this.error = error instanceof Error ? error.message : '历史消息加载失败，点击重试';
      }
    } finally {
      if (this.token === token) this.loadingHistory = false;
      this.notify();
    }
  }

  private merge(page: ChatPage) {
    if (page.read_up_to && (!this.readUpTo || page.read_up_to > this.readUpTo)) {
      this.readUpTo = page.read_up_to;
    }
    for (const row of page.messages) {
      if (!this.lastPosition || row.position > this.lastPosition) this.lastPosition = row.position;
      const index = this.messages.findIndex(
        (message) =>
          message.id === row.id ||
          (row.client_message_id && message.clientId === row.client_message_id),
      );
      if (row.client_message_id) {
        clearTimeout(this.deliveryTimers.get(row.client_message_id));
        this.deliveryTimers.delete(row.client_message_id);
      }
      const message: ChatMessage = {
        id: row.id,
        text: row.text,
        isUser: row.sender === 'user',
        createdAt: new Date(row.created_at),
        order: this.messages[index]?.order ?? this.nextOrder++,
        position: row.position,
        clientId: row.client_message_id ?? undefined,
        delivery: 'delivered',
      };
      if (index < 0) this.messages.push(message);
      else this.messages[index] = message;
    }
    this.messages = this.messages.map((message) =>
      message.isUser && message.position && this.readUpTo && message.position <= this.readUpTo
        ? { ...message, delivery: 'read' }
        : message,
    );
    this.messages.sort(
      (a, b) =>
        (a.position && b.position
          ? a.position.localeCompare(b.position)
          : a.createdAt.getTime() - b.createdAt.getTime()) || a.order - b.order,
    );
  }

  async send(text: string) {
    const trimmed = text.trim();
    if (!trimmed || Array.from(trimmed).length > 32000 || !this.token) return false;
    const id = crypto.randomUUID();
    this.messages = [
      ...this.messages,
      {
        id,
        clientId: id,
        text: trimmed,
        isUser: true,
        createdAt: new Date(),
        order: this.nextOrder++,
        delivery: 'sending',
      },
    ];
    this.sendFrame(id, trimmed);
    this.notify();
    return true;
  }

  retry(message: ChatMessage) {
    if (!message.clientId) return;
    this.setDelivery(message.clientId, 'sending');
    this.sendFrame(message.clientId, message.text);
    this.notify();
  }

  private sendFrame(id: string, text: string) {
    if (
      !this.connected ||
      !this.relay.send({ type: 'send_message', client_message_id: id, text })
    ) {
      this.setDelivery(id, 'failed');
      this.reconnect();
      return;
    }
    clearTimeout(this.deliveryTimers.get(id));
    this.deliveryTimers.set(
      id,
      setTimeout(() => {
        this.deliveryTimers.delete(id);
        this.setDelivery(id, 'failed');
        this.notify();
      }, 15000),
    );
  }

  private setDelivery(id: string, delivery: Delivery) {
    this.messages = this.messages.map((message) =>
      message.clientId === id && message.delivery !== 'read' ? { ...message, delivery } : message,
    );
  }

  dispose() {
    this.disposed = true;
    clearTimeout(this.reconnectTimer);
    clearTimeout(this.readyTimer);
    this.deliveryTimers.forEach(clearTimeout);
    this.rejectPendingRequests(new Error('连接已关闭'));
    this.relay.close();
    this.listeners.clear();
  }
}
