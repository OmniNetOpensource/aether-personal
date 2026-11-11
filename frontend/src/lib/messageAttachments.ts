// 本文件定义引用、OSS 图片与工作区文件的通信结构，并负责批量上传前的分类与大小校验。

import { z } from 'zod';

const maxImageBytes = 4 * 1024 * 1024;
const maxMessageImageBytes = 8 * 1024 * 1024;
const maxFileBytes = 50 * 1024 * 1024;
const supportedImageTypes = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp']);

export const messageQuoteSchema = z.object({
  text: z.string().min(1),
  message_id: z.uuid(),
  start: z.number().int().nonnegative(),
  end: z.number().int().positive(),
});

export const messageImageSchema = z.object({
  name: z.string(),
  url: z
    .string()
    .refine((value) => value === '' || value.startsWith('https://'), '图片预览需使用 HTTPS'),
  storage_url: z
    .string()
    .regex(/^oss:\/\/[a-z0-9-]+\/aether-chat\/[a-f0-9-]+\/[a-f0-9-]+\.(png|jpg|gif|webp)$/),
  size: z.number().int().positive().max(maxImageBytes),
  mime_type: z.enum(['image/png', 'image/jpeg', 'image/gif', 'image/webp']),
});

export const messageFileSchema = z.object({
  name: z.string().min(1),
  url: z
    .string()
    .refine((value) => value === '' || value.startsWith('https://'), '文件下载需使用 HTTPS'),
  storage_url: z
    .string()
    .regex(/^oss:\/\/[a-z0-9-]+\/aether-chat\/[a-f0-9-]+\/files\/[a-f0-9-]+$/),
  size: z.number().int().positive().max(maxFileBytes),
  mime_type: z.string().min(1),
});

export type MessageQuote = z.infer<typeof messageQuoteSchema>;
export type MessageImage = z.infer<typeof messageImageSchema>;
export type MessageFile = z.infer<typeof messageFileSchema>;

// MessagePart 保留文字、引用、图片与工作区文件在消息中的顺序，发送和历史展示共用同一结构。
export const messagePartSchema = z.discriminatedUnion('type', [
  z.object({ type: z.literal('text'), text: z.string() }),
  z.object({ type: z.literal('quote'), quote: messageQuoteSchema }),
  z.object({ type: z.literal('image'), image: messageImageSchema }),
  z.object({ type: z.literal('file'), file: messageFileSchema }),
]);
export type MessagePart = z.infer<typeof messagePartSchema>;

// formatFileSize 把字节数转为易读的 B / KB / MB 显示文本。
export function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// uploadAttachments 在上传前校验数量与体积：4 MB 以内的支持图片且视觉图片未超限时走视觉图片，已开通工作区时其余附件走工作区文件。
export async function uploadAttachments(
  files: File[],
  existing: MessagePart[],
  workspaceEnabled: boolean,
  uploadImage: (file: File) => Promise<MessageImage>,
  uploadFile: (file: File) => Promise<MessageFile>,
): Promise<MessagePart[]> {
  let imageCount = existing.filter((part) => part.type === 'image').length;
  let fileCount = existing.filter((part) => part.type === 'file').length;
  let imageBytes = existing.reduce((sum, part) => (part.type === 'image' ? sum + part.image.size : sum), 0);
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
    files.map(async (file, index): Promise<MessagePart> =>
      plan[index] === 'image'
        ? { type: 'image', image: await uploadImage(file) }
        : { type: 'file', file: await uploadFile(file) },
    ),
  );
}
