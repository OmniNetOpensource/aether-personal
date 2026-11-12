// 本文件定义聊天端设计画布的全部预览场景与免后端假数据，直接挂载真实界面组件以还原每种运行状态。

import { useEffect, useLayoutEffect, useRef } from 'react';
import { createRoot } from 'react-dom/client';
import { ChatHistoryContext } from '../components/chat/ChatHistoryContext.js';
import { ChatHistorySearch } from '../components/chat/ChatHistorySearch.js';
import { ChatMessageList } from '../components/chat/ChatMessageList.js';
import { ChatPage } from '../components/chat/ChatPage.js';
import { ChatTaskSheet } from '../components/chat/ChatTaskSheet.js';
import { DesktopViewer } from '../components/chat/DesktopViewer.js';
import { MessageComposer } from '../components/chat/MessageComposer.js';
import type { MessageComposerHandle } from '../components/chat/MessageComposer.js';
import { LoadingScreen } from '../components/LoadingScreen.js';
import { LoginPage } from '../components/login/LoginPage.js';
import { ChatController } from '../lib/ChatController.js';
import type { ChatMessage } from '../lib/ChatController.js';
import type { MessagePart } from '../lib/messageAttachments.js';
import type { ChatPage as ChatPagePayload, ChatTask } from '../lib/serverEvents.js';
import '../styles/style.css';

// sampleSketchDataUrl 生成一张森林绿与柔黄配色的离线矢量示意图，避免画布预览依赖外部图片网络。
const sampleSketchDataUrl =
  'data:image/svg+xml;utf8,' +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" width="560" height="320" viewBox="0 0 560 320">
      <rect width="560" height="320" rx="18" fill="#173b29"/>
      <rect x="28" y="28" width="232" height="116" rx="14" fill="#204b32" stroke="#527457" stroke-width="2"/>
      <text x="48" y="68" fill="#fff4d6" font-family="system-ui, sans-serif" font-size="18" font-weight="700">Web / PWA 客户端</text>
      <text x="48" y="98" fill="#c4d1b7" font-family="system-ui, sans-serif" font-size="14">单连接登录 · 历史 · 实时推送</text>
      <rect x="300" y="28" width="232" height="116" rx="14" fill="#204b32" stroke="#f6cf83" stroke-width="2"/>
      <text x="320" y="68" fill="#f6cf83" font-family="system-ui, sans-serif" font-size="18" font-weight="700">Go 对等实例 × 2</text>
      <text x="320" y="98" fill="#fff4d6" font-family="system-ui, sans-serif" font-size="14">Agent 三阶段循环 · 租约续期</text>
      <rect x="28" y="176" width="504" height="116" rx="14" fill="#2f5b3d" stroke="#527457" stroke-width="2"/>
      <text x="48" y="218" fill="#fff4d6" font-family="system-ui, sans-serif" font-size="17" font-weight="700">PostgreSQL 18 持久事实 + Redis 8 纯内存信号与图片缓存</text>
      <text x="48" y="250" fill="#c4d1b7" font-family="system-ui, sans-serif" font-size="14">消息、任务、主题记忆与栅栏代数全落库，滚动发布零停机交接</text>
    </svg>`,
  );

// createMessage 构造带固定时间戳与排序游标的聊天消息，保证各画板的时间分隔与分组稳定可复现。
function createMessage(
  index: number,
  isUser: boolean,
  createdAt: string,
  text: string,
  parts?: MessagePart[],
): ChatMessage {
  const suffix = String(index).padStart(12, '0');
  return {
    id: `01960000-0000-7000-8000-${suffix}`,
    position: `01960000-0000-7000-8000-${suffix}`,
    isUser,
    createdAt: new Date(createdAt),
    text,
    parts: parts ?? [{ type: 'text', text }],
  };
}

// dailyMessages 覆盖跨日时间条、同说话人两分钟内合并气泡以及日常自然的问答节奏。
const dailyMessages: ChatMessage[] = [
  createMessage(
    101,
    true,
    '2026-11-10T21:14:00+08:00',
    '帮我记一下：周五下午三点前要把季度服务器账单核对完。',
  ),
  createMessage(
    102,
    false,
    '2026-11-10T21:14:25+08:00',
    '记下了。我已经设好周五 14:00 的提醒，到时会把上季度的费用对比表一并找出来给你。',
  ),
  createMessage(
    103,
    true,
    '2026-11-11T09:40:10+08:00',
    '早安，今天新加坡那台机器的双实例滚动更新顺畅吗？',
  ),
  createMessage(
    104,
    true,
    '2026-11-11T09:41:00+08:00',
    '特别是正在跑长工具调用的会话，有没有断掉？',
  ),
  createMessage(
    105,
    false,
    '2026-11-11T09:41:32+08:00',
    '一切平稳。旧容器收到停止信号后立刻释放了 Redis 租约并把待办推回唤醒队列，`api-2` 在 400 毫秒内接手续跑，没有丢失任何步骤回执。',
  ),
];

// markdownSampleText 集中覆盖 Markdown 管线支持的标题、任务列表、表格、公式与 Go 高亮代码块。
const markdownSampleText = `## 双实例切换核对清单

