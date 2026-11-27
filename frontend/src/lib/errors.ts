// 本文件集中定义前端领域错误类型与统一错误文案格式化，让全部异步失败走 Effect 的类型化错误通道。

import { Cause, Data, Option } from 'effect';

// AuthError 标识会话令牌失效，便于各页面统一引导用户重新登录。
export class AuthError extends Data.TaggedError('AuthError')<{
  readonly message: string;
}> {}

// HttpApiError 表示同源 HTTP 接口返回非 2xx 状态码。
export class HttpApiError extends Data.TaggedError('HttpApiError')<{
  readonly status: number;
  readonly message: string;
}> {}

// NetworkError 表示请求超时、离线或底层网络传输失败。
export class NetworkError extends Data.TaggedError('NetworkError')<{
  readonly message: string;
  readonly cause?: unknown;
}> {}

// DecodeError 表示接口响应或 WebSocket 帧不符合 Schema 协议约束。
export class DecodeError extends Data.TaggedError('DecodeError')<{
  readonly message: string;
  readonly cause?: unknown;
}> {}

// WsConnectionError 表示聊天或桌面 WebSocket 断线、超时或收到非法帧。
export class WsConnectionError extends Data.TaggedError('WsConnectionError')<{
  readonly reason: string;
  readonly message: string;
}> {}

// StorageError 表示浏览器拒绝写入本地会话存储。
export class StorageError extends Data.TaggedError('StorageError')<{
  readonly message: string;
}> {}

// AttachmentValidationError 表示附件在上传前未通过类型、数量或体积校验。
export class AttachmentValidationError extends Data.TaggedError('AttachmentValidationError')<{
  readonly message: string;
}> {}

// FrontendError 汇总前端全部可预期领域错误，便于最外层统一识别与呈现。
export type FrontendError =
  | AuthError
  | HttpApiError
  | NetworkError
  | DecodeError
  | WsConnectionError
  | StorageError
  | AttachmentValidationError;

// formatUserError 从任意领域错误或普通 Error 提取适合界面直接展示的中文提示。
export function formatUserError(error: unknown, fallback = '请求失败，请稍后重试'): string {
  if (typeof error === 'object' && error !== null && 'message' in error) {
    const message = (error as { readonly message?: unknown }).message;
    if (typeof message === 'string' && message.trim()) return message;
  }
  return fallback;
}

// formatCauseError 从 Effect Cause 中提取首要失败原因并转为中文提示。
export function formatCauseError(
  cause: Cause.Cause<unknown>,
  fallback = '请求失败，请稍后重试',
): string {
  const failure = Cause.findErrorOption(cause);
  if (Option.isSome(failure)) {
    return formatUserError(failure.value, fallback);
  }
  return formatUserError(Cause.squash(cause), fallback);
}

// isAuthFailure 判断一个 Effect Cause 是否由登录失效（AuthError）导致。
export function isAuthFailure(cause: Cause.Cause<unknown>): boolean {
  const failure = Cause.findErrorOption(cause);
  return Option.isSome(failure) && failure.value instanceof AuthError;
}
