// 本文件定义只读记忆查看接口的数据结构与 HTTP 请求封装，并返回根索引优先排序的扁平文件清单。

import { requestAPI } from './api.js';

// MemoryFileSummary 描述 /memory-files 返回的单个记忆文件元信息。
export interface MemoryFileSummary {
  path: string;
  bytes: number;
  updated_at: string;
}

// MemoryFileDetail 描述单个记忆文件的完整文本正文与元信息。
export interface MemoryFileDetail extends MemoryFileSummary {
  content: string;
}

// fetchMemoryFiles 请求当前账号的记忆文件列表，将 /memories/memory.md 置顶、其余按路径排序。
export async function fetchMemoryFiles(
  token: string,
  signal?: AbortSignal,
): Promise<MemoryFileSummary[]> {
  const response = await requestAPI('/memory-files', token, {
    headers: { Accept: 'application/json' },
    signal,
    fallbackError: '记忆加载失败，请稍后重试',
  });
  const payload = (await response.json()) as { files: MemoryFileSummary[] };
  return [...payload.files].sort((a, b) => {
    if (a.path === '/memories/memory.md') return -1;
    if (b.path === '/memories/memory.md') return 1;
    return a.path.localeCompare(b.path);
  });
}

// fetchMemoryFileContent 按逻辑路径按需拉取单个记忆文件的完整文本。
export async function fetchMemoryFileContent(
  token: string,
  path: string,
  signal?: AbortSignal,
): Promise<MemoryFileDetail> {
  const response = await requestAPI(
    `/memory-files/content?path=${encodeURIComponent(path)}`,
    token,
    {
      headers: { Accept: 'application/json' },
      signal,
      fallbackError: '记忆加载失败，请稍后重试',
    },
  );
  return (await response.json()) as MemoryFileDetail;
}
