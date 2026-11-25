// 本文件是管理看板的数据层：定义后端管理接口的类型与统一请求函数，并提供全看板共用的格式化工具。

// ApiError 带 HTTP 状态码，供页面区分登录过期、权限失效与其他加载失败。
export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

// 当前标签页的管理员令牌只存在内存与 sessionStorage，不写入任何持久存储。
let adminToken: string | null = null;
export function setAdminToken(token: string | null) {
  adminToken = token;
}

// api 统一请求同源 /api 代理：自动附带管理员令牌，失败时把状态码翻译成中文提示。
export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(`/api${path}`, {
    ...options,
    headers: {
      ...(adminToken ? { Authorization: `Bearer ${adminToken}` } : {}),
      ...(options.body ? { 'Content-Type': 'application/json' } : {}),
      ...options.headers,
    },
  });
  if (!response.ok) {
    const message =
      response.status === 401
        ? path === '/admin/login'
          ? '密码错误，请重试。'
          : '登录已过期，请重新登录。'
        : response.status === 403
          ? '管理权限已不可用，请重新登录。'
          : response.status === 429
            ? '请求过于频繁，请稍后重试。'
            : response.status === 502 || response.status === 503
              ? '暂时无法连接后端，请检查服务状态。'
              : response.status === 400
                ? '请求未通过校验，请检查输入或筛选条件。'
                : '加载失败，请稍后重试。';
    throw new ApiError(response.status, message);
  }
  return response.status === 204 ? (null as T) : ((await response.json()) as T);
}

// ---- 后端返回的数据形状（与 backend/internal/httpapi 的管理接口一一对应） ----

export interface SnapshotUser {
  id: string;
  email: string;
  created_at: string;
}

export interface SnapshotAgent {
  user_id: string;
  email: string;
  status: string;
}

export interface SnapshotSubagent {
  id: string;
  title: string;
  status: string;
  error?: string;
  started_at: string;
  finished_at?: string;
}

// SnapshotUsage 是当前筛选窗口内的步骤用量与费用汇总。
export interface SnapshotUsage {
  steps_count: number;
  compact_steps: number;
  failed_steps: number;
  input_tokens: number;
  cached_input_tokens: number;
  cached_measured_input_tokens: number;
  cached_steps: number;
  output_tokens: number;
  cost: string | null;
  cost_steps: number;
}

export interface Snapshot {
  generated_at: string;
  users: { items: SnapshotUser[] };
  agents: { items: SnapshotAgent[] };
  subagents?: { items: SnapshotSubagent[] };
  usage?: SnapshotUsage;
}

// TimelineUsage 是时间线上模型回复或压缩摘要节点关联的单步消耗。
export interface TimelineUsage {
  model: string;
  purpose: string;
  input_tokens: number | null;
  cached_input_tokens: number | null;
  output_tokens: number | null;
  cost: string | null;
}

export interface TimelineToolCall {
  id: string;
  name: string;
  arguments: string;
}

// TimelineItem 是运行过程的一个消息节点。
export interface TimelineItem {
  type: string;
  at: string;
  id?: string;
  sequence?: number;
  role?: string;
  sender?: string | null;
  is_summary?: boolean;
  name?: string;
  text?: string;
  image_count?: number;
  images?: string[];
  reasoning?: { state: string; text?: string };
  tool_calls?: TimelineToolCall[];
  tool_call_id?: string;
  truncated?: boolean;
  usage?: TimelineUsage;
}

export interface Timeline {
  items: TimelineItem[];
  next_cursor: string | null;
  system_prompt: { text: string } | null;
  subagent?: SnapshotSubagent;
}

export interface QuotaItem {
  id: string;
  name: string;
  status: string;
  remaining: number | null;
  used: number | null;
  limit: number | null;
  unit: string;
  detail: string;
}

// RuntimeSettings 是管理看板编辑的全局阈值；版本用于发现其他页面的保存。
export interface RuntimeSettings {
  compact_at_tokens: number;
  hot_compact_at_tokens: number;
  hot_compact_delay_seconds: number;
  reflection_idle_minutes: number;
  version: number;
}

