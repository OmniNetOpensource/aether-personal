// 本文件用 Effect Schema 定义只读记忆查看接口的数据结构，并封装返回根索引优先列表的 Effect 请求。

import { Effect, Schema } from 'effect';
import { ApiClient } from './api.js';
import type { ApiError } from './api.js';

// MemoryFileSummarySchema 描述 /memory-files 返回的单个记忆文件元信息。
export const MemoryFileSummarySchema = Schema.Struct({
  path: Schema.String,
  bytes: Schema.Number,
  updated_at: Schema.String,
});
export type MemoryFileSummary = typeof MemoryFileSummarySchema.Type;

// MemoryFileDetailSchema 描述单个记忆文件的完整文本正文与元信息。
export const MemoryFileDetailSchema = Schema.Struct({
  path: Schema.String,
  bytes: Schema.Number,
  updated_at: Schema.String,
  content: Schema.String,
});
export type MemoryFileDetail = typeof MemoryFileDetailSchema.Type;

const MemoryFilesPayloadSchema = Schema.Struct({
  files: Schema.Array(MemoryFileSummarySchema),
});

// fetchMemoryFilesEffect 请求当前账号的记忆文件列表，将 /memories/memory.md 置顶、其余按路径排序。
export function fetchMemoryFilesEffect(
  token: string,
): Effect.Effect<readonly MemoryFileSummary[], ApiError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    const payload = yield* api.requestJson('/memory-files', token, MemoryFilesPayloadSchema, {
      headers: { Accept: 'application/json' },
      fallbackError: '记忆加载失败，请稍后重试',
    });
    return [...payload.files].sort((a, b) => {
      if (a.path === '/memories/memory.md') return -1;
      if (b.path === '/memories/memory.md') return 1;
      return a.path.localeCompare(b.path);
    });
  });
}

// fetchMemoryFileContentEffect 按逻辑路径按需拉取单个记忆文件的完整文本。
export function fetchMemoryFileContentEffect(
  token: string,
  path: string,
): Effect.Effect<MemoryFileDetail, ApiError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    return yield* api.requestJson(
      `/memory-files/content?path=${encodeURIComponent(path)}`,
      token,
      MemoryFileDetailSchema,
      {
        headers: { Accept: 'application/json' },
        fallbackError: '记忆加载失败，请稍后重试',
      },
    );
  });
}