重点关注**连接池上限**与**租约栅栏**，~~旧的单进程内存锁~~已经移除：

- [x] \`GET /healthz\` 同时探测 PostgreSQL 与 Redis
- [x] 停止信号触发时归还租约并重推唤醒队列
- [ ] 观察高峰期图片缓存命中率

| 参数 | 默认值 | 用途说明 |
| --- | --- | --- |
| \`DATABASE_MAX_CONNS\` | \`20\` | 单容器连接池上限 |
| \`AGENT_CAPACITY\` | \`16\` | 并发运行 Main 的最大槽位数 |
| \`IMAGE_CACHE_TTL\` | \`24h\` | Redis 共享图片旁路缓存有效期 |

当单实例容量为 $C$、预留连接为 $R = 4$ 时，可用并发槽位满足 $C = N_{\\text{conn}} - R$：

$$
T_{\\text{handover}} \\le T_{\\text{grace}} + T_{\\text{blpop}}
$$

\`\`\`go
// healthz 同时探测 PostgreSQL 与 Redis 连通性，任一异常即返回 503。
func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
\tif err := s.databasePool.Ping(r.Context()); err != nil {
\t\thttp.Error(w, "database unavailable", http.StatusServiceUnavailable)
\t\treturn
\t}
\tw.Write([]byte("ok"))
}
\`\`\``;

// longCollapsedText 构造高度远超 360px 折叠阈值的长回复，用于验证渐隐裁切与「展开全文」按钮。
const longCollapsedText = `### 为什么把持久事实全部留在 PostgreSQL

1. **故障恢复边界清晰**
   Redis 以纯内存无持久化模式运行，即使整机重启或容器重建，消息列表、需求队列、主题记忆与 \`main_lease_epoch\` 栅栏代数都完好保存在 PostgreSQL 中，启动时的补偿扫描会自动把未完待办重新推入唤醒队列。

2. **避免跨存储双写不一致**
   如果把运行步骤或会话状态拆到两套存储里，网络抖动或进程退出时就必须额外处理半成功状态；现在所有业务事实落在同一个事务边界内，Redis 只做可丢弃的信号通知与临时租约。

3. **滚动升级体验连贯**
   发版时逐台替换 \`api-1\` 与 \`api-2\`，Caddy 通过主动健康检查切走流量，存活容器直接从最近一个完整步骤继续推进，聊天界面不需要用户手动重发。

4. **后续扩容路径自然**
   只要数据库与对象存储共享，对等无状态后端容器可以从两台平滑横向增加，不需要修改会话路由协议。`;