export interface ServiceItem {
  id: string;
  name: string;
  status: string;
  key_suffix: string;
  email_from: string;
  updated_at: string | null;
  oss?: { bucket: string; region: string };
}

// ---- 全看板共用的显示格式化 ----

export const outcomeLabels: Record<string, string> = {
  processing: '处理中',
  running: '运行中',
  completed: '已完成',
  cancelled: '已取消',
  idle: '空闲',
  failed: '失败',
};

export const formatNumber = (value: number | null | undefined) =>
  new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 0 }).format(value ?? 0);
export const formatCompact = (value: number | null | undefined) =>
  new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 }).format(value ?? 0);
export const formatTime = (value: string | null | undefined) =>
  value
    ? new Date(value).toLocaleString('zh-CN', {
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        hour12: false,
      })
    : '—';
export const formatMoney = (value: string | number | null | undefined, compact = false) => {
  if (value == null) return '未知';
  const amount = Number(value);
  const digits = compact && Math.abs(amount) >= 1 ? [2, 4] : [4, 6];
  return `$${amount.toLocaleString('en-US', {
    minimumFractionDigits: digits[0],
    maximumFractionDigits: digits[1],
  })}`;
};
export const shortModel = (value: string | null | undefined) =>
  value?.split('/').slice(1).join('/') || value || '未知';

// stepUsageMeta 把单步模型名、输入/缓存/输出 Token 与费用整理成时间线标题行摘要。
export function stepUsageMeta(usage: TimelineUsage): string {
  const inputText =
    usage.input_tokens == null
      ? '输入 未知'
      : usage.cached_input_tokens != null
        ? usage.input_tokens > 0
          ? `输入 ${formatNumber(usage.input_tokens)}（缓存 ${formatNumber(usage.cached_input_tokens)} / ${((usage.cached_input_tokens / usage.input_tokens) * 100).toFixed(1)}%）`
          : `输入 0（缓存 ${formatNumber(usage.cached_input_tokens)}）`
        : `输入 ${formatNumber(usage.input_tokens)}`;
  const outputText = `输出 ${usage.output_tokens == null ? '未知' : formatNumber(usage.output_tokens)}`;
  const costText = usage.cost == null ? '费用 未知' : formatMoney(usage.cost);
  return `${shortModel(usage.model)} · ${inputText} · ${outputText} · ${costText}`;
}

// quotaAmount 按供应商的计价单位格式化一个额度数字，未知时不显示成 0。
export function quotaAmount(value: number | null, unit: string): string {
  if (value == null) return '未知';
  if (unit === 'USD')
    return `$${Number(value).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
  if (unit === 'CNY')
    return `¥${Number(value).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
  if (unit === 'credits') return `${formatNumber(value)} 次`;
  if (unit === '封/月') return `${formatNumber(value)} 封`;
  if (unit === 'Token') return formatCompact(value);
  return String(value);
}

// quotaHeadline 是额度卡片的大数字：查得到显示剩余，查不到显示原因类别，不伪造数字。
export function quotaHeadline(item: QuotaItem): string {
  if (item.status === 'ok') return quotaAmount(item.remaining, item.unit);
  return (
    {
      unlimited: '不限额',
      not_configured: '未配置',
      unreadable: '无法解密',
      unsupported: '无法查询',
      error: '查询失败',
    }[item.status] || '查询失败'
  );
}

// quotaNote 组合供应商说明与已用／上限，让管理员一眼看出还剩多少、总共多少。
export function quotaNote(item: QuotaItem): string {
  const parts: string[] = [];
  if (item.detail) parts.push(item.detail);
  if (item.status === 'ok' && item.limit != null)
    parts.push(`已用 ${quotaAmount(item.used, item.unit)} / 上限 ${quotaAmount(item.limit, item.unit)}`);
  return parts.join(' · ') || '暂无说明';
}
