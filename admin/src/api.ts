// 本文件是管理看板的纯 Effect 数据层：用 Schema 定义后端管理接口结构、用 TaggedError 分流权限与请求错误，并提供共用格式化工具。

import { Cause, Context, Data, Effect, Layer, Option, Schema } from 'effect';
import { FetchHttpClient, HttpBody, HttpClient, HttpClientRequest } from 'effect/http';

// ---- 类型化错误（替代手写 ApiError 与状态码散落判断） ----

export class AdminAuthError extends Data.TaggedError('AdminAuthError')<{
  readonly message: string;
}> {}

export class AdminForbiddenError extends Data.TaggedError('AdminForbiddenError')<{
  readonly message: string;
}> {}

export class AdminHttpError extends Data.TaggedError('AdminHttpError')<{
  readonly status: number;
  readonly message: string;
}> {}

export class AdminNetworkError extends Data.TaggedError('AdminNetworkError')<{
  readonly message: string;
  readonly cause?: unknown;
}> {}

export class AdminDecodeError extends Data.TaggedError('AdminDecodeError')<{
  readonly message: string;
  readonly cause?: unknown;
}> {}

export type AdminError =
  | AdminAuthError
  | AdminForbiddenError
  | AdminHttpError
  | AdminNetworkError
  | AdminDecodeError;

// extractAccessLost 从 Effect Cause 中识别 401/403 权限失效错误。
export function extractAccessLost(
  cause: Cause.Cause<unknown>,
): AdminAuthError | AdminForbiddenError | null {
  const failure = Cause.findErrorOption(cause);
  if (Option.isNone(failure)) return null;
  if (failure.value instanceof AdminAuthError || failure.value instanceof AdminForbiddenError) {
    return failure.value;
  }
  return null;
}

// extractAdminHttpError 从 Effect Cause 中提取带 HTTP 状态码的普通请求错误。
export function extractAdminHttpError(cause: Cause.Cause<unknown>): AdminHttpError | null {
  const failure = Cause.findErrorOption(cause);
  if (Option.isSome(failure) && failure.value instanceof AdminHttpError) {
    return failure.value;
  }
  return null;
}

// formatAdminCause 将 Effect Cause 转换为适合管理看板直接展示的中文提示。
export function formatAdminCause(
  cause: Cause.Cause<unknown>,
  fallback = '加载失败，请稍后重试。',
): string {
  const failure = Cause.findErrorOption(cause);
  const value = Option.isSome(failure) ? failure.value : Cause.squash(cause);
  if (typeof value === 'object' && value !== null && 'message' in value) {
    const message = (value as { readonly message?: unknown }).message;
    if (typeof message === 'string' && message.trim()) return message;
  }
  return fallback;
}

// ---- 后端返回的数据 Schema（与 backend/internal/httpapi 的管理接口一一对应） ----

export const AdminMeSchema = Schema.Struct({
  name: Schema.String,
});
export type AdminMe = typeof AdminMeSchema.Type;

export const AdminLoginSchema = Schema.Struct({
  token: Schema.String,
});

export const SnapshotUserSchema = Schema.Struct({
  id: Schema.String,
  email: Schema.String,
  created_at: Schema.String,
});
export type SnapshotUser = typeof SnapshotUserSchema.Type;

export const SnapshotAgentSchema = Schema.Struct({
  user_id: Schema.String,
  email: Schema.String,
  status: Schema.String,
});
export type SnapshotAgent = typeof SnapshotAgentSchema.Type;

export const SnapshotSubagentSchema = Schema.Struct({
  id: Schema.String,
  title: Schema.String,
  status: Schema.String,
  error: Schema.optional(Schema.String),
  started_at: Schema.String,
  finished_at: Schema.optional(Schema.String),
});
export type SnapshotSubagent = typeof SnapshotSubagentSchema.Type;

// SnapshotUsageSchema 是当前筛选窗口内的步骤用量与费用汇总。
export const SnapshotUsageSchema = Schema.Struct({
  steps_count: Schema.Number,
  compact_steps: Schema.Number,
  failed_steps: Schema.Number,
  input_tokens: Schema.Number,
  cached_input_tokens: Schema.Number,
  cached_measured_input_tokens: Schema.Number,
  cached_steps: Schema.Number,
  output_tokens: Schema.Number,
  cost: Schema.NullOr(Schema.String),
  cost_steps: Schema.Number,
});
export type SnapshotUsage = typeof SnapshotUsageSchema.Type;