// sampleTasks 覆盖进行中、排队中、等待回复、已完成与已取消五种状态，供胶囊与清单画板共用。
const sampleTasks: ChatTask[] = [
  {
    id: '01960000-0000-7000-8000-000000000501',
    title: '核对工作区最新静态检查并汇总告警',
    description:
      '进入 /mnt/workspace 执行 go build、go vet 与前端类型检查，按模块整理告警原因与修复建议。',
    status: 'in_progress',
    created_at: '2026-11-12T10:35:00+08:00',
    updated_at: '2026-11-12T10:36:12+08:00',
  },
  {
    id: '01960000-0000-7000-8000-000000000502',
    title: '导出三季度云服务器与对象存储账单对比表',
    description:
      '对比新加坡单机双实例改造前后的月度账单明细：\n1. 计算与带宽固定成本\n2. 私有 OSS 存储量与回源请求变化\n3. Redis 共享图片缓存上线后的请求减幅\n生成 xlsx 文件发回聊天。',
    status: 'queued',
    created_at: '2026-11-12T10:38:00+08:00',
    updated_at: '2026-11-12T10:38:00+08:00',
  },
  {
    id: '01960000-0000-7000-8000-000000000503',
    title: '预约下周三团队架构复盘提醒',
    description: '等待确认具体复盘开始时间（上午 10:00 或下午 15:00）以及是否需要提前半小时提醒。',
    status: 'waiting',
    created_at: '2026-11-12T09:50:00+08:00',
    updated_at: '2026-11-12T09:55:20+08:00',
  },
  {
    id: '01960000-0000-7000-8000-000000000504',
    title: '检查双实例滚动发布期间的长工具调用交接记录',
    description: '核对 api-1 收到停止信号后归还租约与 api-2 接手步骤的日志时间差。',
    status: 'completed',
    created_at: '2026-11-11T09:40:10+08:00',
    updated_at: '2026-11-11T09:41:32+08:00',
  },
  {
    id: '01960000-0000-7000-8000-000000000505',
    title: '整理旧版单进程监听脚本归档',
    description: '已确认不再保留池外 LISTEN 脚本，用户要求直接取消该整理项。',
    status: 'cancelled',
    created_at: '2026-11-10T18:10:00+08:00',
    updated_at: '2026-11-10T18:14:05+08:00',
  },
];

