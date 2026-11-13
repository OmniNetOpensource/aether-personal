// 本文件是聊天端活体设计画布主界面：左侧按模块切换分页，展示 palette.css 实时规范与多视口、亮暗并排的真实组件画板。

import { useLayoutEffect, useRef, useState } from 'react';
import '../styles/palette.css';
import './designCanvas.css';

type ViewportPreset = 'mobile' | 'desktop';
type ThemeMode = 'both' | 'light' | 'dark';
type ScalePreset = 0.75 | 1;

// SceneSpec 描述单个界面画板的标识、标题与设计关注点，供画布目录和画板标题栏展示。
interface SceneSpec {
  id: string;
  title: string;
  note: string;
}

// SceneGroup 按功能模块组织设计画布的左侧分页，避免二十多个画板同屏堆叠难以巡检。
interface SceneGroup {
  id: 'tokens' | 'auth' | 'conversation' | 'composer' | 'overlays';
  title: string;
  subtitle: string;
  scenes: SceneSpec[];
}

const sceneGroups: SceneGroup[] = [
  {
    id: 'tokens',
    title: '配色与字排规范',
    subtitle: '实时读取 palette.css 的森林绿亮暗双套变量、文字层级与排版标尺。',
    scenes: [],
  },
  {
    id: 'auth',
    title: '启动与登录',
    subtitle: '连接握手加载态、生产邮箱验证码流程、开发测试登录与离线报错反馈。',
    scenes: [
      {
        id: 'loading',
        title: '首屏连接加载态',
        note: 'WebSocket 尚未收到 ready 时的 aether 角色呼吸轻弹、眨眼与暖金连接波纹动画。',
      },
      {
        id: 'login-email',
        title: '邮箱登录 · 获取验证码',
        note: '生产默认入口，包含品牌图标、邮箱输入框与 Turnstile 验证位。',
      },
      {
        id: 'login-code',
        title: '邮箱登录 · 输入 6 位验证码',
        note: '发码成功后锁定邮箱，展示数字验证码输入与更换邮箱入口。',
      },
      {
        id: 'login-dev',
        title: '本地开发一键登录',
        note: 'APP_ENV=dev 下省略邮箱与验证码，一键进入测试会话。',
      },
      {
        id: 'login-offline',
        title: '登录异常与离线提示',
        note: '未缓存登录态离线打开或验证码错误时的卡片内告警样式。',
      },
    ],
  },
  {
    id: 'conversation',
    title: '对话与消息形态',
    subtitle: '时间分隔、两分钟连续气泡分组、Markdown 全语法、长消息折叠与多类型附件。',
    scenes: [
      {
        id: 'chat-daily',
        title: '日常多轮对话与气泡衔接',
        note: '展示跨日/半小时时间分隔、同说话人两分钟内紧凑圆角衔接及右上角悬浮入口。',
      },
      {
        id: 'chat-markdown',
        title: 'Markdown 富文本 · 代码 / 表格 / 公式',
        note: '覆盖标题层级、任务列表、横向滚动表格、KaTeX 公式与 Shiki 双主题高亮代码块。',
      },
      {
        id: 'chat-collapsed',
        title: '超长消息自动折叠（360px 阈值）',
        note: '超过 360px 渲染高度的正文自动裁切并渐隐，底部提供「展开全文 / 收起」切换。',
      },
      {
        id: 'chat-attachments',
        title: '引用回看、图片缩略图与工作区文件卡片',
        note: '同时对比有效图片/可下载文件卡片与签名过期失效态的视觉呈现。',
      },
    ],
  },
  {
    id: 'composer',
    title: '输入框与运行状态',
    subtitle: '多模态混排草稿、发送失败保留草稿、思考动画、回复失败、断线重连与版本更新。',
    scenes: [
      {
        id: 'composer-multimodal',
        title: '多模态混排输入草稿',
        note: '输入框内混排引用芯片、多行正文与紧凑文件芯片，工具栏固定在胶囊底部。',
      },
      {
        id: 'composer-send-error',
        title: '发送失败 · 保留草稿待重发',
        note: '消息列表不展示失败回执；错误提示浮在输入框上方，原内容保留供直接重发。',
      },
      {
        id: 'status-thinking',
        title: 'aether 正在处理（思考动态）',
        note: 'Main 运行期间在消息列表尾部展示三点呼吸动画。',
      },
      {
        id: 'status-failed',
        title: '回复失败与历史加载重试',
        note: '页头历史拉取失败重试条与页尾本轮模型回复失败提示。',
      },
      {
        id: 'status-disconnected',
        title: '断线重连提示与 PWA 待更新按钮',
        note: '连接断开时的输入框上方状态提示，以及右下角新版 Service Worker 更新入口。',
      },
    ],
  },
  {
    id: 'overlays',
    title: '浮层与弹窗',
    subtitle: '右上角账号菜单、历史关键词搜索、原文上下文定位窗口与只读实时桌面。',
    scenes: [
      {
        id: 'overlay-account',
        title: '账号与退出登录浮层',
        note: '点击右上角头像按钮展开当前登录邮箱与退出操作气泡。',
      },
      {
        id: 'overlay-search',
        title: '搜索历史聊天 · 关键词高亮列表',
        note: '原生模态对话框展示命中摘要、关键词 mark 高亮与游标分页按钮。',
      },
      {
        id: 'overlay-context',
        title: '历史消息原文上下文窗口',
        note: '独立于主聊天列表定位命中消息，支持向前后双向翻页与一键回到最新聊天。',
      },
      {
        id: 'overlay-desktop-paused',
        title: '查看电脑 · 休眠等待态',
        note: '沙箱电脑休眠时的只读观察窗口，说明下次使用时自动恢复。',
      },
      {
        id: 'overlay-desktop-error',
        title: '查看电脑 · 画面连接中断态',
        note: '桌面流断开时的窗口内状态提示与「重新连接」操作按钮。',
      },
    ],
  },
];

