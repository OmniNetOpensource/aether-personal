// 本文件是聊天业务控制器：管理登录、实时消息与任务队列快照、发送与上传，并通过事件中转提供独立的历史搜索和上下文读取。

import { clearSession, readSession, saveSession } from './platform.js';
import { ServerEventRelay } from './ServerEventRelay.js';
import type { ClientCommand } from './ServerEventRelay.js';
import type {
  ChatPage,
  ChatTask,
  RequestFailedCode,
  ServerEvent,
  TaskStatus,
} from './serverEvents.js';

import { messageFileSchema, messageImageSchema } from './messageAttachments.js';
import type { MessageFile, MessageImage, MessagePart } from './messageAttachments.js';

// isOpenTask 判断任务是否处于未结束状态，供右上角角标、输入框胶囊与任务清单分组共用。
export function isOpenTask(task: ChatTask): boolean {
  return task.status === 'in_progress' || task.status === 'queued' || task.status === 'waiting';
}

// taskStatusLabel 返回任务状态的中文展示文案，保持输入框胶囊与任务清单用词一致。
export function taskStatusLabel(status: TaskStatus): string {
  switch (status) {
    case 'in_progress':
      return '进行中';
    case 'queued':
      return '排队中';
    case 'waiting':
      return '等待回复';
    case 'completed':
      return '已完成';
    case 'cancelled':
      return '已取消';
  }
}

// ChatMessage 是服务器已保存的聊天消息，发送中的内容留在输入框里。
export interface ChatMessage {
  id: string;
  text: string;
  parts: MessagePart[];
  isUser: boolean;
  createdAt: Date;
  position: string;
}

// chatMessageFromRow 在服务器消息进入界面时统一转换时间和多段内容，正常聊天与历史窗口共用。
export function chatMessageFromRow(row: ChatPage['messages'][number]): ChatMessage {
  return {
    id: row.id,
    text: row.text,
    parts: row.parts ?? (row.text ? [{ type: 'text', text: row.text }] : []),
    isUser: row.sender === 'user',
    createdAt: new Date(row.created_at),
    position: row.position,
  };
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
    case 'verification_required':
      return '安全验证已失效，请完成验证后重试';
    case 'login_unavailable':
      return '登录服务暂不可用，请稍后再试';
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
    case 'invalid_request':
      return '请输入 1 至 200 个字符的搜索词';
    case 'not_found':
      return '找不到这条历史消息';
  }
}

interface PendingRequest {
  resolve: (event: ServerEvent) => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

// ChatController 只维护界面状态：它订阅 ServerEventRelay 中转的服务器事件，
// 并通过中转发送聊天命令；图片字节单独通过 HTTP 上传，不直接接触原始 WebSocket 帧。
export class ChatController {
  messages: ChatMessage[] = [];
  tasks: ChatTask[] = [];
  // taskReplyDrafts 按任务 ID 暂存浮层中等待回复卡片的未发送草稿，收起浮层查看历史时不丢失输入。
  taskReplyDrafts = new Map<string, string>();
  initializing = true;
  connected = false;
  mainThinking = false;
  mainFailed = false;
  loadingHistory = false;
  hasMoreHistory = true;
  email = '';
  iconURL = '/app-icon.png';
  devLoginAvailable = false;
  desktopAvailable = false;
  turnstileSiteKey = '';
  error: string | null = null;
  token: string | null = null;
  private baseURL = '';
  private relay = new ServerEventRelay();
  private reconnectTimer?: ReturnType<typeof setTimeout>;
  private readyTimer?: ReturnType<typeof setTimeout>;
  private pendingRequests = new Map<string, PendingRequest>();
  private reconnectAttempt = 0;
  private historyLoaded = false;
  private socketOpen = false;
  private disposed = false;
  private suspended = false;
  private devLoginAttempted = false;
  // 重连按变更序号续推，历史分页只按已读取的历史页推进；迟到的旧回复不应跳过中间一页。
  private lastChangeCursor?: string;
  private oldestHistoryPosition?: string;
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
    if (navigator.onLine) {
      this.connect();
    } else {
      // 离线冷启动时已缓存登录态直接进聊天页编辑草稿，未登录则进登录页说明原因。
      if (!this.token) {
        this.error = '当前处于离线状态，恢复网络后即可登录';
      }
      this.initializing = false;
      this.notify();
    }
  }