// createPreviewController 生成不建立真实网络连接的控制器实例，并填入当前画板所需的静态快照。
function createPreviewController(options: {
  email?: string;
  token?: string | null;
  connected?: boolean;
  desktopAvailable?: boolean;
  devLoginAvailable?: boolean;
  turnstileSiteKey?: string;
  mainThinking?: boolean;
  mainFailed?: boolean;
  hasMoreHistory?: boolean;
  error?: string | null;
  messages?: ChatMessage[];
  tasks?: ChatTask[];
  sendError?: string;
}): ChatController {
  const controller = new ChatController();
  controller.initializing = false;
  controller.email = options.email ?? 'lin@aether.design';
  controller.token = options.token === undefined ? 'design-preview-token' : options.token;
  controller.connected = options.connected ?? true;
  controller.desktopAvailable = options.desktopAvailable ?? true;
  controller.devLoginAvailable = options.devLoginAvailable ?? false;
  controller.turnstileSiteKey = options.turnstileSiteKey ?? '';
  controller.mainThinking = options.mainThinking ?? false;
  controller.mainFailed = options.mainFailed ?? false;
  controller.hasMoreHistory = options.hasMoreHistory ?? false;
  controller.error = options.error ?? null;
  controller.messages = options.messages ?? dailyMessages;
  controller.tasks = options.tasks ?? [];

  // 画布内点击操作都在内存里即时回应，避免触发真实 WebSocket 超时计时器。
  controller.requestCode = async () => {};
  controller.signIn = async () => {};
  controller.devSignIn = async () => {};
  controller.signOut = async () => {};
  controller.loadHistory = async () => {};
  controller.uploadImage = async (file: File) => ({
    name: file.name,
    url: 'https://aether.forkicks.fun/images/design-preview.png',
    storage_url:
      'oss://aether-bucket/aether-chat/01960000-0000-7000-8000-000000000001/01960000-0000-7000-8000-000000000002.png',
    size: Math.max(1, file.size),
    mime_type: 'image/png',
  });
  controller.uploadFile = async (file: File) => ({
    name: file.name,
    url: 'https://aether.forkicks.fun/files/design-preview',
    storage_url:
      'oss://aether-bucket/aether-chat/01960000-0000-7000-8000-000000000001/files/01960000-0000-7000-8000-000000000003',
    size: Math.max(1, file.size),
    mime_type: file.type || 'application/pdf',
  });
  controller.send = async () => {
    if (options.sendError) throw new Error(options.sendError);
    return true;
  };
  controller.searchMessages = async (keyword: string) => ({
    type: 'search_results',
    request_id: '01960000-0000-7000-8000-000000000901',
    next_before: '01960000-0000-7000-8000-000000000101',
    results: [
      {
        id: '01960000-0000-7000-8000-000000000202',
        sender: 'main',
        created_at: '2026-11-11T14:20:00+08:00',
        excerpt: `已完成 ${keyword} 策略巡检：每日凌晨 03:30 自动导出压缩快照并校验主密钥完整性。`,
      },
      {
        id: '01960000-0000-7000-8000-000000000201',
        sender: 'user',
        created_at: '2026-11-09T19:05:00+08:00',
        excerpt: `顺便看下生产环境的数据库 ${keyword} 脚本放在哪个目录，恢复演练要用。`,
      },
    ],
  });
  controller.loadMessageContext = async () => {
    const contextMessages: ChatPagePayload['messages'] = [
      {
        id: '01960000-0000-7000-8000-000000000301',
        position: '01960000-0000-7000-8000-000000000301',
        sender: 'user',
        created_at: '2026-11-11T14:18:10+08:00',
        text: '现在生产环境 PostgreSQL 的定时备份和连接上限是怎么配的？',
      },
      {
        id: '01960000-0000-7000-8000-000000000302',
        position: '01960000-0000-7000-8000-000000000302',
        sender: 'main',
        created_at: '2026-11-11T14:20:00+08:00',
        text: '当前单容器对 PostgreSQL 默认保持最多 20 条连接，聊天实时推送已迁至 Redis Pub/Sub；备份由 `scripts/backup-prod.sh` 负责，与加密主密钥分开保存。',
      },
      {
        id: '01960000-0000-7000-8000-000000000303',
        position: '01960000-0000-7000-8000-000000000303',
        sender: 'user',
        created_at: '2026-11-11T14:21:15+08:00',
        text: '明白了，那恢复的时候先跑迁移还是直接导入快照？',
      },
    ];
    return {
      type: 'message_context',
      request_id: '01960000-0000-7000-8000-000000000902',
      messages: contextMessages,
      has_more_before: true,
      has_more_after: true,
    };
  };
  return controller;
}

// installTurnstileStub 在登录预览画板中提供静态 Turnstile 占位框，避免请求外部验证脚本。
function installTurnstileStub() {
  if (!document.querySelector('script[data-aether-turnstile]')) {
    const marker = document.createElement('script');
    marker.dataset.aetherTurnstile = '';
    document.head.append(marker);
  }
  window.turnstile = {
    render(container, options) {
      container.innerHTML = '';
      const badge = document.createElement('div');
      badge.style.cssText =
        'display:flex;align-items:center;justify-content:space-between;padding:10px 12px;border-radius:10px;border:1px solid var(--line);background:var(--surface-muted);font-size:13px;line-height:18px;color:var(--text-secondary);';
      badge.innerHTML = '<span>✓ 安全验证已通过</span><span style="opacity:.7">Turnstile</span>';
      container.append(badge);
      options.callback('design-turnstile-token');
      return 'design-widget';
    },
    remove() {},
  };
}