export const SnapshotSchema = Schema.Struct({
  generated_at: Schema.String,
  users: Schema.Struct({ items: Schema.Array(SnapshotUserSchema) }),
  agents: Schema.Struct({ items: Schema.Array(SnapshotAgentSchema) }),
  subagents: Schema.optional(Schema.Struct({ items: Schema.Array(SnapshotSubagentSchema) })),
  usage: Schema.optional(SnapshotUsageSchema),
});
export type Snapshot = typeof SnapshotSchema.Type;

// TimelineUsageSchema 是时间线上模型回复或压缩摘要节点关联的单步消耗。
export const TimelineUsageSchema = Schema.Struct({
  model: Schema.String,
  purpose: Schema.String,
  input_tokens: Schema.NullOr(Schema.Number),
  cached_input_tokens: Schema.NullOr(Schema.Number),
  output_tokens: Schema.NullOr(Schema.Number),
  cost: Schema.NullOr(Schema.String),
});
export type TimelineUsage = typeof TimelineUsageSchema.Type;

export const TimelineToolCallSchema = Schema.Struct({
  id: Schema.String,
  name: Schema.String,
  arguments: Schema.String,
});
export type TimelineToolCall = typeof TimelineToolCallSchema.Type;

// TimelineItemSchema 是运行过程的一个消息节点。
export const TimelineItemSchema = Schema.Struct({
  type: Schema.String,
  at: Schema.String,
  id: Schema.optional(Schema.String),
  sequence: Schema.optional(Schema.Number),
  role: Schema.optional(Schema.String),
  sender: Schema.optional(Schema.NullOr(Schema.String)),
  is_summary: Schema.optional(Schema.Boolean),
  name: Schema.optional(Schema.String),
  text: Schema.optional(Schema.String),
  image_count: Schema.optional(Schema.Number),
  images: Schema.optional(Schema.Array(Schema.String)),
  reasoning: Schema.optional(
    Schema.Struct({
      state: Schema.String,
      text: Schema.optional(Schema.String),
    }),
  ),
  tool_calls: Schema.optional(Schema.Array(TimelineToolCallSchema)),
  tool_call_id: Schema.optional(Schema.String),
  truncated: Schema.optional(Schema.Boolean),
  usage: Schema.optional(TimelineUsageSchema),
});
export type TimelineItem = typeof TimelineItemSchema.Type;

export const TimelineSchema = Schema.Struct({
  items: Schema.Array(TimelineItemSchema),
  next_cursor: Schema.NullOr(Schema.String),
  system_prompt: Schema.NullOr(Schema.Struct({ text: Schema.String })),
  subagent: Schema.optional(SnapshotSubagentSchema),
});
export type Timeline = typeof TimelineSchema.Type;

export const QuotaItemSchema = Schema.Struct({
  id: Schema.String,
  name: Schema.String,
  status: Schema.String,
  remaining: Schema.NullOr(Schema.Number),
  used: Schema.NullOr(Schema.Number),
  limit: Schema.NullOr(Schema.Number),
  unit: Schema.String,
  detail: Schema.String,
});
export type QuotaItem = typeof QuotaItemSchema.Type;

export const QuotasPayloadSchema = Schema.Struct({
  items: Schema.Array(QuotaItemSchema),
});

// RuntimeSettingsSchema 是管理看板编辑的全局阈值；版本用于发现其他页面的保存。
export const RuntimeSettingsSchema = Schema.Struct({
  compact_at_tokens: Schema.Number,
  hot_compact_at_tokens: Schema.Number,
  version: Schema.Number,
});
export type RuntimeSettings = typeof RuntimeSettingsSchema.Type;

export const ModelProviderProtocolSchema = Schema.Literals([
  'openai',
  'openai_responses',
  'gemini',
  'claude',
]);
export type ModelProviderProtocol = typeof ModelProviderProtocolSchema.Type;

