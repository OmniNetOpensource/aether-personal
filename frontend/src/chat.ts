import type { WebSocket } from '@lynx-js/websocket';
import { z } from 'zod';
import { host, messageID, readSession, saveSession } from './platform.js';

const messageSchema = z.object({
  id: z.string().min(1),
  text: z.string(),
  sender: z.enum(['user', 'main']),
  created_at: z.iso.datetime({ offset: true }),
  position: z.uuid(),
  client_message_id: z.uuid().nullable().optional(),
});
const pageSchema = z.object({
  messages: z
    .array(messageSchema)
    .nullable()
    .transform((value) => value ?? []),
  read_up_to: z.string().nullable().optional(),
});
const activitySchema = z.discriminatedUnion('state', [
  z.object({ state: z.literal('idle'), revision: z.number().int().nonnegative(), work_version: z.number().int().nonnegative() }),
  z.object({ state: z.literal('working'), summary: z.string(), revision: z.number().int().nonnegative(), work_version: z.number().int().nonnegative() }),
]);
const frameSchema = z.discriminatedUnion('type', [
  pageSchema.extend({ type: z.literal('messages'), main_thinking: z.boolean().optional(), activity: activitySchema.optional() }),
  z.object({ type: z.literal('delivered'), client_message_id: z.uuid() }),
]);

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

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