// TokenItem 描述一个 CSS 颜色变量的名称与界面职责，实际色值在挂载后从 palette.css 实时读取。
interface TokenItem {
  name: string;
  usage: string;
}

// TokenSection 将 palette.css 中的全部颜色变量按视觉职责归类，方便核对亮暗映射是否成对。
interface TokenSection {
  title: string;
  items: TokenItem[];
}

const tokenSections: TokenSection[] = [
  {
    title: '品牌森林绿与暖金强调',
    items: [
      { name: '--forest', usage: '应用底色、加载页背景' },
      { name: '--forest-deep', usage: '深层底色、按钮悬停加深' },
      { name: '--forest-hover', usage: '森林绿表面悬停态' },
      { name: '--forest-border', usage: '深绿背景上的描边' },
      { name: '--on-forest', usage: '森林绿背景上的主文字与图标' },
      { name: '--on-forest-muted', usage: '森林绿背景上的次级说明文字' },
      { name: '--gold', usage: '品牌主操作按钮、键盘焦点环' },
      { name: '--gold-soft', usage: '柔和暖金底色、搜索命中高亮底' },
      { name: '--on-gold', usage: '暖金按钮上的深绿文字' },
    ],
  },
  {
    title: '画布、卡片表面与聊天气泡',
    items: [
      { name: '--canvas', usage: '聊天消息区画布底色' },
      { name: '--surface', usage: '助手消息气泡、输入框、登录卡片与弹窗表面' },
      { name: '--surface-muted', usage: '代码块头部、行内代码与次级填充块' },
      { name: '--user-bubble', usage: '用户发出的消息气泡底色' },
      { name: '--quote-surface', usage: '气泡内嵌引用块的半透明衬底' },
      { name: '--shadow', usage: '浮层、卡片与悬浮按钮投影色' },
    ],
  },
  {
    title: '文字层级与分隔线条',
    items: [
      { name: '--text', usage: '正文与主要标题文字' },
      { name: '--text-secondary', usage: '次要正文、工具栏说明' },
      { name: '--text-muted', usage: '时间戳、占位符与弱提示' },
      { name: '--accent-text', usage: '正文链接、附件图标与高亮强调字' },
      { name: '--line', usage: '常规卡片、表格与输入框边框' },
      { name: '--line-strong', usage: '悬停边框、引用竖线与分割线' },
    ],
  },
  {
    title: '异常反馈与状态提示',
    items: [
      { name: '--error', usage: '表单、卡片与列表内的错误文字' },
      { name: '--on-forest-error', usage: '深绿背景上的高对比错误提示字' },
      { name: '--error-soft', usage: '错误横幅与告警块柔和底色' },
      { name: '--error-chart', usage: '图表或辅助异常指示色' },
      { name: '--warning', usage: '需要注意但非阻断的提醒色' },
    ],
  },
];

