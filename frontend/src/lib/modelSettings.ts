// 本文件定义个人模型设置的数据结构，并封装读取与保存模型配置的同源 HTTP 请求。

import { requestAPI } from './api.js';

// ModelSelection 描述当前账号选择的 Main、Subagent 模型 ID 与服务端并发版本号。
export interface ModelSelection {
  main_model: string;
  subagent_model: string;
  version: number;
}

// ModelSettings 包含当前选择、可选模型目录以及目录加载状态提示。
export interface ModelSettings {
  selection: ModelSelection;
  models: { id: string; name: string }[];
  catalog_notice: string;
}

// getModelSettings 读取当前账号的模型选择与可选目录，关闭浮层时可通过 signal 取消。
export async function getModelSettings(token: string, signal: AbortSignal): Promise<ModelSettings> {
  const response = await requestAPI('/model-settings', token, {
    signal,
    fallbackError: '模型设置暂时不可用，请稍后重试',
  });
  return (await response.json()) as ModelSettings;
}

// saveModelSettings 提交修改后的模型选择与版本号，返回服务端确认的最新配置。
export async function saveModelSettings(
  token: string,
  selection: ModelSelection,
): Promise<ModelSelection> {
  const response = await requestAPI('/model-settings', token, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(selection),
    fallbackError: '模型设置暂时不可用，请稍后重试',
  });
  return (await response.json()) as ModelSelection;
}