// installDesktopSocketStub 只拦截 /sandbox/desktop 连接以模拟桌面状态，其余连接原样放行给 Rsbuild 热更新。
function installDesktopSocketStub(mode: 'paused' | 'error') {
  const OriginalWebSocket = window.WebSocket;
  class MockDesktopSocket extends EventTarget {
    binaryType: BinaryType = 'blob';
    readyState: number = OriginalWebSocket.CONNECTING;
    onmessage: ((event: MessageEvent<unknown>) => void) | null = null;

    constructor() {
      super();
      queueMicrotask(() => {
        if (mode === 'error') {
          this.readyState = OriginalWebSocket.CLOSED;
          this.dispatchEvent(new CloseEvent('close', { code: 1006 }));
          return;
        }
        this.readyState = OriginalWebSocket.OPEN;
        this.dispatchEvent(new Event('open'));
      });
    }

    send() {
      if (mode === 'paused') {
        queueMicrotask(() => {
          this.onmessage?.(
            new MessageEvent('message', {
              data: JSON.stringify({ type: 'state', state: 'paused' }),
            }),
          );
        });
      }
    }

    close() {
      this.readyState = OriginalWebSocket.CLOSED;
    }
  }

  const PatchedWebSocket = function (url: string | URL, protocols?: string | string[]) {
    if (String(url).includes('/sandbox/desktop')) {
      return new MockDesktopSocket() as unknown as WebSocket;
    }
    return protocols !== undefined
      ? new OriginalWebSocket(url, protocols)
      : new OriginalWebSocket(url);
  };
  Object.assign(PatchedWebSocket, OriginalWebSocket);
  PatchedWebSocket.prototype = OriginalWebSocket.prototype;
  window.WebSocket = PatchedWebSocket as unknown as typeof WebSocket;
}

// LoginCodeScene 在挂载后自动走完发码一步，让画板直接停留在真实的 6 位验证码输入态。
function LoginCodeScene() {
  const controllerRef = useRef<ChatController | null>(null);
  if (!controllerRef.current) {
    installTurnstileStub();
    controllerRef.current = createPreviewController({
      token: null,
      email: 'lin@aether.design',
      turnstileSiteKey: 'design-site-key',
    });
  }
  useEffect(() => {
    const timer = requestAnimationFrame(() => {
      const form = document.querySelector<HTMLFormElement>('form.login-card');
      form?.requestSubmit();
    });
    return () => cancelAnimationFrame(timer);
  }, []);
  return <LoginPage controller={controllerRef.current} />;
}

// MultimodalComposerScene 通过公开的引用插入句柄与文件拖放路径，还原文字、引用与文件混排草稿。
function MultimodalComposerScene() {
  const composerRef = useRef<MessageComposerHandle | null>(null);
  const controllerRef = useRef<ChatController | null>(null);
  if (!controllerRef.current) {
    sessionStorage.setItem(
      'aether_draft:multimodal@aether.design',
      '帮我对照这份迁移方案核对两点：\n1. 双实例滚动发布期间旧连接如何排空',
    );
    controllerRef.current = createPreviewController({
      email: 'multimodal@aether.design',
      desktopAvailable: true,
    });
  }
  useEffect(() => {
    composerRef.current?.insertQuote({
      message_id: '01960000-0000-7000-8000-000000000105',
      text: '旧容器收到停止信号后立刻释放了 Redis 租约',
      start: 5,
      end: 27,
    });
    const form = document.querySelector<HTMLFormElement>('form.multimodal-composer');
    if (form) {
      const transfer = new DataTransfer();
      transfer.items.add(
        new File(['-- migration preview\nSELECT 1;\n'], '00055_check_lease.sql', {
          type: 'application/sql',
        }),
      );
      form.dispatchEvent(new DragEvent('drop', { bubbles: true, dataTransfer: transfer }));
    }
  }, []);
  return (
    <main className="chat-page">
      <ChatMessageList
        controller={controllerRef.current}
        latestRequest={0}
        onQuote={(quote) => composerRef.current?.insertQuote(quote)}
      />
      <MessageComposer controller={controllerRef.current} composerRef={composerRef} />
    </main>
  );
}

// SendErrorComposerScene 触发一次真实的发送异常，展示输入框保留草稿并在上方提示重发的状态。
function SendErrorComposerScene() {
  const controllerRef = useRef<ChatController | null>(null);
  if (!controllerRef.current) {
    sessionStorage.setItem(
      'aether_draft:send-error@aether.design',
      '把刚才核对好的服务器费用明细整理成一页摘要发给我',
    );
    controllerRef.current = createPreviewController({
      email: 'send-error@aether.design',
      sendError: '连接超时，请检查网络后重试',
    });
  }
  useEffect(() => {
    const timer = requestAnimationFrame(() => {
      document.querySelector<HTMLFormElement>('form.multimodal-composer')?.requestSubmit();
    });
    return () => cancelAnimationFrame(timer);
  }, []);
  return <ChatPage controller={controllerRef.current} />;
}

