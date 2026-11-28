// 本文件组装管理看板的 Effect Runtime 与全局 Atom 状态树：统一管理登录鉴权、运行过程轮询与配置页三组数据及写操作。

import { Effect, Layer, Schedule } from 'effect';
import { AsyncResult, Atom, AtomRegistry } from 'effect/reactivity';
import {
  AdminApi,
  AdminApiLive,
  AdminAuthError,
  AdminForbiddenError,
  AdminLoginSchema,
  AdminMeSchema,
  AdminTokenStorage,
  AdminTokenStorageLive,
  ChatGPTAuthStartSchema,
  ChatGPTStatusSchema,
  ModelProviderSchema,
  ModelProvidersPayloadSchema,
  QuotasPayloadSchema,
  RuntimeSettingsSchema,
  ServicesPayloadSchema,
  SnapshotSchema,
  TimelineItemSchema,
  TimelineSchema,
  formatTime,
} from './api.js';
import type {
  ModelProvider,
  ModelProviderProtocol,
  QuotaItem,
  RuntimeSettings,
  ServiceItem,
  Snapshot,
  Timeline,
  TimelineItem,
} from './api.js';

export type AdminTab = 'timeline' | 'settings';

// TimelineFilters 区分编辑中的条件与已经提交的查询，自动刷新和翻页只使用已提交值。
export interface TimelineFilters {
  readonly hours: number;
  readonly user: string;
  readonly subagent: string;
}

export const defaultFilters: TimelineFilters = { hours: 24, user: '', subagent: '' };

// AdminAccessState 描述看板当前的访问阶段：启动核验、未登录、无权限或已登录。
export type AdminAccessState =
  | { readonly kind: 'booting' }
  | { readonly kind: 'logged_out' }
  | { readonly kind: 'denied'; readonly message: string }
  | { readonly kind: 'authenticated'; readonly name: string };

// AdminLive 组装管理看板的纯 Effect 服务层。
export const AdminLive = AdminApiLive.pipe(Layer.provideMerge(AdminTokenStorageLive));
export const adminRuntime = Atom.runtime(AdminLive);

// ---- 全局状态 Atom ----

export const accessStateAtom = Atom.make<AdminAccessState>({ kind: 'booting' });
export const activeTabAtom = Atom.make<AdminTab>('timeline');
export const draftFiltersAtom = Atom.make<TimelineFilters>(defaultFilters);
export const submittedFiltersAtom = Atom.make<TimelineFilters>(defaultFilters);
export const knownUsersAtom = Atom.make<ReadonlyMap<string, string>>(new Map());
export const snapshotDataAtom = Atom.make<Snapshot | null>(null);
export const timelineDataAtom = Atom.make<(Timeline & { readonly key: string }) | null>(null);
export const timelineFullItemsAtom = Atom.make<ReadonlyMap<string, TimelineItem>>(new Map());
export const timelineWindowAtom = Atom.make<{ readonly from: string; readonly to: string } | null>(
  null,
);
export const timelineLoadErrorAtom = Atom.make('');
export const browsingPagesAtom = Atom.make(false);
export const updatedTextAtom = Atom.make('尚未刷新');
export const toastMessageAtom = Atom.make('');

// resetDashboardData 清空当前账号在内存中的所有看板缓存与筛选条件。
function resetDashboardData(registry: AtomRegistry.AtomRegistry) {
  registry.set(snapshotDataAtom, null);
  registry.set(timelineDataAtom, null);
  registry.set(timelineFullItemsAtom, new Map());
  registry.set(draftFiltersAtom, defaultFilters);
  registry.set(submittedFiltersAtom, defaultFilters);
  registry.set(knownUsersAtom, new Map());
  registry.set(timelineWindowAtom, null);
  registry.set(timelineLoadErrorAtom, '');
  registry.set(updatedTextAtom, '尚未刷新');
  registry.set(browsingPagesAtom, false);
}

