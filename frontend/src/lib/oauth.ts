// 本文件封装远程记忆 MCP 的 OAuth 2.1 前端接口：授权上下文校验、授权决策提交以及已授权应用列表与撤销。

import { requestAPI } from './api.js';

// OAuthAuthorizeParams 描述客户端跳转到 /oauth/authorize 时附带的一组查询参数。
export interface OAuthAuthorizeParams {
  response_type: string;
  client_id: string;
  redirect_uri: string;
  state: string;
  scope: string;
  code_challenge: string;
  code_challenge_method: string;
  resource: string;
}

// OAuthAuthorizeContext 描述后端校验通过后的授权展示信息，或需要直接跳回客户端的错误回调地址。
export interface OAuthAuthorizeContext {
  client_name?: string;
  redirect_host?: string;
  scope?: string;
  resource?: string;
  redirect_to?: string;
  error?: string;
}

// OAuthConnectionSummary 描述单个已获授权访问 /memories 的外部 MCP 客户端记录。
export interface OAuthConnectionSummary {
  id: string;
  client_name: string;
  scope: string;
  created_at: string;
  last_used_at: string | null;
}

// OAuthConnectionsPayload 返回当前部署的 MCP 接入地址与该用户已授权的外部应用清单。
export interface OAuthConnectionsPayload {
  mcp_url: string;
  connections: OAuthConnectionSummary[];
}

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

// fetchOAuthAuthorizeContext 向后端核验授权请求参数并读取客户端展示名，不依赖登录态。
export async function fetchOAuthAuthorizeContext(
  search: string,
  signal?: AbortSignal,
): Promise<OAuthAuthorizeContext> {
  const timeout = AbortSignal.timeout(15000);
  const response = await fetch(`/oauth/authorize/context${search}`, {
    headers: { Accept: 'application/json' },
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  });
  const contentType = response.headers.get('content-type') ?? '';
  if (contentType.includes('application/json')) {
    const payload = (await response.json()) as OAuthAuthorizeContext;
    if (!response.ok && !payload.error) {
      throw new Error('授权请求参数无效');
    }
    return payload;
  }
  const message = contentType.startsWith('text/plain') ? (await response.text()).trim() : '';
  throw new Error(message || '无法加载授权信息，请稍后重试');
}

// submitOAuthAuthorizeDecision 携带当前登录令牌向后端提交同意或拒绝，并取回服务端构造好的回调地址。
export async function submitOAuthAuthorizeDecision(
  token: string,
  params: OAuthAuthorizeParams,
  approve: boolean,
): Promise<string> {
  const response = await requestAPI('/oauth/authorize/decision', token, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json',
    },
    body: JSON.stringify({ approve, ...params }),
    fallbackError: '提交授权决策失败，请稍后重试',
  });
  const payload = (await response.json()) as { redirect_to: string };
  return payload.redirect_to;
}

// fetchOAuthConnections 获取当前用户的 MCP 接入地址和已授权外部客户端列表。
export async function fetchOAuthConnections(
  token: string,
  signal?: AbortSignal,
): Promise<OAuthConnectionsPayload> {
  const response = await requestAPI('/oauth/connections', token, {
    headers: { Accept: 'application/json' },
    signal,
    fallbackError: '无法读取已授权应用列表',
  });
  return (await response.json()) as OAuthConnectionsPayload;
}

// revokeOAuthConnection 立即删除一条外部应用授权，使其访问与刷新令牌同时失效。
export async function revokeOAuthConnection(token: string, connectionID: string): Promise<void> {
  await requestAPI(`/oauth/connections/${encodeURIComponent(connectionID)}`, token, {
    method: 'DELETE',
    fallbackError: '撤销授权失败，请稍后重试',
  });
}
