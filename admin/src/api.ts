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

// Page 是游标分页的统一外壳。
export interface Page<T> {
  items: T[];
  next_cursor: string | null;
}

export interface SnapshotUser {
  id: string;
  email: string;
  created_at: string;
  last_activity_at: string | null;
  main_active: boolean;
  attempts: number;
  failed: number;
  input_tokens: number;
  output_tokens: number;
  cost_known_usd: string;
  cost_unknown: number;
}

export interface SnapshotAgent {
  id: string | null;
  user_id: string;
  email: string;
  status: string;
}

export interface Snapshot {
  generated_at: string;
  coverage: { first_observed_at: string | null; legacy_steps: number };
  metrics: {
    attempts: number;
    succeeded: number;
    failed: number;
    in_flight: number;
    interrupted: number;
    input_tokens: number;
    output_tokens: number;
    input_unknown: number;
    output_unknown: number;
    cost_known_usd: string;
    cost_unknown: number;
    mean_duration_ms: number | null;
    duration_samples: number;
  };
  trend: { at: string; attempts: number; succeeded: number; failed: number }[];
  models: { model: string; attempts: number; cost_known_usd: string }[];
  tools: { name: string; count: number }[];
  users: Page<SnapshotUser>;
  agents: Page<SnapshotAgent>;
}

// Call 是单次模型请求尝试的日志行，列表与时间线共用。
export interface Call {
  id: string;
  step_id: string;
  user_id?: string;
  email?: string;
  requested_model: string;
  response_model: string | null;
  outcome: string;
  http_status: number | null;
  purpose: string;
  compact_reason?: string | null;
  context_tokens_before?: number | null;
  summary_through_sequence?: number | null;
  failure_kind: string | null;
  started_at: string;
  finished_at: string | null;
  duration_ms: number | null;
  input_tokens: number | null;
  output_tokens: number | null;
  cost: string | null;
  tool_names?: string[];
}

export interface CallDetail {
  id: string;
  request: unknown;
  response: unknown;
  response_text: string | null;
  response_truncated: boolean;
}

export interface TimelineToolCall {
  id: string;
  name: string;
  arguments: string;
}

// TimelineItem 是运行过程的一个节点：message 来自消息表，call 是未产出消息的模型请求。
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
  call?: Call;
}

export interface Timeline {
  items: TimelineItem[];
  next_cursor: string | null;
  system_prompt: { text: string } | null;
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

export interface ModelSettings {
  compact_at_tokens: number;
  effective_output_limit: number;
  main_model: string;
  version: number;
  updated_at: string | null;
  updated_by: string | null;
  source: string;
}

export interface CatalogModel {
  id: string;
  name: string;
  provider: string;
  context_length: number;
  pricing: { prompt: string; completion: string };
}

export interface ModelCatalog {
  items: CatalogModel[];
  warnings: string[];
  fetched_at: string;
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
  in_flight: '请求中',
  succeeded: '成功',
  http_error: 'HTTP 错误',
  transport_error: '连接异常',
  invalid_response: '响应异常',
  interrupted: '中断 · 结果未知',
  processing: '处理中',
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
export const formatMoney = (value: string | number | null | undefined) =>
  value == null
    ? '未知'
    : `$${Number(value).toLocaleString('en-US', { minimumFractionDigits: 4, maximumFractionDigits: 6 })}`;
export const formatDuration = (value: number | null | undefined) =>
  value == null ? '未知' : value < 1000 ? `${Math.round(value)} ms` : `${(value / 1000).toFixed(2)} s`;
export const shortModel = (value: string | null | undefined) =>
  value?.split('/').slice(1).join('/') || value || '未知';

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

// callMeta 是时间线模型回复节点上的一行请求摘要。
export function callMeta(call: Call): string {
  return `${shortModel(call.response_model || call.requested_model)} · ${call.input_tokens == null ? '未知' : formatNumber(call.input_tokens)} / ${call.output_tokens == null ? '未知' : formatNumber(call.output_tokens)} Token · ${formatMoney(call.cost)} · ${formatDuration(call.duration_ms)}`;
}
