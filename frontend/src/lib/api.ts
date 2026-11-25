// 本文件统一封装带会话令牌与超时控制的同源 HTTP 请求，集中处理登录过期与错误文本提取。

// AuthError 标识会话令牌失效，便于页面引导用户返回登录。
export class AuthError extends Error {}

// requestAPI 为同源接口附带 Bearer 认证头与超时限制，并在非成功响应时向上抛出可读错误。
export async function requestAPI(
  path: string,
  token: string,
  options: RequestInit & { timeoutMs?: number; fallbackError?: string } = {},
): Promise<Response> {
  const {
    timeoutMs = 15000,
    fallbackError = '请求失败，请稍后重试',
    signal,
    headers,
    ...rest
  } = options;
  const timeout = AbortSignal.timeout(timeoutMs);
  const response = await fetch(path, {
    ...rest,
    headers: {
      Authorization: 'Bearer ' + token,
      ...headers,
    },
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  });
  if (response.status === 401) {
    throw new AuthError('登录已过期，请重新登录');
  }
  if (!response.ok) {
    const message = response.headers.get('content-type')?.startsWith('text/plain')
      ? (await response.text()).trim()
      : '';
    throw new Error(message || fallbackError);
  }
  return response;
}
