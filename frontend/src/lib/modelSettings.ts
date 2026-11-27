// 本文件用 Effect Schema 定义个人模型设置的数据结构，并封装读取与保存模型配置的 Effect 请求。

import { Effect, Schema } from 'effect';
import { HttpBody } from 'effect/http';
import { ApiClient } from './api.js';
import type { ApiError } from './api.js';

// ModelSelectionSchema 描述当前账号选择的 Main、Subagent 模型 ID 与服务端并发版本号。
export const ModelSelectionSchema = Schema.Struct({
  main_model: Schema.String,
  subagent_model: Schema.String,
  version: Schema.Number,
});
export type ModelSelection = typeof ModelSelectionSchema.Type;

// ModelCatalogItemSchema 描述模型目录中的单个可选项。
export const ModelCatalogItemSchema = Schema.Struct({
  id: Schema.String,
  name: Schema.String,
});

// ModelSettingsSchema 包含当前选择、可选模型目录以及目录加载状态提示。
export const ModelSettingsSchema = Schema.Struct({
  selection: ModelSelectionSchema,
  models: Schema.Array(ModelCatalogItemSchema),
  catalog_notice: Schema.String,
});
export type ModelSettings = typeof ModelSettingsSchema.Type;

// getModelSettingsEffect 读取当前账号的模型选择与可选目录，Fiber 中断时自动取消底层请求。
export function getModelSettingsEffect(
  token: string,
): Effect.Effect<ModelSettings, ApiError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    return yield* api.requestJson('/model-settings', token, ModelSettingsSchema, {
      fallbackError: '模型设置暂时不可用，请稍后重试',
    });
  });
}

// saveModelSettingsEffect 提交修改后的模型选择与版本号，返回服务端确认的最新配置。
export function saveModelSettingsEffect(
  token: string,
  selection: ModelSelection,
): Effect.Effect<ModelSelection, ApiError, ApiClient> {
  return Effect.gen(function* () {
    const api = yield* ApiClient;
    return yield* api.requestJson('/model-settings', token, ModelSelectionSchema, {
      method: 'PUT',
      body: HttpBody.jsonUnsafe(selection),
      fallbackError: '模型设置暂时不可用，请稍后重试',
    });
  });
}
