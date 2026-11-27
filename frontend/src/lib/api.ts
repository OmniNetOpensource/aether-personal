// 本文件基于 Effect HttpClient 封装同源 HTTP 服务，统一处理 Bearer 认证、超时中断、状态码分流与 Schema 解码。

import { Context, Duration, Effect, Layer, Schema } from 'effect';
import {
  FetchHttpClient,
  HttpBody,
  HttpClient,
  HttpClientRequest,
  HttpClientResponse,
} from 'effect/http';
import { AuthError, DecodeError, HttpApiError, NetworkError } from './errors.js';

// ApiRequestOptions 定义单次同源 HTTP 请求的可选方法、请求头、请求体、超时和兜底错误文案。
export interface ApiRequestOptions {
  readonly method?: 'GET' | 'POST' | 'PUT' | 'DELETE';
  readonly headers?: Readonly<Record<string, string>>;
  readonly body?: HttpBody.HttpBody;
  readonly timeout?: Duration.Input;
  readonly fallbackError?: string;
}

// ApiError 汇总同源 HTTP 请求可能抛出的领域错误。
export type ApiError = AuthError | HttpApiError | NetworkError | DecodeError;

// JsonDecoder 约束无需额外环境服务即可解码 JSON 的 Schema。
export type JsonDecoder<A = unknown> = Schema.Decoder<A, never>;

// ApiClient 提供经 Schema 校验的同源 HTTP 能力，脱离 React 组件独立存在于 Effect Layer 中。
export class ApiClient extends Context.Service<
  ApiClient,
  {
    readonly requestJson: <S extends JsonDecoder>(
      path: string,
      token: string,
      schema: S,
      options?: ApiRequestOptions,
    ) => Effect.Effect<S['Type'], ApiError>;
    readonly requestVoid: (
      path: string,
      token: string,
      options?: ApiRequestOptions,
    ) => Effect.Effect<void, AuthError | HttpApiError | NetworkError>;
    readonly requestPublicJson: <S extends JsonDecoder>(
      path: string,
      schema: S,
      options?: ApiRequestOptions,
    ) => Effect.Effect<
      { readonly ok: boolean; readonly status: number; readonly data: S['Type'] },
      HttpApiError | NetworkError | DecodeError
    >;
    readonly requestPublicVoid: (
      path: string,
      options?: ApiRequestOptions,
    ) => Effect.Effect<void, HttpApiError | NetworkError>;
  }
>()('@aether/ApiClient') {}

// buildRequest 按方法、路径、认证头与可选请求体构造不可变的 HttpClientRequest。
function buildRequest(
  path: string,
  token: string | null,
  options: ApiRequestOptions = {},
): HttpClientRequest.HttpClientRequest {
  const method = options.method ?? 'GET';
  let req = HttpClientRequest.make(method)(path);
  if (token !== null) {
    req = HttpClientRequest.bearerToken(req, token);
  }
  if (options.headers) {
    req = HttpClientRequest.setHeaders(req, options.headers);
  }
  if (options.body) {
    req = HttpClientRequest.setBody(req, options.body);
  }
  return req;
}

// readPlainErrorText 在服务端返回 text/plain 错误正文时提取去空白文案。
function readPlainErrorText(
  response: HttpClientResponse.HttpClientResponse,
): Effect.Effect<string> {
  const contentType = response.headers['content-type'] ?? '';
  if (!contentType.startsWith('text/plain')) return Effect.succeed('');
  return response.text.pipe(
    Effect.map((text) => text.trim()),
    Effect.orElseSucceed(() => ''),
  );
}

// BrowserHttpClientLive 提供关闭 trace 传播头的浏览器 FetchHttpClient，避免触发额外预检请求。
export const BrowserHttpClientLive = FetchHttpClient.layer.pipe(
  Layer.provide(Layer.succeed(HttpClient.TracerPropagationEnabled, false)),
);

