// 本文件用 Effect Schema 与 ApiClient 封装远程记忆 MCP 的 OAuth 2.1 前端接口：授权上下文校验、授权决策提交及已授权应用管理。

import { Effect, Schema } from 'effect';
import { HttpBody } from 'effect/http';
import { ApiClient } from './api.js';
import type { ApiError } from './api.js';
import { AuthError, DecodeError, HttpApiError, NetworkError } from './errors.js';

// OAuthAuthorizeParams 描述客户端跳转到 /oauth/authorize 时附带的一组查询参数。
export interface OAuthAuthorizeParams {
  readonly response_type: string;
  readonly client_id: string;
  readonly redirect_uri: string;
  readonly state: string;
  readonly scope: string;
  readonly code_challenge: string;
  readonly code_challenge_method: string;
  readonly resource: string;
}

// OAuthAuthorizeContextSchema 描述后端校验通过后的授权展示信息，或需要直接跳回客户端的错误回调地址。
export const OAuthAuthorizeContextSchema = Schema.Struct({
  client_name: Schema.optional(Schema.String),
  redirect_host: Schema.optional(Schema.String),
  scope: Schema.optional(Schema.String),
  resource: Schema.optional(Schema.String),
  redirect_to: Schema.optional(Schema.String),
  error: Schema.optional(Schema.String),
});
export type OAuthAuthorizeContext = typeof OAuthAuthorizeContextSchema.Type;

// OAuthConnectionSummarySchema 描述单个已获授权访问 /memories 的外部 MCP 客户端记录。
export const OAuthConnectionSummarySchema = Schema.Struct({
  id: Schema.String,
  client_name: Schema.String,
  scope: Schema.String,
  created_at: Schema.String,
  last_used_at: Schema.NullOr(Schema.String),
});
export type OAuthConnectionSummary = typeof OAuthConnectionSummarySchema.Type;

// OAuthConnectionsPayloadSchema 返回当前部署的 MCP 接入地址与该用户已授权的外部应用清单。
export const OAuthConnectionsPayloadSchema = Schema.Struct({
  mcp_url: Schema.String,
  connections: Schema.Array(OAuthConnectionSummarySchema),
});
export type OAuthConnectionsPayload = typeof OAuthConnectionsPayloadSchema.Type;

const OAuthDecisionPayloadSchema = Schema.Struct({
  redirect_to: Schema.String,
});

// readOAuthAuthorizeParams 从浏览器当前查询串提取标准 OAuth 2.1 授权请求参数。
export function readOAuthAuthorizeParams(search: string): OAuthAuthorizeParams {
  const query = new URLSearchParams(search);
  return {
    response_type: query.get('response_type') ?? '',
    client_id: query.get('client_id') ?? '',
    redirect_uri: query.get('redirect_uri') ?? '',
    state: query.get('state') ?? '',
    scope: query.get('scope') ?? '',
    code_challenge: query.get('code_challenge') ?? '',
    code_challenge_method: query.get('code_challenge_method') ?? '',
    resource: query.get('resource') ?? '',
  };
}

// fetchOAuthAuthorizeContextEffect 向后端核验授权请求参数并读取客户端展示名，不依赖登录态。
export function fetchOAuthAuthorizeContextEffect(
  search: string,
): Effect.Effect<OAuthAuthorizeContext, HttpApiError | NetworkError | DecodeError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    const result = yield* api.requestPublicJson(
      `/oauth/authorize/context${search}`,
      OAuthAuthorizeContextSchema,
      { fallbackError: '无法加载授权信息，请稍后重试' },
    );
    if (!result.ok && !result.data.error) {
      return yield* Effect.fail(
        new HttpApiError({ status: result.status, message: '授权请求参数无效' }),
      );
    }
    return result.data;
  });
}

// submitOAuthAuthorizeDecisionEffect 携带当前登录令牌向后端提交同意或拒绝，并取回服务端构造好的回调地址。
export function submitOAuthAuthorizeDecisionEffect(
  token: string,
  params: OAuthAuthorizeParams,
  approve: boolean,
): Effect.Effect<string, ApiError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    const payload = yield* api.requestJson(
      '/oauth/authorize/decision',
      token,
      OAuthDecisionPayloadSchema,
      {
        method: 'POST',
        headers: { Accept: 'application/json' },
        body: HttpBody.jsonUnsafe({ approve, ...params }),
        fallbackError: '提交授权决策失败，请稍后重试',
      },
    );
    return payload.redirect_to;
  });
}

// fetchOAuthConnectionsEffect 获取当前用户的 MCP 接入地址和已授权外部客户端列表。
export function fetchOAuthConnectionsEffect(
  token: string,
): Effect.Effect<OAuthConnectionsPayload, ApiError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    return yield* api.requestJson('/oauth/connections', token, OAuthConnectionsPayloadSchema, {
      headers: { Accept: 'application/json' },
      fallbackError: '无法读取已授权应用列表',
    });
  });
}

// revokeOAuthConnectionEffect 立即删除一条外部应用授权，使其访问与刷新令牌同时失效。
export function revokeOAuthConnectionEffect(
  token: string,
  connectionID: string,
): Effect.Effect<void, AuthError | HttpApiError | NetworkError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    yield* api.requestVoid(`/oauth/connections/${encodeURIComponent(connectionID)}`, token, {
      method: 'DELETE',
      fallbackError: '撤销授权失败，请稍后重试',
    });
  });
}
