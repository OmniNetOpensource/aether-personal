// 本文件定义个人模型设置接口的响应结构，在浏览器接收数据时统一校验。

import { z } from 'zod';

// modelSelectionSchema 描述可写的模型 ID 与服务端版本；空字符串表示跟随。
export const modelSelectionSchema = z.object({
  main_model: z.string().min(1),
  subagent_model: z.string(),
  compact_model: z.string(),
  version: z.number().int().nonnegative(),
});
export type ModelSelection = z.infer<typeof modelSelectionSchema>;

// modelSettingsSchema 同时返回个人选择、可选目录和目录加载状态。
export const modelSettingsSchema = z.object({
  selection: modelSelectionSchema,
  models: z.array(z.object({ id: z.string().min(1), name: z.string() })),
  catalog_notice: z.string(),
});
export type ModelSettings = z.infer<typeof modelSettingsSchema>;