// applyAccessLost 集中处理 401 令牌失效回登录页与 403 无权访问页。
function applyAccessLost(
  registry: AtomRegistry.AtomRegistry,
  error: AdminAuthError | AdminForbiddenError,
) {
  return Effect.gen(function* () {
    const tokenStorage = yield* AdminTokenStorage;
    resetDashboardData(registry);
    if (error._tag === 'AdminAuthError') {
      yield* tokenStorage.setToken(null);
      registry.set(accessStateAtom, { kind: 'logged_out' });
    } else {
      registry.set(accessStateAtom, { kind: 'denied', message: error.message });
    }
  });
}

// buildTimelineQuery 为查询与翻页共用同一时间窗口和已提交条件。
function buildTimelineQuery(
  filters: TimelineFilters,
  windowRange: { readonly from: string; readonly to: string } | null,
  extra: Readonly<Record<string, string | null>> = {},
): string {
  const params = new URLSearchParams(windowRange ?? {});
  if (filters.user) params.set('user_id', filters.user);
  if (filters.subagent) params.set('subagent_id', filters.subagent);
  for (const [key, value] of Object.entries(extra)) {
    if (value != null) params.set(key, value);
  }
  return params.toString();
}

// notifyToastAtom 展示右下角提示并在 4 秒后自动收起；新提示到来自动中断旧计时。
export const notifyToastAtom = adminRuntime.fn<string>()((message, get) =>
  Effect.gen(function* () {
    get.set(toastMessageAtom, message);
    yield* Effect.sleep('4 seconds');
    get.set(toastMessageAtom, '');
  }),
);

// ---- 认证会话动作 ----

// bootstrapSessionAtom 启动时若存在标签页令牌则校验 /admin/me，失效则清理回登录页。
export const bootstrapSessionAtom = adminRuntime.atom((get) =>
  Effect.gen(function* () {
    const tokenStorage = yield* AdminTokenStorage;
    const api = yield* AdminApi;
    const token = yield* tokenStorage.getToken;
    if (!token) {
      get.set(accessStateAtom, { kind: 'logged_out' });
      return;
    }
    const result = yield* api.getJson('/admin/me', AdminMeSchema).pipe(Effect.result);
    if (result._tag === 'Success') {
      get.set(accessStateAtom, { kind: 'authenticated', name: result.success.name });
    } else {
      yield* tokenStorage.setToken(null);
      get.set(accessStateAtom, { kind: 'logged_out' });
    }
  }),
);

// loginAdminAtom 提交管理员密码换取令牌并立即核验 /admin/me。
export const loginAdminAtom = adminRuntime.fn<string>()((password, get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    const tokenStorage = yield* AdminTokenStorage;
    const verified = yield* api.postJson('/admin/login', { password }, AdminLoginSchema);
    yield* tokenStorage.setToken(verified.token);
    const me = yield* api.getJson('/admin/me', AdminMeSchema);
    get.set(accessStateAtom, { kind: 'authenticated', name: me.name });
    return me;
  }),
);

// logoutAdminAtom 撤销后端管理会话并重置本地看板状态；除已过期外的错误留在原页。
export const logoutAdminAtom = adminRuntime.fn((_arg: void, get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    const tokenStorage = yield* AdminTokenStorage;
    yield* api.postVoid('/admin/logout').pipe(Effect.catchTag('AdminAuthError', () => Effect.void));
    resetDashboardData(get.registry);
    yield* tokenStorage.setToken(null);
    get.set(accessStateAtom, { kind: 'logged_out' });
  }),
);

// ---- 运行过程（Snapshot + Timeline） ----