export const ChatGPTStatusSchema = Schema.Struct({
  status: Schema.Literals(['not_configured', 'connected', 'unreadable']),
  account_email: Schema.String,
  account_sub: Schema.String,
  access_expires_at: Schema.NullOr(Schema.String),
  updated_at: Schema.NullOr(Schema.String),
});
export type ChatGPTStatus = typeof ChatGPTStatusSchema.Type;

export const ChatGPTAuthStartSchema = Schema.Struct({
  authorize_url: Schema.String,
  redirect_uri: Schema.String,
});
export type ChatGPTAuthStart = typeof ChatGPTAuthStartSchema.Type;

export const ModelProviderSchema = Schema.Struct({
  id: Schema.String,
  name: Schema.String,
  protocol: ModelProviderProtocolSchema,
  base_url: Schema.String,
  status: Schema.String,
  key_suffix: Schema.String,
  created_at: Schema.String,
  updated_at: Schema.String,
});
export type ModelProvider = typeof ModelProviderSchema.Type;

export const ModelProvidersPayloadSchema = Schema.Struct({
  items: Schema.Array(ModelProviderSchema),
});

export const ServiceItemSchema = Schema.Struct({
  id: Schema.String,
  name: Schema.String,
  status: Schema.String,
  key_suffix: Schema.String,
  email_from: Schema.String,
  updated_at: Schema.NullOr(Schema.String),
  oss: Schema.optional(
    Schema.Struct({
      bucket: Schema.String,
      region: Schema.String,
    }),
  ),
});
export type ServiceItem = typeof ServiceItemSchema.Type;

export const ServicesPayloadSchema = Schema.Struct({
  items: Schema.Array(ServiceItemSchema),
});

// ---- 令牌存储与同源 /api 请求服务 ----

const adminTokenStorageKey = 'aether_admin_token';

// AdminTokenStorage 抽象当前标签页管理员令牌的读写，不落盘到任何持久存储。
export class AdminTokenStorage extends Context.Service<
  AdminTokenStorage,
  {
    readonly getToken: Effect.Effect<string | null>;
    readonly setToken: (token: string | null) => Effect.Effect<void>;
  }
>()('@aether-admin/AdminTokenStorage') {}

// AdminTokenStorageLive 使用内存缓存 + sessionStorage 维护当前标签页会话令牌。
export const AdminTokenStorageLive = Layer.sync(AdminTokenStorage, () => {
  let cachedToken: string | null = (() => {
    try {
      return sessionStorage.getItem(adminTokenStorageKey);
    } catch {
      return null;
    }
  })();

  return AdminTokenStorage.of({
    getToken: Effect.sync(() => cachedToken),
    setToken: (token) =>
      Effect.sync(() => {
        cachedToken = token;
        try {
          if (token) sessionStorage.setItem(adminTokenStorageKey, token);
          else sessionStorage.removeItem(adminTokenStorageKey);
        } catch {
          // 禁用 sessionStorage 时仍保留内存中的令牌。
        }
      }),
  });
});

export type AdminJsonDecoder<A = unknown> = Schema.Decoder<A, never>;

// AdminApi 封装全部经 /api 代理的管理接口请求，统一附带管理员令牌、状态码映射与 Schema 解码。
export class AdminApi extends Context.Service<
  AdminApi,
  {
    readonly getJson: <S extends AdminJsonDecoder>(
      path: string,
      schema: S,
    ) => Effect.Effect<S['Type'], AdminError>;
    readonly postJson: <S extends AdminJsonDecoder>(
      path: string,
      body: unknown,
      schema: S,
    ) => Effect.Effect<S['Type'], AdminError>;
    readonly putJson: <S extends AdminJsonDecoder>(
      path: string,
      body: unknown,
      schema: S,
    ) => Effect.Effect<S['Type'], AdminError>;
    readonly postVoid: (path: string) => Effect.Effect<void, AdminError>;
    readonly putVoid: (path: string, body: unknown) => Effect.Effect<void, AdminError>;
    readonly deleteVoid: (path: string) => Effect.Effect<void, AdminError>;
  }
>()('@aether-admin/AdminApi') {}