  private connect() {
    if (this.disposed || this.suspended || !navigator.onLine || this.socketOpen) return;
    this.socketOpen = true;
    clearTimeout(this.reconnectTimer);
    clearTimeout(this.readyTimer);
    this.readyTimer = setTimeout(() => this.closeSocket(), 10000);
    try {
      this.relay.connect(this.baseURL, this.token, this.lastChangeCursor);
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
        this.turnstileSiteKey = event.turnstile_site_key ?? '';
        this.error = null;
        if (event.authenticated) {
          this.lastChangeCursor = event.change_cursor;
          this.connected = true;
          this.desktopAvailable = event.desktop_available === true;
          this.email = event.email ?? this.email;
          if (event.tasks !== undefined) {
            this.tasks = event.tasks;
          }
          if (event.history) {
            this.mergeHistory(event.history);
          }
          this.initializing = false;
        } else {
          this.connected = false;
          this.desktopAvailable = false;
          this.tasks = [];
          this.taskReplyDrafts.clear();
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
        this.lastChangeCursor = event.change_cursor;
        const pending = this.takePendingRequest(event.request_id);
        void saveSession(event.token, event.email)
          .then(() => {
            this.token = event.token;
            this.email = event.email;
            this.desktopAvailable = event.desktop_available === true;
            this.tasks = event.tasks ?? [];
            this.connected = true;
            this.mergeHistory(event.history);
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
      case 'search_results':
      case 'message_context':
        this.resolvePendingRequest(event.request_id, event);
        break;
      case 'history':
        this.mergeHistory(event);
        this.error = null;
        this.resolvePendingRequest(event.request_id, event);
        break;
      case 'messages':
        this.lastChangeCursor = event.change_cursor;
        this.connected = true;
        this.mainThinking = event.main_thinking === true;
        this.mainFailed = event.main_failed === true;
        if (event.tasks !== undefined) {
          this.tasks = event.tasks;
        }
        this.error = null;
        this.merge(event);
        break;
      case 'delivered':
        this.resolvePendingRequest(event.client_message_id, event);
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
    this.desktopAvailable = false;
    this.mainThinking = false;
    this.mainFailed = false;
    this.historyLoaded = false;
    this.loadingHistory = false;
    this.lastChangeCursor = undefined;
    this.oldestHistoryPosition = undefined;
    this.hasMoreHistory = true;
    this.messages = [];
    this.tasks = [];
    this.taskReplyDrafts.clear();
    clearSession();
  }

  // sendRequest 发出一条命令并等待它的回执：发送消息按 client_message_id 等 delivered，其余命令按 request_id 等对应事件。
  private sendRequest(command: ClientCommand): Promise<ServerEvent> {
    const requestID =
      command.type === 'send_message' ? command.client_message_id : command.request_id;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(
        () => {
          this.pendingRequests.delete(requestID);
          reject(new Error('连接超时，请检查网络后重试'));
        },
        command.type === 'request_code' ? 30000 : 15000,
      );
      this.pendingRequests.set(requestID, { resolve, reject, timer });
      if (!this.socketOpen || !this.relay.send(command)) {
        clearTimeout(timer);
        this.pendingRequests.delete(requestID);
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

  async requestCode(email: string, turnstileToken: string) {
    await this.sendRequest({
      type: 'request_code',
      request_id: crypto.randomUUID(),
      email: email.trim(),
      turnstile_token: turnstileToken,
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
    const before = older ? this.oldestHistoryPosition : undefined;
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

  // searchMessages 不把搜索结果并入正常列表，也不推进重连或历史游标。
  async searchMessages(keyword: string, before?: string) {
    const event = await this.sendRequest({
      type: 'search_messages',
      request_id: crypto.randomUUID(),
      keyword,
      ...(before ? { before } : {}),
    });
    if (event.type !== 'search_results') throw new Error('搜索返回了错误的事件类型');
    return event;
  }

  // loadMessageContext 按原消息定位或向一侧翻页，独立历史窗口由调用方持有。
  async loadMessageContext(messageID: string, direction: 'around' | 'older' | 'newer') {
    const event = await this.sendRequest({
      type: 'load_message_context',
      request_id: crypto.randomUUID(),
      message_id: messageID,
      direction,
    });
    if (event.type !== 'message_context') throw new Error('历史消息返回了错误的事件类型');
    return event;
  }

  // mergeHistory 记住连续翻过的最早位置，即使实时补来更早的一条占位回复也从原来的位置续翻。
  private mergeHistory(page: ChatPage) {
    const first = page.messages[0]?.position;
    if (first && (!this.oldestHistoryPosition || first < this.oldestHistoryPosition)) {
      this.oldestHistoryPosition = first;
    }
    this.merge(page);
    this.historyLoaded = true;
    this.hasMoreHistory = page.messages.length > 0;
  }

  // merge 把服务器发来的一页消息并入列表：同一 ID 覆盖旧值，再按服务器位置排序。
  private merge(page: ChatPage) {
    for (const row of page.messages) {
      const message = chatMessageFromRow(row);
      const index = this.messages.findIndex((existing) => existing.id === row.id);
      if (index < 0) this.messages.push(message);
      else this.messages[index] = message;
    }
    this.messages.sort((a, b) => a.position.localeCompare(b.position));
  }

  // uploadImage 以原始字节上传图片；聊天消息只携带服务端生成的 OSS 引用。
  async uploadImage(file: File): Promise<MessageImage> {
    if (!this.token) throw new Error('请先登录');
    const response = await fetch(this.baseURL + '/images?name=' + encodeURIComponent(file.name), {
      method: 'POST',
      headers: { Authorization: 'Bearer ' + this.token, 'Content-Type': file.type },
      body: file,
      signal: AbortSignal.timeout(60000),
    });
    if (!response.ok) throw new Error((await response.text()).trim() || '图片上传失败');
    return messageImageSchema.parse(await response.json());
  }

  // uploadFile 以原始字节流上传工作区文件（上限 50 MB）；消息只携带服务端生成的 OSS 引用，阿芒需要时经 sandbox_import_file 导入电脑。
  async uploadFile(file: File): Promise<MessageFile> {
    if (!this.token) throw new Error('请先登录');
    const response = await fetch(this.baseURL + '/files?name=' + encodeURIComponent(file.name), {
      method: 'POST',
      headers: {
        Authorization: 'Bearer ' + this.token,
        'Content-Type': file.type || 'application/octet-stream',
      },
      body: file,
      signal: AbortSignal.timeout(300000),
    });
    if (!response.ok) throw new Error((await response.text()).trim() || '文件上传失败');
    return messageFileSchema.parse(await response.json());
  }

  // send 发出一条消息并等服务器保存：成功时消息已经推进列表，失败时抛出错误，由输入框展示并保留内容。
  // clientMessageID 由输入框为同一份草稿复用，重发时服务器按它去重，不会存成两条。
  async send(parts: MessagePart[], clientMessageID: string) {
    const text = parts.flatMap((part) => (part.type === 'text' ? [part.text] : [])).join('');
    const quotes = parts.flatMap((part) => (part.type === 'quote' ? [part.quote] : []));
    const images = parts.flatMap((part) => (part.type === 'image' ? [part.image] : []));
    const files = parts.flatMap((part) => (part.type === 'file' ? [part.file] : []));
    const trimmed = text.trim();
    if (
      (!trimmed && quotes.length === 0 && images.length === 0 && files.length === 0) ||
      Array.from(trimmed + quotes.map((quote) => quote.text).join('')).length > 64000 ||
      !this.token
    )
      return false;
    if (!this.connected) {
      this.reconnect();
      throw new Error('连接已断开，请稍后重试');
    }
    await this.sendRequest({ type: 'send_message', client_message_id: clientMessageID, parts });
    return true;
  }

  dispose() {
    this.disposed = true;
    clearTimeout(this.reconnectTimer);
    clearTimeout(this.readyTimer);
    this.rejectPendingRequests(new Error('连接已关闭'));
    this.relay.close();
    this.listeners.clear();
  }
}