const viewportDimensions: Record<ViewportPreset, { label: string; width: number; height: number }> =
  {
    mobile: { label: '手机 390×844', width: 390, height: 844 },
    desktop: { label: '桌面 1120×720', width: 1120, height: 720 },
  };

// TokensSpecView 通过隐藏探针节点读取 palette.css 在亮暗主题下的真实计算值，同时展示字号与行高标尺。
function TokensSpecView() {
  const lightProbeRef = useRef<HTMLDivElement>(null);
  const darkProbeRef = useRef<HTMLDivElement>(null);
  const [resolvedValues, setResolvedValues] = useState<
    Record<string, { light: string; dark: string }>
  >({});

  useLayoutEffect(() => {
    const lightElement = lightProbeRef.current;
    const darkElement = darkProbeRef.current;
    if (!lightElement || !darkElement) return;
    const lightStyles = getComputedStyle(lightElement);
    const darkStyles = getComputedStyle(darkElement);
    const nextValues: Record<string, { light: string; dark: string }> = {};
    for (const section of tokenSections) {
      for (const item of section.items) {
        nextValues[item.name] = {
          light: lightStyles.getPropertyValue(item.name).trim(),
          dark: darkStyles.getPropertyValue(item.name).trim(),
        };
      }
    }
    setResolvedValues(nextValues);
  }, []);

  return (
    <div className="dc-tokens-page">
      <div ref={lightProbeRef} data-theme="light" className="dc-style-probe" aria-hidden="true" />
      <div ref={darkProbeRef} data-theme="dark" className="dc-style-probe" aria-hidden="true" />

      {tokenSections.map((section) => (
        <section key={section.title} className="dc-section">
          <h2 className="dc-section-title">{section.title}</h2>
          <div className="dc-token-grid">
            {section.items.map((token) => {
              const pair = resolvedValues[token.name];
              return (
                <article key={token.name} className="dc-token-card">
                  <div className="dc-token-swatches">
                    <div className="dc-swatch-cell" data-theme="light">
                      <span
                        className="dc-swatch-fill"
                        style={{ background: `var(${token.name})` }}
                      />
                      <span className="dc-swatch-tag">亮色 {pair?.light ?? ''}</span>
                    </div>
                    <div className="dc-swatch-cell" data-theme="dark">
                      <span
                        className="dc-swatch-fill"
                        style={{ background: `var(${token.name})` }}
                      />
                      <span className="dc-swatch-tag">暗色 {pair?.dark ?? ''}</span>
                    </div>
                  </div>
                  <div className="dc-token-meta">
                    <code className="dc-token-name">{token.name}</code>
                    <p className="dc-token-usage">{token.usage}</p>
                  </div>
                </article>
              );
            })}
          </div>
        </section>
      ))}

      <section className="dc-section">
        <h2 className="dc-section-title">字体、字号与阅读节奏（亮色 / 暗色真实表面对照）</h2>
        <div className="dc-type-columns">
          {(['light', 'dark'] as const).map((theme) => (
            <div key={theme} data-theme={theme} className="dc-type-sheet">
              <header className="dc-type-sheet-header">
                <span>{theme === 'light' ? '亮色表面（--surface）' : '暗色表面（--surface）'}</span>
                <code>system-ui, PingFang SC, Microsoft YaHei</code>
              </header>
              <div className="dc-type-row">
                <span className="dc-type-spec">登录主标题 · 28px / 36px · 700</span>
                <p style={{ fontSize: 28, lineHeight: '36px', fontWeight: 700 }}>和 aether 聊聊</p>
              </div>
              <div className="dc-type-row">
                <span className="dc-type-spec">Markdown 二级标题 · 26px（1.45em）· 700</span>
                <p style={{ fontSize: '1.45em', lineHeight: 1.3, fontWeight: 700 }}>
                  双实例切换核对清单
                </p>
              </div>
              <div className="dc-type-row">
                <span className="dc-type-spec">聊天正文与输入框 · 18px / 28px · 400</span>
                <p style={{ fontSize: 18, lineHeight: '28px' }}>
                  持久状态全部保存在 PostgreSQL，同机纯内存 Redis 只传递跨实例唤醒信号与图片缓存。
                </p>
              </div>
              <div className="dc-type-row">
                <span className="dc-type-spec">附件卡片标题 · 15px / 22px · 500</span>
                <p style={{ fontSize: 15, lineHeight: '22px', fontWeight: 500 }}>
                  aether-cost-comparison-2026Q3.xlsx
                </p>
              </div>
              <div className="dc-type-row">
                <span className="dc-type-spec">时间分隔与辅助说明 · 13px / 18px · 次要文字</span>
                <p style={{ fontSize: 13, lineHeight: '18px', color: 'var(--text-muted)' }}>
                  2026年11月11日 09:41 · 2.4 MB · 仅搜索你可见的聊天内容
                </p>
              </div>
              <div className="dc-type-row">
                <span className="dc-type-spec">行内与围栏代码 · 16px · ui-monospace</span>
                <p>
                  <code className="dc-inline-code-sample">
                    GET /healthz -&gt; 200 ok (PostgreSQL + Redis)
                  </code>
                </p>
              </div>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}

// ArtboardFrame 使用固定像素尺寸的同源 iframe 隔离每个场景，确保媒体查询、fixed 布局和原生模态框真实生效。
function ArtboardFrame({
  sceneId,
  title,
  theme,
  width,
  height,
  scale,
}: {
  sceneId: string;
  title: string;
  theme: 'light' | 'dark';
  width: number;
  height: number;
  scale: ScalePreset;
}) {
  const url = `/?scene=${encodeURIComponent(sceneId)}&theme=${theme}`;
  return (
    <div className="dc-frame-unit">
      <div className="dc-frame-bar">
        <span className={`dc-theme-badge dc-theme-badge--${theme}`}>
          {theme === 'light' ? '亮色 Light' : '暗色 Dark'}
        </span>
        <span className="dc-frame-size">
          {width} × {height}
        </span>
        <a
          className="dc-open-link"
          href={url}
          target="_blank"
          rel="noopener noreferrer"
          title="在独立标签页中按全屏尺寸交互预览"
        >
          单独打开 ↗
        </a>
      </div>
      <div
        className="dc-frame-stage"
        style={{
          width: Math.round(width * scale),
          height: Math.round(height * scale),
        }}
      >
        <iframe
          key={`${sceneId}:${theme}`}
          src={url}
          title={`${title}（${theme === 'light' ? '亮色' : '暗色'}）`}
          width={width}
          height={height}
          style={
            scale === 1
              ? undefined
              : {
                  transform: `scale(${scale})`,
                  transformOrigin: 'top left',
                }
          }
        />
      </div>
    </div>
  );
}

// DesignCanvas 组织左侧模块目录、顶部视口/主题工具栏与右侧画板舞台。
export function DesignCanvas() {
  const [activeGroupId, setActiveGroupId] = useState<SceneGroup['id']>('conversation');
  const [viewport, setViewport] = useState<ViewportPreset>('mobile');
  const [themeMode, setThemeMode] = useState<ThemeMode>('both');
  const [scale, setScale] = useState<ScalePreset>(1);
  const workspaceRef = useRef<HTMLElement>(null);

  const activeGroup = sceneGroups.find((group) => group.id === activeGroupId) ?? sceneGroups[0];
  const preset = viewportDimensions[viewport];
  const themes: ('light' | 'dark')[] = themeMode === 'both' ? ['light', 'dark'] : [themeMode];

  const selectGroup = (groupId: SceneGroup['id']) => {
    setActiveGroupId(groupId);
    workspaceRef.current?.scrollTo({ top: 0 });
  };

  return (
    <div className="dc-shell">
      <aside className="dc-sidebar" aria-label="设计画布分组导航">
        <div className="dc-brand">
          <img src="/app-icon.png" alt="aether" width="40" height="40" />
          <div>
            <h1>aether · 设计画布</h1>
            <p>真实组件与样式 · 免后端预览</p>
          </div>
        </div>

        <nav className="dc-nav">
          {sceneGroups.map((group, index) => {
            const countLabel = group.id === 'tokens' ? '25 变量' : `${group.scenes.length} 画板`;
            const active = group.id === activeGroup.id;
            return (
              <button
                key={group.id}
                type="button"
                className={`dc-nav-item${active ? ' is-active' : ''}`}
                onClick={() => selectGroup(group.id)}
              >
                <span className="dc-nav-index">{String(index + 1).padStart(2, '0')}</span>
                <span className="dc-nav-title">{group.title}</span>
                <span className="dc-nav-count">{countLabel}</span>
              </button>
            );
          })}
        </nav>

        <footer className="dc-sidebar-note">
          画板直接挂载 <code>frontend/src/components</code> 与 <code>palette.css</code>
          ，修改样式或组件后自动同步更新。
        </footer>
      </aside>

      <main ref={workspaceRef} className="dc-workspace">
        <header className="dc-toolbar">
          <div className="dc-toolbar-heading">
            <h2>{activeGroup.title}</h2>
            <p>{activeGroup.subtitle}</p>
          </div>

          {activeGroup.id !== 'tokens' && (
            <div className="dc-controls" role="toolbar" aria-label="画板显示控制">
              <div className="dc-segmented" role="group" aria-label="视口尺寸">
                {(Object.keys(viewportDimensions) as ViewportPreset[]).map((key) => (
                  <button
                    key={key}
                    type="button"
                    className={viewport === key ? 'is-selected' : undefined}
                    onClick={() => {
                      setViewport(key);
                      if (key === 'desktop' && themeMode === 'both') setScale(0.75);
                      if (key === 'mobile') setScale(1);
                    }}
                  >
                    {viewportDimensions[key].label}
                  </button>
                ))}
              </div>

              <div className="dc-segmented" role="group" aria-label="外观主题">
                <button
                  type="button"
                  className={themeMode === 'both' ? 'is-selected' : undefined}
                  onClick={() => setThemeMode('both')}
                >
                  亮暗对照
                </button>
                <button
                  type="button"
                  className={themeMode === 'light' ? 'is-selected' : undefined}
                  onClick={() => setThemeMode('light')}
                >
                  仅亮色
                </button>
                <button
                  type="button"
                  className={themeMode === 'dark' ? 'is-selected' : undefined}
                  onClick={() => setThemeMode('dark')}
                >
                  仅暗色
                </button>
              </div>

              <div className="dc-segmented" role="group" aria-label="画板缩放">
                <button
                  type="button"
                  className={scale === 0.75 ? 'is-selected' : undefined}
                  onClick={() => setScale(0.75)}
                >
                  75%
                </button>
                <button
                  type="button"
                  className={scale === 1 ? 'is-selected' : undefined}
                  onClick={() => setScale(1)}
                >
                  100%
                </button>
              </div>
            </div>
          )}
        </header>

        {activeGroup.id === 'tokens' ? (
          <TokensSpecView />
        ) : (
          <div className="dc-scene-list">
            {activeGroup.scenes.map((scene) => (
              <section key={scene.id} className="dc-scene-card">
                <div className="dc-scene-header">
                  <div>
                    <h3>{scene.title}</h3>
                    <p>{scene.note}</p>
                  </div>
                  <code className="dc-scene-id">{scene.id}</code>
                </div>
                <div className="dc-artboard-row">
                  {themes.map((theme) => (
                    <ArtboardFrame
                      key={theme}
                      sceneId={scene.id}
                      title={scene.title}
                      theme={theme}
                      width={preset.width}
                      height={preset.height}
                      scale={scale}
                    />
                  ))}
                </div>
              </section>
            ))}
          </div>
        )}
      </main>
    </div>
  );
}