export class ChatController {
  messages: ChatMessage[] = [];
  initializing = true;
  connected = false;
  mainThinking = false;
  activity: z.infer<typeof activitySchema> = { state: 'idle', revision: 0, work_version: 0 };
  loadingHistory = false;
  hasMoreHistory = true;
  email = '';
  devLoginAvailable = false;
  error: string | null = null;
  token: string | null = null;
  private baseURL = '';
  private channel?: WebSocket;
  private reconnectTimer?: ReturnType<typeof setTimeout>;
  private authTimer?: ReturnType<typeof setTimeout>;
  private deliveryTimers = new Map<string, ReturnType<typeof setTimeout>>();
  private connection = 0;
  private nextOrder = 0;
  private reconnectAttempt = 0;
  private historyLoaded = false;
  private connecting = false;
  private disposed = false;
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
    try {
      const session = await readSession();
      this.baseURL = session.baseURL;
      this.token = session.token;
      this.email = session.email;
      const response = await this.request('GET', '/auth/config');
      const config = z.object({ dev_login: z.boolean() }).parse(await response.json());
      this.devLoginAvailable = config.dev_login;
    } catch (error) {
      this.error = error instanceof Error ? error.message : '无法读取登录信息，请重新登录';
    }
    this.initializing = false;
    this.notify();
    if (this.token) this.connect();
  }

  private async request(method: string, path: string, body?: unknown) {
    const token = this.token;
    const abort = new AbortController();
    const timer = setTimeout(() => abort.abort(), 15000);
    try {
      const response = await fetch(`${this.baseURL}${path}`, {
        method,
        signal: abort.signal,
        headers: {
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
          ...(body ? { 'Content-Type': 'application/json' } : {}),
        },
        ...(body ? { body: JSON.stringify(body) } : {}),
      });
      if (response.status >= 400) {
        const text = await response.text();
        if (response.status === 401 && token && this.token === token) this.clearSession();
        throw new ApiError(
          response.status === 401
            ? '登录已过期，请重新登录'
            : response.status === 429
              ? '请求过于频繁，请稍后再试'
              : response.status === 400 && text.includes('invalid or expired code')
                ? '验证码不正确或已过期'
                : response.status === 400 && text.includes('invalid email')
                  ? '请输入有效的邮箱地址'
                  : response.status >= 500
                    ? '服务暂时不可用，请稍后重试'
                    : text.trim() || '请求失败，请重试',
          response.status,
        );
      }
      return response;
    } finally {
      clearTimeout(timer);
    }
  }

  async requestCode(email: string) {
    await this.request('POST', '/auth/code', { email: email.trim() });
  }
  async signIn(email: string, code: string) {
    const response = await this.request('POST', '/auth/verify', {
      email: email.trim(),
      code: code.trim(),
    });
    const data = z.object({ token: z.string().min(1) }).parse(await response.json());
    const address = email.trim().toLowerCase();
    await this.completeSignIn(data.token, address);
  }
  async devSignIn() {
    const response = await this.request('POST', '/auth/dev');
    const data = z
      .object({ token: z.string().min(1), email: z.email() })
      .parse(await response.json());
    await this.completeSignIn(data.token, data.email);
  }
  private async completeSignIn(token: string, email: string) {
    await saveSession(token, email);
    this.token = token;
    this.email = email;
    this.error = null;
    this.notify();
    this.connect();
  }
  async signOut() {
    try {
      await this.request('POST', '/auth/logout');
    } finally {
      this.clearSession();
    }
  }
  private clearSession() {
    this.activity = { state: 'idle', revision: 0, work_version: 0 };
    this.token = null;
    this.connection++;
    clearTimeout(this.reconnectTimer);
    clearTimeout(this.authTimer);
    this.deliveryTimers.forEach(clearTimeout);
    this.deliveryTimers.clear();
    this.channel?.close();
    this.channel = undefined;
    this.connected =
      this.mainThinking =
      this.connecting =
      this.historyLoaded =
      this.loadingHistory =
        false;
    this.lastPosition = this.readUpTo = undefined;
    this.hasMoreHistory = true;
    this.messages = [];
    host.clearSession();
    this.notify();
  }

  private connect() {
    if (this.disposed || !this.token || this.connecting || this.connected) return;
    this.connecting = true;
    const generation = ++this.connection;
    clearTimeout(this.reconnectTimer);
    const endpoint = this.baseURL.replace(/^http/, 'ws') + '/ws';
    try {
      const {
        WebSocket: LynxWebSocket,
      }: typeof import('@lynx-js/websocket') = require('@lynx-js/websocket');
      const channel = new LynxWebSocket(endpoint);
      this.channel = channel;
      this.authTimer = setTimeout(() => this.disconnect(generation), 10000);
      channel.onopen = () => {
        if (generation !== this.connection || !this.token) return;
        channel.send(
          JSON.stringify({
            type: 'auth',
            token: this.token,
            ...(this.lastPosition ? { last_position: this.lastPosition } : {}),
          }),
        );
      };
      channel.onmessage = (event) => {
        if (generation !== this.connection || this.disposed) return;
        try {
          const frame = frameSchema.parse(JSON.parse(event.data));
          if (frame.type === 'delivered') {
            clearTimeout(this.deliveryTimers.get(frame.client_message_id));
            this.deliveryTimers.delete(frame.client_message_id);
            this.setDelivery(frame.client_message_id, 'delivered');
          } else {
            this.connected = true;
            this.mainThinking = frame.main_thinking === true;
            if (frame.activity && frame.activity.revision >= this.activity.revision) {
              this.activity = frame.activity;
            }
            this.connecting = false;
            this.reconnectAttempt = 0;
            clearTimeout(this.authTimer);
            this.error = null;
            this.merge(frame);
            if (!this.historyLoaded && !this.loadingHistory) void this.loadHistory();
          }
          this.notify();
        } catch {
          this.disconnect(generation);
        }
      };
      channel.onerror = () => this.disconnect(generation);
      channel.onclose = (event) => {
        if (generation !== this.connection) return;
        if (event.reason === 'unauthorized') {
          this.error = '登录已过期，请重新登录';
          this.clearSession();
        } else this.disconnect(generation);
      };
    } catch {
      this.disconnect(generation);
    }
  }

  private disconnect(generation: number) {
    if (generation !== this.connection || this.disposed || !this.token) return;
    this.connection++;
    this.activity = { state: 'idle', revision: 0, work_version: 0 };
    this.connected = this.mainThinking = this.connecting = false;
    clearTimeout(this.authTimer);
    this.channel?.close();
    this.messages = this.messages.map((message) =>
      message.delivery === 'sending' ? { ...message, delivery: 'failed' } : message,
    );
    this.deliveryTimers.forEach(clearTimeout);
    this.deliveryTimers.clear();
    const delay = [1, 2, 4, 8, 15][Math.min(this.reconnectAttempt++, 4)] ?? 15;
    this.reconnectTimer = setTimeout(() => this.connect(), delay * 1000);
    this.notify();
  }
  reconnect() {
    clearTimeout(this.reconnectTimer);
    this.connect();
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
      const response = await this.request(
        'GET',
        `/messages${before ? `?before=${encodeURIComponent(before)}` : ''}`,
      );
      const page = pageSchema.parse(await response.json());
      if (this.token !== token || this.disposed) return;
      this.merge(page);
      this.historyLoaded = true;
      this.hasMoreHistory = page.messages.length > 0;
      this.error = null;
    } catch (error) {
      if (this.token === token || !this.token)
        this.error = error instanceof ApiError ? error.message : '历史消息加载失败，点击重试';
    } finally {
      if (this.token === token) this.loadingHistory = false;
      this.notify();
    }
  }

  private merge(page: z.infer<typeof pageSchema>) {
    if (page.read_up_to && (!this.readUpTo || page.read_up_to > this.readUpTo))
      this.readUpTo = page.read_up_to;
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
    const token = this.token;
    const id = await messageID();
    if (this.token !== token || this.disposed) return false;
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
    if (!this.connected) {
      this.setDelivery(id, 'failed');
      this.reconnect();
      return;
    }
    try {
      this.channel?.send(JSON.stringify({ type: 'send_message', client_message_id: id, text }));
      clearTimeout(this.deliveryTimers.get(id));
      this.deliveryTimers.set(
        id,
        setTimeout(() => {
          this.deliveryTimers.delete(id);
          this.setDelivery(id, 'failed');
          this.notify();
        }, 15000),
      );
    } catch {
      this.setDelivery(id, 'failed');
    }
  }
  private setDelivery(id: string, delivery: Delivery) {
    this.messages = this.messages.map((message) =>
      message.clientId === id && message.delivery !== 'read' ? { ...message, delivery } : message,
    );
  }
  dispose() {
    this.disposed = true;
    this.connection++;
    clearTimeout(this.reconnectTimer);
    clearTimeout(this.authTimer);
    this.deliveryTimers.forEach(clearTimeout);
    this.channel?.close();
    this.listeners.clear();
  }
}