// refreshTimelineAtom 并发拉取 snapshot 与当前选中用户的时间线；新刷新自动中断旧在途请求。
export const refreshTimelineAtom = adminRuntime.fn((_arg: void, get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    const filters = get(submittedFiltersAtom);
    get.set(browsingPagesAtom, false);

    const now = new Date();
    const windowRange = {
      from: new Date(now.getTime() - filters.hours * 3600000).toISOString(),
      to: now.toISOString(),
    };
    get.set(timelineWindowAtom, windowRange);

    const snapshotQuery = new URLSearchParams(windowRange);
    if (filters.user) snapshotQuery.set('user_id', filters.user);
    if (filters.user && filters.subagent) snapshotQuery.set('subagent_id', filters.subagent);
    const timelineQuery = buildTimelineQuery(filters, windowRange);

    const [loadedSnapshot, loadedTimeline] = yield* Effect.all(
      [
        api.getJson(`/admin/snapshot?${snapshotQuery}`, SnapshotSchema),
        filters.user
          ? api.getJson(`/admin/timeline?${timelineQuery}`, TimelineSchema)
          : Effect.succeed(null),
      ],
      { concurrency: 'unbounded' },
    ).pipe(
      Effect.tapError((error) => {
        if (error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError') {
          return applyAccessLost(get.registry, error);
        }
        get.set(timelineLoadErrorAtom, error.message);
        return Effect.void;
      }),
    );

    get.set(snapshotDataAtom, loadedSnapshot);
    const nextUsers = new Map(get(knownUsersAtom));
    for (const item of loadedSnapshot.users.items) {
      nextUsers.set(item.id, item.email);
    }
    get.set(knownUsersAtom, nextUsers);
    get.set(
      timelineDataAtom,
      loadedTimeline ? { ...loadedTimeline, key: `${filters.user}:${filters.subagent}` } : null,
    );
    get.set(updatedTextAtom, `${formatTime(loadedSnapshot.generated_at)} 已更新`);
    get.set(timelineLoadErrorAtom, '');
  }),
);

// loadEarlierTimelineAtom 按 next_cursor 向前翻一页时间线并暂停自动刷新，避免覆盖已加载历史。
export const loadEarlierTimelineAtom = adminRuntime.fn((_arg: void, get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    const currentTimeline = get(timelineDataAtom);
    const filters = get(submittedFiltersAtom);
    const windowRange = get(timelineWindowAtom);
    if (!currentTimeline?.next_cursor) return;

    const query = buildTimelineQuery(filters, windowRange, { before: currentTimeline.next_cursor });
    const pageData = yield* api
      .getJson(`/admin/timeline?${query}`, TimelineSchema)
      .pipe(
        Effect.tapError((error) =>
          error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
            ? applyAccessLost(get.registry, error)
            : Effect.void,
        ),
      );

    const latestTimeline = get(timelineDataAtom);
    if (!latestTimeline || latestTimeline.key !== currentTimeline.key) return;
    get.set(timelineDataAtom, {
      ...latestTimeline,
      items: [...pageData.items, ...latestTimeline.items],
      next_cursor: pageData.next_cursor,
    });
    get.set(browsingPagesAtom, true);
  }),
);

// loadTimelineMessageAtom 按条拉取截断消息的完整文本与签名截图，写入保留 Map 跨自动刷新复用。
export const loadTimelineMessageAtom = adminRuntime.fn<string>()(
  (id, get) =>
    Effect.gen(function* () {
      const api = yield* AdminApi;
      const full = yield* api
        .getJson(`/admin/timeline/messages/${id}`, TimelineItemSchema)
        .pipe(
          Effect.tapError((error) =>
            error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
              ? applyAccessLost(get.registry, error)
              : Effect.void,
          ),
        );
      const nextFull = new Map(get(timelineFullItemsAtom));
      nextFull.set(id, full);
      get.set(timelineFullItemsAtom, nextFull);
      return full;
    }),
  { concurrent: true },
);