// ApiClientLive 组合 HttpClient 实现完整的同源认证、超时与 Schema 解码逻辑。
export const ApiClientLive = Layer.effect(
  ApiClient,
  Effect.gen(function* () {
    const httpClient = yield* HttpClient.HttpClient;

    const executeRequest = (
      path: string,
      token: string | null,
      options: ApiRequestOptions = {},
    ): Effect.Effect<HttpClientResponse.HttpClientResponse, NetworkError> => {
      const timeout = options.timeout ?? '15 seconds';
      const fallbackError = options.fallbackError ?? '请求失败，请稍后重试';
      const request = buildRequest(path, token, options);
      return httpClient.execute(request).pipe(
        Effect.timeoutOrElse({
          duration: timeout,
          orElse: () => Effect.fail(new NetworkError({ message: '请求超时，请检查网络后重试' })),
        }),
        Effect.catchTag('HttpClientError', (cause) =>
          Effect.fail(new NetworkError({ message: fallbackError, cause })),
        ),
      );
    };

    const executeAuthenticated = (
      path: string,
      token: string,
      options: ApiRequestOptions = {},
    ): Effect.Effect<
      HttpClientResponse.HttpClientResponse,
      AuthError | HttpApiError | NetworkError
    > =>
      Effect.gen(function* () {
        const fallbackError = options.fallbackError ?? '请求失败，请稍后重试';
        const response = yield* executeRequest(path, token, options);

        if (response.status === 401) {
          return yield* Effect.fail(new AuthError({ message: '登录已过期，请重新登录' }));
        }
        if (response.status < 200 || response.status >= 300) {
          const message = yield* readPlainErrorText(response);
          return yield* Effect.fail(
            new HttpApiError({
              status: response.status,
              message: message || fallbackError,
            }),
          );
        }
        return response;
      });

    return ApiClient.of({
      requestJson: (path, token, schema, options) =>
        Effect.gen(function* () {
          const fallbackError = options?.fallbackError ?? '响应数据格式无效';
          const response = yield* executeAuthenticated(path, token, options);
          const payload = yield* response.json.pipe(
            Effect.mapError((cause) => new DecodeError({ message: fallbackError, cause })),
          );
          return yield* Schema.decodeUnknownEffect(schema)(payload).pipe(
            Effect.mapError((cause) => new DecodeError({ message: fallbackError, cause })),
          );
        }),

      requestVoid: (path, token, options) =>
        executeAuthenticated(path, token, options).pipe(Effect.asVoid),

      requestPublicJson: (path, schema, options) =>
        Effect.gen(function* () {
          const fallbackError = options?.fallbackError ?? '无法加载数据，请稍后重试';
          const response = yield* executeRequest(path, null, {
            ...options,
            headers: { Accept: 'application/json', ...options?.headers },
            fallbackError,
          });

          const contentType = response.headers['content-type'] ?? '';
          const ok = response.status >= 200 && response.status < 300;
          if (contentType.includes('application/json')) {
            const payload = yield* response.json.pipe(
              Effect.mapError((cause) => new DecodeError({ message: fallbackError, cause })),
            );
            const data = yield* Schema.decodeUnknownEffect(schema)(payload).pipe(
              Effect.mapError((cause) => new DecodeError({ message: fallbackError, cause })),
            );
            return { ok, status: response.status, data };
          }
          const message = yield* readPlainErrorText(response);
          return yield* Effect.fail(
            new HttpApiError({
              status: response.status,
              message: message || fallbackError,
            }),
          );
        }),

      requestPublicVoid: (path, options) =>
        Effect.gen(function* () {
          const fallbackError = options?.fallbackError ?? '请求失败，请稍后重试';
          const response = yield* executeRequest(path, null, options);
          if (response.status < 200 || response.status >= 300) {
            const message = yield* readPlainErrorText(response);
            return yield* Effect.fail(
              new HttpApiError({
                status: response.status,
                message: message || fallbackError,
              }),
            );
          }
        }),
    });
  }),
).pipe(Layer.provide(BrowserHttpClientLive));
