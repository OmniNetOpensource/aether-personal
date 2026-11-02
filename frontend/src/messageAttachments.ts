// 本文件定义引用与 OSS 图片的通信结构，并负责批量上传前的大小校验。

import { z } from 'zod';

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
  size: z
    .number()
    .int()
    .positive()
    .max(4 * 1024 * 1024),
  mime_type: z.enum(['image/png', 'image/jpeg', 'image/gif', 'image/webp']),
});
export type MessageQuote = z.infer<typeof messageQuoteSchema>;
export type MessageImage = z.infer<typeof messageImageSchema>;

// MessagePart 保留文字、引用与图片在编辑器中的顺序，发送和历史展示共用同一结构。
export const messagePartSchema = z.discriminatedUnion('type', [
  z.object({ type: z.literal('text'), text: z.string() }),
  z.object({ type: z.literal('quote'), quote: messageQuoteSchema }),
  z.object({ type: z.literal('image'), image: messageImageSchema }),
]);
export type MessagePart = z.infer<typeof messagePartSchema>;

// uploadImages 先校验整批图片，再交给控制器上传到 OSS，避免开始上传后才发现超出单条消息限制。
export async function uploadImages(
  files: File[],
  existing: MessageImage[],
  upload: (file: File) => Promise<MessageImage>,
): Promise<MessageImage[]> {
  if (existing.length + files.length > 8) throw new Error('每条消息最多添加 8 张图片');
  let total = existing.reduce((sum, image) => sum + image.size, 0);
  for (const file of files) {
    if (!['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(file.type))
      throw new Error(`不支持「${file.name}」，请选择 PNG、JPEG、GIF 或 WebP 图片`);
    if (file.size === 0 || file.size > 4 * 1024 * 1024)
      throw new Error(`「${file.name}」需为非空图片，且不超过 4 MB`);
    total += file.size;
  }
  if (total > 8 * 1024 * 1024) throw new Error('每条消息的图片合计不能超过 8 MB');
  return Promise.all(files.map(upload));
}