// timelineAutoRefreshAtom 在登录态下每 15 秒刷新运行过程，并在页面回到前台时补刷一次；卸载自动清理。
export const timelineAutoRefreshAtom = adminRuntime.atom((get) =>
  Effect.gen(function* () {
    const registry = get.registry;
    const triggerIfActive = () => {
      if (
        document.hidden ||
        registry.get(activeTabAtom) === 'settings' ||
        registry.get(browsingPagesAtom) ||
        document.querySelector('input:focus')
      ) {
        return;
      }
      registry.set(refreshTimelineAtom, undefined);
    };

    yield* Effect.acquireRelease(
      Effect.sync(() => {
        const onVisibility = () => {
          if (
            !document.hidden &&
            registry.get(activeTabAtom) === 'timeline' &&
            !registry.get(browsingPagesAtom)
          ) {
            registry.set(refreshTimelineAtom, undefined);
          }
        };
        document.addEventListener('visibilitychange', onVisibility);
        return () => document.removeEventListener('visibilitychange', onVisibility);
      }),
      (cleanup) => Effect.sync(cleanup),
    );

    yield* Effect.repeat(
      Effect.sleep('15 seconds').pipe(Effect.andThen(Effect.sync(triggerIfActive))),
      Schedule.spaced('0 millis'),
    );
  }),
);

// ---- 配置页：全局运行参数、ChatGPT 订阅、自定义模型供应商、服务凭证与额度 ----

export const chatGPTStatusAtom = adminRuntime.atom((get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    const loaded = yield* api
      .getJson('/admin/chatgpt', ChatGPTStatusSchema)
      .pipe(
        Effect.tapError((error) =>
          error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
            ? applyAccessLost(get.registry, error)
            : Effect.void,
        ),
      );
    get.set(updatedTextAtom, `${formatTime(new Date().toISOString())} 已更新`);
    return loaded;
  }),
);

export const startChatGPTAuthAtom = adminRuntime.fn((_arg: void, get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    return yield* api
      .postJson('/admin/chatgpt/start', {}, ChatGPTAuthStartSchema)
      .pipe(
        Effect.tapError((error) =>
          error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
            ? applyAccessLost(get.registry, error)
            : Effect.void,
        ),
      );
  }),
);

export const completeChatGPTAuthAtom = adminRuntime.fn<string>()((callbackURL, get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    const connected = yield* api
      .postJson('/admin/chatgpt/complete', { callback_url: callbackURL }, ChatGPTStatusSchema)
      .pipe(
        Effect.tapError((error) =>
          error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
            ? applyAccessLost(get.registry, error)
            : Effect.void,
        ),
      );
    get.refresh(chatGPTStatusAtom);
    return connected;
  }),
);

export const disconnectChatGPTAtom = adminRuntime.fn((_arg: void, get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    yield* api
      .deleteVoid('/admin/chatgpt')
      .pipe(
        Effect.tapError((error) =>
          error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
            ? applyAccessLost(get.registry, error)
            : Effect.void,
        ),
      );
    get.refresh(chatGPTStatusAtom);
  }),
);

export const runtimeSettingsAtom = adminRuntime.atom((get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    const loaded = yield* api
      .getJson('/admin/runtime-settings', RuntimeSettingsSchema)
      .pipe(
        Effect.tapError((error) =>
          error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
            ? applyAccessLost(get.registry, error)
            : Effect.void,
        ),
      );
    get.set(updatedTextAtom, `${formatTime(new Date().toISOString())} 已更新`);
    return loaded;
  }),
);

export const saveRuntimeSettingsAtom = adminRuntime.fn<RuntimeSettings>()((draft, get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    const saved = yield* api
      .putJson('/admin/runtime-settings', draft, RuntimeSettingsSchema)
      .pipe(
        Effect.tapError((error) =>
          error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
            ? applyAccessLost(get.registry, error)
            : Effect.void,
        ),
      );
    get.refresh(runtimeSettingsAtom);
    return saved;
  }),
);

export const modelProvidersAtom = adminRuntime.atom((get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    const payload = yield* api
      .getJson('/admin/model-providers', ModelProvidersPayloadSchema)
      .pipe(
        Effect.tapError((error) =>
          error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
            ? applyAccessLost(get.registry, error)
            : Effect.void,
        ),
      );
    get.set(updatedTextAtom, `${formatTime(new Date().toISOString())} 已更新`);
    return payload.items;
  }),
);

