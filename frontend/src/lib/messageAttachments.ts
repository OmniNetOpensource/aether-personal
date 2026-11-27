// 本文件用 Effect Schema 定义引用、OSS 图片与工作区文件的通信结构，并用受控并发 Effect 负责附件校验与上传。

import { Effect, Schema } from 'effect';
import { HttpBody } from 'effect/http';
import { ApiClient } from './api.js';
import type { ApiError } from './api.js';
import { AttachmentValidationError } from './errors.js';

const maxImageBytes = 4 * 1024 * 1024;
const maxMessageImageBytes = 8 * 1024 * 1024;
const maxFileBytes = 50 * 1024 * 1024;
const uploadConcurrency = 4;
const supportedImageTypes = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp']);

// MessageQuoteSchema 记录选区引用指向的原消息 ID、字符区间与快照文本。
export const MessageQuoteSchema = Schema.Struct({
  text: Schema.String,
  message_id: Schema.String,
  start: Schema.Number,
  end: Schema.Number,
});
export type MessageQuote = typeof MessageQuoteSchema.Type;

// MessageImageSchema 描述上传至私有 OSS 的视觉图片及其签名预览地址。
export const MessageImageSchema = Schema.Struct({
  name: Schema.String,
  url: Schema.String,
  storage_url: Schema.String,
  size: Schema.Number,
  mime_type: Schema.String,
});
export type MessageImage = typeof MessageImageSchema.Type;

// MessageFileSchema 描述随消息发送的工作区文件卡片及临时签名下载地址。
export const MessageFileSchema = Schema.Struct({
  name: Schema.String,
  url: Schema.String,
  storage_url: Schema.String,
  size: Schema.Number,
  mime_type: Schema.String,
});
export type MessageFile = typeof MessageFileSchema.Type;

// MessagePartSchema 保留文字、引用、图片与工作区文件在消息中的顺序，发送和历史展示共用同一结构。
export const MessagePartSchema = Schema.Union([
  Schema.Struct({ type: Schema.Literal('text'), text: Schema.String }),
  Schema.Struct({ type: Schema.Literal('quote'), quote: MessageQuoteSchema }),
  Schema.Struct({ type: Schema.Literal('image'), image: MessageImageSchema }),
  Schema.Struct({ type: Schema.Literal('file'), file: MessageFileSchema }),
]);
export type MessagePart = typeof MessagePartSchema.Type;

// formatFileSize 把字节数转为易读的 B / KB / MB 文本，供文件卡片与记忆列表共用。
export function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// planUploads 在发起任何网络请求前统一校验类型、数量与体积，并返回每个文件的上传通道分类。
function planUploads(
  files: readonly File[],
  existing: readonly MessagePart[],
  workspaceEnabled: boolean,
): Effect.Effect<readonly ('image' | 'file')[], AttachmentValidationError> {
  return Effect.suspend(() => {
    let imageCount = existing.filter((part) => part.type === 'image').length;
    let fileCount = existing.filter((part) => part.type === 'file').length;
    let imageBytes = existing.reduce(
      (sum, part) => (part.type === 'image' ? sum + part.image.size : sum),
      0,
    );
    let totalBytes = existing.reduce(
      (sum, part) =>
        part.type === 'image'
          ? sum + part.image.size
          : part.type === 'file'
            ? sum + part.file.size
            : sum,
      0,
    );

    const plan: ('image' | 'file')[] = [];
    for (const file of files) {
      if (file.size === 0) {
        return Effect.fail(
          new AttachmentValidationError({ message: `「${file.name}」为空文件，无法上传` }),
        );
      }
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
        if (!supportedImageTypes.has(file.type)) {
          return Effect.fail(
            new AttachmentValidationError({
              message: `不支持「${file.name}」，请选择 PNG、JPEG、GIF 或 WebP 图片`,
            }),
          );
        }
        if (file.size > maxImageBytes) {
          return Effect.fail(
            new AttachmentValidationError({ message: `「${file.name}」不能超过 4 MB` }),
          );
        }
        if (imageCount >= 8) {
          return Effect.fail(
            new AttachmentValidationError({ message: '每条消息最多添加 8 张图片' }),
          );
        }
        return Effect.fail(
          new AttachmentValidationError({ message: '每条消息的图片合计不能超过 8 MB' }),
        );
      }

      if (file.size > maxFileBytes) {
        return Effect.fail(
          new AttachmentValidationError({ message: `「${file.name}」不能超过 50 MB` }),
        );
      }
      if (fileCount >= 10) {
        return Effect.fail(
          new AttachmentValidationError({ message: '每条消息最多添加 10 个文件' }),
        );
      }
      fileCount++;
      totalBytes += file.size;
      plan.push('file');
    }

    if (totalBytes > maxFileBytes) {
      return Effect.fail(
        new AttachmentValidationError({ message: '单条消息的图片与文件合计不能超过 50 MB' }),
      );
    }

    return Effect.succeed(plan);
  });
}

// uploadAttachmentsEffect 按并发上限批量上传附件；任一文件失败时 Effect 会自动中断其余在途上传。
export function uploadAttachmentsEffect(
  token: string,
  files: readonly File[],
  existing: readonly MessagePart[],
  workspaceEnabled: boolean,
): Effect.Effect<readonly MessagePart[], AttachmentValidationError | ApiError, ApiClient> {
  return Effect.gen(function* () {
    const plan = yield* planUploads(files, existing, workspaceEnabled);
    const api = yield* ApiClient;

    return yield* Effect.all(
      files.map((file, index): Effect.Effect<MessagePart, ApiError> => {
        if (plan[index] === 'image') {
          return api
            .requestJson(
              `/images?name=${encodeURIComponent(file.name)}`,
              token,
              MessageImageSchema,
              {
                method: 'POST',
                body: HttpBody.raw(file, { contentType: file.type }),
                timeout: '60 seconds',
                fallbackError: '图片上传失败',
              },
            )
            .pipe(Effect.map((image): MessagePart => ({ type: 'image', image })));
        }
        return api
          .requestJson(`/files?name=${encodeURIComponent(file.name)}`, token, MessageFileSchema, {
            method: 'POST',
            body: HttpBody.raw(file, { contentType: file.type || 'application/octet-stream' }),
            timeout: '300 seconds',
            fallbackError: '文件上传失败',
          })
          .pipe(Effect.map((fileMeta): MessagePart => ({ type: 'file', file: fileMeta })));
      }),
      { concurrency: uploadConcurrency },
    );
  });
}