// AccountPopoverScene 自动点开右上角账号入口，定格展示邮箱信息与「退出登录」浮层。
function AccountPopoverScene() {
  const controllerRef = useRef<ChatController | null>(null);
  if (!controllerRef.current) {
    controllerRef.current = createPreviewController({
      email: 'maintainer@aether.forkicks.fun',
      desktopAvailable: true,
    });
  }
  useEffect(() => {
    document.querySelector<HTMLButtonElement>('.chat-account-launch')?.click();
  }, []);
  return <ChatPage controller={controllerRef.current} />;
}

// SearchOverlayScene 打开搜索模态框并填入关键词，展示命中结果列表与关键词高亮样式。
function SearchOverlayScene() {
  const controllerRef = useRef<ChatController | null>(null);
  if (!controllerRef.current) {
    controllerRef.current = createPreviewController({});
  }
  useEffect(() => {
    const input = document.querySelector<HTMLInputElement>('.history-search-input input');
    if (!input) return;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set;
    setter?.call(input, '备份');
    input.dispatchEvent(new Event('input', { bubbles: true }));
  }, []);
  return (
    <>
      <ChatPage controller={controllerRef.current} />
      <ChatHistorySearch
        controller={controllerRef.current}
        onClose={() => {}}
        onLatest={() => {}}
      />
    </>
  );
}

// ContextOverlayScene 在原生搜索模态框内直接展示命中消息的上下文浏览窗口与锚点标记。
function ContextOverlayScene() {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const controllerRef = useRef<ChatController | null>(null);
  if (!controllerRef.current) {
    controllerRef.current = createPreviewController({});
  }
  useLayoutEffect(() => {
    const dialog = dialogRef.current;
    if (dialog && !dialog.open) dialog.showModal();
    return () => dialog?.close();
  }, []);
  return (
    <>
      <ChatPage controller={controllerRef.current} />
      <dialog
        ref={dialogRef}
        className="history-search-dialog is-browsing"
        aria-label="查看历史消息上下文"
      >
        <ChatHistoryContext
          controller={controllerRef.current}
          messageID="01960000-0000-7000-8000-000000000302"
          keyword="PostgreSQL"
          onBack={() => {}}
          onClose={() => {}}
          onLatest={() => {}}
        />
      </dialog>
    </>
  );
}