export const createModelProviderAtom = adminRuntime.fn<{
  readonly name: string;
  readonly protocol: ModelProviderProtocol;
  readonly base_url: string;
  readonly api_key: string;
}>()((input, get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    const created = yield* api
      .postJson('/admin/model-providers', input, ModelProviderSchema)
      .pipe(
        Effect.tapError((error) =>
          error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
            ? applyAccessLost(get.registry, error)
            : Effect.void,
        ),
      );
    get.refresh(modelProvidersAtom);
    return created;
  }),
);

export const updateModelProviderAtom = adminRuntime.fn<{
  readonly id: string;
  readonly name: string;
  readonly protocol: ModelProviderProtocol;
  readonly base_url: string;
  readonly api_key: string | null;
}>()((input, get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    const updated = yield* api
      .putJson(
        `/admin/model-providers/${input.id}`,
        {
          name: input.name,
          protocol: input.protocol,
          base_url: input.base_url,
          api_key: input.api_key,
        },
        ModelProviderSchema,
      )
      .pipe(
        Effect.tapError((error) =>
          error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
            ? applyAccessLost(get.registry, error)
            : Effect.void,
        ),
      );
    get.refresh(modelProvidersAtom);
    return updated;
  }),
);

export const deleteModelProviderAtom = adminRuntime.fn<string>()((id, get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    yield* api
      .deleteVoid(`/admin/model-providers/${id}`)
      .pipe(
        Effect.tapError((error) =>
          error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
            ? applyAccessLost(get.registry, error)
            : Effect.void,
        ),
      );
    get.refresh(modelProvidersAtom);
  }),
);

// ServiceSettingsData 合并服务凭证列表与可选额度结果；额度查询失败不阻断凭证管理。
export interface ServiceSettingsData {
  readonly items: readonly ServiceItem[];
  readonly quotas: readonly QuotaItem[] | null;
}

export const serviceSettingsAtom = adminRuntime.atom((get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    const [config, quotas] = yield* Effect.all(
      [
        api.getJson('/admin/services', ServicesPayloadSchema),
        api.getJson('/admin/quotas', QuotasPayloadSchema).pipe(
          Effect.map((payload) => payload.items),
          Effect.catchIf(
            (error) => error._tag !== 'AdminAuthError' && error._tag !== 'AdminForbiddenError',
            () => Effect.succeed(null),
          ),
        ),
      ],
      { concurrency: 'unbounded' },
    ).pipe(
      Effect.tapError((error) =>
        error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
          ? applyAccessLost(get.registry, error)
          : Effect.void,
      ),
    );
    get.set(updatedTextAtom, `${formatTime(new Date().toISOString())} 已更新`);
    return { items: config.items, quotas } satisfies ServiceSettingsData;
  }),
);

export const saveServiceSettingAtom = adminRuntime.fn<{
  readonly serviceID: string;
  readonly body: Readonly<Record<string, unknown>>;
}>()((input, get) =>
  Effect.gen(function* () {
    const api = yield* AdminApi;
    yield* api
      .putVoid(`/admin/services/${input.serviceID}`, input.body)
      .pipe(
        Effect.tapError((error) =>
          error._tag === 'AdminAuthError' || error._tag === 'AdminForbiddenError'
            ? applyAccessLost(get.registry, error)
            : Effect.void,
        ),
      );
    get.refresh(serviceSettingsAtom);
  }),
);

// settingsBusyAtom 聚合配置页各组异步任务的加载态，驱动顶部统一刷新按钮的禁用状态。
export const settingsBusyAtom = Atom.make((get) => {
  const runtime = get(runtimeSettingsAtom);
  const chatGPT = get(chatGPTStatusAtom);
  const providers = get(modelProvidersAtom);
  const services = get(serviceSettingsAtom);
  const savingRuntime = get(saveRuntimeSettingsAtom);
  return (
    AsyncResult.isWaiting(runtime) ||
    AsyncResult.isWaiting(chatGPT) ||
    AsyncResult.isWaiting(providers) ||
    AsyncResult.isWaiting(services) ||
    AsyncResult.isWaiting(savingRuntime)
  );
});

export type { ModelProvider };
