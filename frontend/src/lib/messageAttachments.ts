// 本文件定义引用、OSS 图片与工作区文件的通信结构，并负责批量上传前的分类、体积校验与文件大小格式化。

import { requestAPI } from './api.js';

const maxImageBytes = 4 * 1024 * 1024;
const maxMessageImageBytes = 8 * 1024 * 1024;
const maxFileBytes = 50 * 1024 * 1024;
const supportedImageTypes = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp']);

// MessageQuote 记录选区引用指向的原消息 ID、字符区间与快照文本。
export interface MessageQuote {
  text: string;
  message_id: string;
  start: number;
  end: number;
}

// MessageImage 描述上传至私有 OSS 的视觉图片及其签名预览地址。
export interface MessageImage {
  name: string;
  url: string;
  storage_url: string;
  size: number;
  mime_type: string;
}

// MessageFile 描述随消息发送的工作区文件卡片及临时签名下载地址。
export interface MessageFile {
  name: string;
  url: string;
  storage_url: string;
  size: number;
  mime_type: string;
}

// MessagePart 保留文字、引用、图片与工作区文件在消息中的顺序，发送和历史展示共用同一结构。
export type MessagePart =
  | { type: 'text'; text: string }
  | { type: 'quote'; quote: MessageQuote }
  | { type: 'image'; image: MessageImage }
  | { type: 'file'; file: MessageFile };

// formatFileSize 把字节数转为易读的 B / KB / MB 文本，供文件卡片与记忆列表共用。
export function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// uploadAttachments 在上传前校验数量与体积并逐个分类：4 MB 以内的支持图片且视觉图片未超限时走视觉图片，已开通工作区时其余附件走工作区文件。
export async function uploadAttachments(
  token: string,
  files: File[],
  existing: MessagePart[],
  workspaceEnabled: boolean,
): Promise<MessagePart[]> {
  let imageCount = existing.filter((part) => part.type === 'image').length;
  let fileCount = existing.filter((part) => part.type === 'file').length;
  let imageBytes = existing.reduce(
    (sum, part) => (part.type === 'image' ? sum + part.image.size : sum),
    0,
  );
  let totalBytes = existing.reduce(
    (sum, part) =>
      part.type === 'image' ? sum + part.image.size : part.type === 'file' ? sum + part.file.size : sum,
    0,
  );

  const plan: ('image' | 'file')[] = [];
  for (const file of files) {
    if (file.size === 0) throw new Error(`「${file.name}」为空文件，无法上传`);
    const canBeInlineImage =
      supportedImageTypes.has(file.type) &&
      file.size <= maxImageBytes &&
      imageCount < 8 &&
      imageBytes + file.size <= maxMessageImageBytes;

    if (canBeInlineImage) {
      imageCount++;
      imageBytes += file.size;
      totalBytes += file.size;
      plan.push('image');
      continue;
    }

    if (!workspaceEnabled) {
      if (!supportedImageTypes.has(file.type))
        throw new Error(`不支持「${file.name}」，请选择 PNG、JPEG、GIF 或 WebP 图片`);
      if (file.size > maxImageBytes)
        throw new Error(`「${file.name}」不能超过 4 MB`);
      if (imageCount >= 8)
        throw new Error('每条消息最多添加 8 张图片');
      throw new Error('每条消息的图片合计不能超过 8 MB');
    }

    if (file.size > maxFileBytes) throw new Error(`「${file.name}」不能超过 50 MB`);
    if (fileCount >= 10) throw new Error('每条消息最多添加 10 个文件');
    fileCount++;
    totalBytes += file.size;
    plan.push('file');
  }

  if (totalBytes > maxFileBytes) {
    throw new Error('单条消息的图片与文件合计不能超过 50 MB');
  }

  return Promise.all(
    files.map(async (file, index): Promise<MessagePart> => {
      if (plan[index] === 'image') {
        const response = await requestAPI(`/images?name=${encodeURIComponent(file.name)}`, token, {
          method: 'POST',
          headers: { 'Content-Type': file.type },
          body: file,
          timeoutMs: 60000,
          fallbackError: '图片上传失败',
        });
        return { type: 'image', image: (await response.json()) as MessageImage };
      }
      const response = await requestAPI(`/files?name=${encodeURIComponent(file.name)}`, token, {
        method: 'POST',
        headers: { 'Content-Type': file.type || 'application/octet-stream' },
        body: file,
        timeoutMs: 300000,
        fallbackError: '文件上传失败',
      });
      return { type: 'file', file: (await response.json()) as MessageFile };
    }),
  );
}
