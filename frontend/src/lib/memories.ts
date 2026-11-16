// 本文件负责只读记忆查看接口的数据校验、HTTP 请求封装，以及将扁平记忆路径组装为 VS Code 样式目录树。

import { z } from 'zod';

// memoryFileSummarySchema 校验记忆文件列表接口返回的每项元信息。
export const memoryFileSummarySchema = z.object({
  path: z.string().min(1),
  bytes: z.number().int().nonnegative(),
  updated_at: z.string(),
});
export type MemoryFileSummary = z.infer<typeof memoryFileSummarySchema>;

// memoryFileListSchema 校验 /memory-files 列表响应。
export const memoryFileListSchema = z.object({
  files: z.array(memoryFileSummarySchema),
});

// memoryFileDetailSchema 校验单个记忆文件正文与元信息响应。
export const memoryFileDetailSchema = z.object({
  path: z.string().min(1),
  content: z.string(),
  bytes: z.number().int().nonnegative(),
  updated_at: z.string(),
});
export type MemoryFileDetail = z.infer<typeof memoryFileDetailSchema>;

// MemoryAuthError 区分登录失效与其他读取异常，便于页面引导用户返回聊天重新登录。
export class MemoryAuthError extends Error {}

// MemoryTreeNode 统一描述目录树中的文件夹与文件节点，目录按字母排序后排在文件之前。
export type MemoryTreeNode =
  | {
      kind: 'directory';
      name: string;
      path: string;
      children: MemoryTreeNode[];
    }
  | {
      kind: 'file';
      name: string;
      path: string;
      file: MemoryFileSummary;
    };

// fetchMemoryFiles 请求当前用户的全部记忆文件清单，不触发后端初始化空根文件。
export async function fetchMemoryFiles(
  token: string,
  signal?: AbortSignal,
): Promise<MemoryFileSummary[]> {
  const response = await requestMemoryAPI('/memory-files', token, signal);
  return memoryFileListSchema.parse(await response.json()).files;
}

// fetchMemoryFileContent 按逻辑路径按需拉取单个记忆文件的完整文本。
export async function fetchMemoryFileContent(
  token: string,
  path: string,
  signal?: AbortSignal,
): Promise<MemoryFileDetail> {
  const response = await requestMemoryAPI(
    `/memory-files/content?path=${encodeURIComponent(path)}`,
    token,
    signal,
  );
  return memoryFileDetailSchema.parse(await response.json());
}

// requestMemoryAPI 统一附带 Bearer 会话令牌与超时控制，并按状态码区分登录失效与普通错误。
async function requestMemoryAPI(url: string, token: string, signal?: AbortSignal) {
  const timeout = AbortSignal.timeout(15000);
  const response = await fetch(url, {
    method: 'GET',
    headers: {
      Authorization: 'Bearer ' + token,
      Accept: 'application/json',
    },
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  });
  if (response.status === 401) {
    throw new MemoryAuthError('登录已过期，请返回聊天重新登录');
  }
  if (!response.ok) {
    const message = response.headers.get('content-type')?.startsWith('text/plain')
      ? (await response.text()).trim()
      : '';
    throw new Error(message || '记忆加载失败，请稍后重试');
  }
  return response;
}

// buildMemoryTree 把以 /memories/ 开头的扁平路径列表组织成可折叠的层级目录树。
export function buildMemoryTree(files: MemoryFileSummary[]): MemoryTreeNode[] {
  interface MutableDirectory {
    directories: Map<string, MutableDirectory>;
    files: Extract<MemoryTreeNode, { kind: 'file' }>[];
  }

  const root: MutableDirectory = { directories: new Map(), files: [] };

  for (const file of files) {
    const relative = file.path.startsWith('/memories/')
      ? file.path.slice('/memories/'.length)
      : file.path;
    const segments = relative.split('/').filter(Boolean);
    if (segments.length === 0) continue;

    let current = root;
    for (let index = 0; index < segments.length - 1; index++) {
      const folderName = segments[index];
      let next = current.directories.get(folderName);
      if (!next) {
        next = { directories: new Map(), files: [] };
        current.directories.set(folderName, next);
      }
      current = next;
    }

    current.files.push({
      kind: 'file',
      name: segments[segments.length - 1],
      path: file.path,
      file,
    });
  }

  function finalize(directory: MutableDirectory, prefix: string): MemoryTreeNode[] {
    const directories: MemoryTreeNode[] = Array.from(directory.directories.entries())
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([name, child]) => {
        const path = `${prefix}/${name}`;
        return {
          kind: 'directory',
          name,
          path,
          children: finalize(child, path),
        };
      });

    // memory.md 根文件固定放在根目录文件首位，其余文件按文件名排序。
    const files = [...directory.files].sort((a, b) => {
      if (a.path === '/memories/memory.md') return -1;
      if (b.path === '/memories/memory.md') return 1;
      return a.name.localeCompare(b.name);
    });

    return [...directories, ...files];
  }

  return finalize(root, '/memories');
}

// formatMemoryBytes 把字节数格式化为紧凑的文件大小文本，供目录树与状态栏共用。
export function formatMemoryBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