// statusError 将非 2xx HTTP 状态码映射为结构化的领域错误与中文文案。
function statusError(
  path: string,
  status: number,
): AdminAuthError | AdminForbiddenError | AdminHttpError {
  if (status === 401) {
    return new AdminAuthError({
      message: path === '/admin/login' ? '密码错误，请重试。' : '登录已过期，请重新登录。',
    });
  }
  if (status === 403) {
    return new AdminForbiddenError({ message: '管理权限已不可用，请重新登录。' });
  }
  const message =
    status === 429
      ? '请求过于频繁，请稍后重试。'
      : status === 502 || status === 503
        ? '暂时无法连接后端，请检查服务状态。'
        : status === 400
          ? '请求未通过校验，请检查输入或筛选条件。'
          : '加载失败，请稍后重试。';
  return new AdminHttpError({ status, message });
}

const BrowserHttpClientLive = FetchHttpClient.layer.pipe(
  Layer.provide(Layer.succeed(HttpClient.TracerPropagationEnabled, false)),
);

// AdminApiLive 组合浏览器 FetchHttpClient 与令牌存储，提供完整的管理接口调用能力。
export const AdminApiLive = Layer.effect(
  AdminApi,
  Effect.gen(function* () {
    const httpClient = yield* HttpClient.HttpClient;
    const tokenStorage = yield* AdminTokenStorage;

    const execute = (method: 'GET' | 'POST' | 'PUT' | 'DELETE', path: string, body?: unknown) =>
      Effect.gen(function* () {
        const token = yield* tokenStorage.getToken;
        let request = HttpClientRequest.make(method)(`/api${path}`);
        if (token) {
          request = HttpClientRequest.bearerToken(request, token);
        }
        if (body !== undefined) {
          request = HttpClientRequest.setBody(request, HttpBody.jsonUnsafe(body));
        }

        const response = yield* httpClient.execute(request).pipe(
          Effect.timeoutOrElse({
            duration: '20 seconds',
            orElse: () =>
              Effect.fail(new AdminNetworkError({ message: '请求超时，请检查服务状态。' })),
          }),
          Effect.catchTag('HttpClientError', (cause) =>
            Effect.fail(
              new AdminNetworkError({ message: '暂时无法连接后端，请检查服务状态。', cause }),
            ),
          ),
        );

        if (response.status < 200 || response.status >= 300) {
          return yield* Effect.fail(statusError(path, response.status));
        }
        return response;
      });

    const decodeJson = <S extends AdminJsonDecoder>(
      method: 'GET' | 'POST' | 'PUT',
      path: string,
      schema: S,
      body?: unknown,
    ): Effect.Effect<S['Type'], AdminError> =>
      Effect.gen(function* () {
        const response = yield* execute(method, path, body);
        const payload = yield* response.json.pipe(
          Effect.mapError(
            (cause) => new AdminDecodeError({ message: '响应数据格式无效。', cause }),
          ),
        );
        return yield* Schema.decodeUnknownEffect(schema)(payload).pipe(
          Effect.mapError(
            (cause) => new AdminDecodeError({ message: '响应数据格式无效。', cause }),
          ),
        );
      });

    return AdminApi.of({
      getJson: (path, schema) => decodeJson('GET', path, schema),
      postJson: (path, body, schema) => decodeJson('POST', path, schema, body),
      putJson: (path, body, schema) => decodeJson('PUT', path, schema, body),
      postVoid: (path) => execute('POST', path).pipe(Effect.asVoid),
      putVoid: (path, body) => execute('PUT', path, body).pipe(Effect.asVoid),
      deleteVoid: (path) => execute('DELETE', path).pipe(Effect.asVoid),
    });
  }),
).pipe(Layer.provide(BrowserHttpClientLive));

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
export const shortModel = (value: string | null | undefined) => {
  if (!value) return '未知';
  const withoutProvider = value.replace(/^(?:provider:[0-9a-f-]+:|chatgpt:)/i, '');
  return withoutProvider.split('/').slice(1).join('/') || withoutProvider;
};

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
    parts.push(
      `已用 ${quotaAmount(item.used, item.unit)} / 上限 ${quotaAmount(item.limit, item.unit)}`,
    );
  return parts.join(' · ') || '暂无说明';
}