// buildSceneElement 根据场景 ID 返回对应的真实组件树，保证画板渲染与线上 DOM 结构一致。
function buildSceneElement(sceneId: string) {
  switch (sceneId) {
    case 'loading':
      return <LoadingScreen />;

    case 'login-email': {
      installTurnstileStub();
      const controller = createPreviewController({
        token: null,
        email: '',
        turnstileSiteKey: 'design-site-key',
      });
      return <LoginPage controller={controller} />;
    }

    case 'login-code':
      return <LoginCodeScene />;

    case 'login-dev': {
      const controller = createPreviewController({
        token: null,
        devLoginAvailable: true,
      });
      return <LoginPage controller={controller} />;
    }

    case 'login-offline': {
      installTurnstileStub();
      const controller = createPreviewController({
        token: null,
        email: 'lin@aether.design',
        turnstileSiteKey: 'design-site-key',
        error: '当前处于离线状态，恢复网络后即可登录',
      });
      return <LoginPage controller={controller} />;
    }

    case 'chat-daily': {
      sessionStorage.removeItem('aether_draft:daily@aether.design');
      const controller = createPreviewController({
        email: 'daily@aether.design',
        hasMoreHistory: true,
        desktopAvailable: true,
        messages: dailyMessages,
      });
      return <ChatPage controller={controller} />;
    }

    case 'chat-markdown': {
      sessionStorage.removeItem('aether_draft:markdown@aether.design');
      const controller = createPreviewController({
        email: 'markdown@aether.design',
        messages: [
          createMessage(
            201,
            true,
            '2026-11-11T10:12:00+08:00',
            '把双实例切换的关键参数和健康检查代码整理给我看下。',
          ),
          createMessage(202, false, '2026-11-11T10:12:18+08:00', markdownSampleText),
        ],
      });
      return <ChatPage controller={controller} />;
    }

    case 'chat-collapsed': {
      sessionStorage.removeItem('aether_draft:collapsed@aether.design');
      const controller = createPreviewController({
        email: 'collapsed@aether.design',
        messages: [
          createMessage(
            301,
            true,
            '2026-11-11T11:00:00+08:00',
            '详细讲讲为什么持久状态全部放在 PostgreSQL，而 Redis 只传信号？',
          ),
          createMessage(302, false, '2026-11-11T11:00:20+08:00', longCollapsedText),
          createMessage(
            303,
            true,
            '2026-11-11T11:03:00+08:00',
            '清楚了，长回答自动折起来确实不会一下子刷满整屏。',
          ),
        ],
      });
      return <ChatPage controller={controller} />;
    }

    case 'chat-attachments': {
      sessionStorage.removeItem('aether_draft:attachments@aether.design');
      const controller = createPreviewController({
        email: 'attachments@aether.design',
        messages: [
          createMessage(
            401,
            true,
            '2026-11-11T15:10:00+08:00',
            '按这张架构草图和原始账单帮我导出一份汇总表。',
            [
              {
                type: 'quote',
                quote: {
                  message_id: '01960000-0000-7000-8000-000000000102',
                  text: '周五 14:00 的提醒，到时会把上季度的费用对比表一并找出来给你。',
                  start: 0,
                  end: 31,
                },
              },
              {
                type: 'image',
                image: {
                  name: 'architecture-overview.svg',
                  url: sampleSketchDataUrl,
                  storage_url:
                    'oss://aether-bucket/aether-chat/01960000-0000-7000-8000-000000000001/01960000-0000-7000-8000-000000000011.png',
                  size: 184320,
                  mime_type: 'image/png',
                },
              },
              {
                type: 'file',
                file: {
                  name: 'cloud-billing-2026Q3.csv',
                  url: 'https://aether.forkicks.fun/files/cloud-billing-2026Q3.csv',
                  storage_url:
                    'oss://aether-bucket/aether-chat/01960000-0000-7000-8000-000000000001/files/01960000-0000-7000-8000-000000000012',
                  size: 482900,
                  mime_type: 'text/csv',
                },
              },
              {
                type: 'text',
                text: '按这张架构草图和原始账单帮我导出一份汇总表。',
              },
            ],
          ),
          createMessage(
            402,
            false,
            '2026-11-11T15:11:05+08:00',
            '已在工作区核对完毕并生成对比报表（下方同时展示正常可下载卡片与签名过期占位态）：',
            [
              {
                type: 'file',
                file: {
                  name: 'aether-cost-comparison-2026Q3.xlsx',
                  url: 'https://aether.forkicks.fun/files/aether-cost-comparison-2026Q3.xlsx',
                  storage_url:
                    'oss://aether-bucket/aether-chat/01960000-0000-7000-8000-000000000001/files/01960000-0000-7000-8000-000000000013',
                  size: 2516582,
                  mime_type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
                },
              },
              {
                type: 'file',
                file: {
                  name: 'expired-archive-snapshot.tar.gz',
                  url: '',
                  storage_url:
                    'oss://aether-bucket/aether-chat/01960000-0000-7000-8000-000000000001/files/01960000-0000-7000-8000-000000000014',
                  size: 14920192,
                  mime_type: 'application/gzip',
                },
              },
            ],
          ),
        ],
      });
      return <ChatPage controller={controller} />;
    }

    case 'composer-multimodal':
      return <MultimodalComposerScene />;

    case 'composer-send-error':
      return <SendErrorComposerScene />;

    case 'status-thinking': {
      sessionStorage.removeItem('aether_draft:thinking@aether.design');
      const controller = createPreviewController({
        email: 'thinking@aether.design',
        connected: true,
        mainThinking: true,
        messages: [
          ...dailyMessages.slice(2),
          createMessage(
            106,
            true,
            '2026-11-11T09:43:10+08:00',
            '顺便去工作区跑一下最新的静态检查，看有没有告警。',
          ),
        ],
      });
      return <ChatPage controller={controller} />;
    }

    case 'status-tasks': {
      sessionStorage.removeItem('aether_draft:tasks@aether.design');
      const controller = createPreviewController({
        email: 'tasks@aether.design',
        connected: true,
        mainThinking: true,
        tasks: sampleTasks,
        messages: [
          ...dailyMessages.slice(2),
          createMessage(
            106,
            true,
            '2026-11-12T10:35:00+08:00',
            '去工作区跑一下静态检查，另外把三季度账单对比表也排上。',
          ),
        ],
      });
      return <ChatPage controller={controller} />;
    }

    case 'status-failed': {
      sessionStorage.removeItem('aether_draft:failed@aether.design');
      const controller = createPreviewController({
        email: 'failed@aether.design',
        connected: true,
        mainFailed: true,
        error: '历史消息加载失败，点击重试',
        messages: dailyMessages.slice(2),
      });
      return <ChatPage controller={controller} />;
    }

    case 'status-disconnected': {
      sessionStorage.setItem(
        'aether_draft:offline@aether.design',
        '网络恢复后帮我查一下下周去杭州的高铁余票',
      );
      const controller = createPreviewController({
        email: 'offline@aether.design',
        connected: false,
        messages: dailyMessages.slice(2),
      });
      return (
        <>
          <ChatPage controller={controller} />
          <button type="button" className="app-button">
            更新应用
          </button>
        </>
      );
    }

    case 'overlay-tasks': {
      sessionStorage.removeItem('aether_draft:overlay-tasks@aether.design');
      const controller = createPreviewController({
        email: 'overlay-tasks@aether.design',
        connected: true,
        mainThinking: true,
        tasks: sampleTasks,
      });
      return (
        <>
          <ChatPage controller={controller} />
          <ChatTaskSheet controller={controller} onClose={() => {}} />
        </>
      );
    }

    case 'overlay-account':
      return <AccountPopoverScene />;

    case 'overlay-search':
      return <SearchOverlayScene />;

    case 'overlay-context':
      return <ContextOverlayScene />;

    case 'overlay-desktop-paused': {
      installDesktopSocketStub('paused');
      sessionStorage.removeItem('aether_draft:desktop-paused@aether.design');
      const controller = createPreviewController({
        email: 'desktop-paused@aether.design',
        desktopAvailable: true,
      });
      return (
        <>
          <ChatPage controller={controller} />
          <DesktopViewer token="design-preview-token" onClose={() => {}} />
        </>
      );
    }

    case 'overlay-desktop-error': {
      installDesktopSocketStub('error');
      sessionStorage.removeItem('aether_draft:desktop-error@aether.design');
      const controller = createPreviewController({
        email: 'desktop-error@aether.design',
        desktopAvailable: true,
      });
      return (
        <>
          <ChatPage controller={controller} />
          <DesktopViewer token="design-preview-token" onClose={() => {}} />
        </>
      );
    }

    default:
      throw new Error(`未知的设计画板场景：${sceneId}`);
  }
}

// renderScene 在独立画板视口中设置显式主题、屏蔽首屏自动抢焦点，并挂载对应场景组件。
export function renderScene(
  container: HTMLElement,
  sceneId: string,
  theme: string | null,
  embedded: boolean,
) {
  if (theme === 'light' || theme === 'dark') {
    document.documentElement.dataset.theme = theme;
  } else {
    delete document.documentElement.dataset.theme;
  }

  // 内嵌在画布 iframe 中时，未点击画板前禁止子组件自动抢焦点，避免把外层画布滚动拉走。
  if (embedded) {
    let activatedByPointer = false;
    window.addEventListener(
      'pointerdown',
      () => {
        activatedByPointer = true;
      },
      { once: true, capture: true },
    );
    const nativeFocus = HTMLElement.prototype.focus;
    HTMLElement.prototype.focus = function (options?: FocusOptions) {
      if (!activatedByPointer) return;
      nativeFocus.call(this, { ...options, preventScroll: true });
    };
  }

  createRoot(container).render(buildSceneElement(sceneId));
}
