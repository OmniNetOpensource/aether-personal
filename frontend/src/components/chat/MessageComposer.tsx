// 本文件组合实时任务进度胶囊、多模态编辑器与发送工具栏：统一管理文字、引用、图片与文件上传和草稿，展示发送中与发送失败，发送成功后清空输入。

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { RefObject } from 'react';
import { uploadAttachments } from '../../lib/messageAttachments.js';
import type { MessagePart, MessageQuote } from '../../lib/messageAttachments.js';
import { RichMessageInput } from './RichMessageInput.js';
import type { RichMessageInputHandle } from './RichMessageInput.js';
import { isOpenTask, taskStatusLabel } from '../../lib/ChatController.js';
import type { ChatController } from '../../lib/ChatController.js';
import './chatTasks.css';

// MessageComposerHandle 供消息列表把选区引用插入当前光标处。
export interface MessageComposerHandle {
  insertQuote: (quote: MessageQuote) => void;
}

// MessageComposer 以结构化内容作为草稿，编辑器负责光标、混排和撤销，控制器负责可靠发送。
export function MessageComposer({
  controller,
  composerRef,
  onOpenTasks,
}: {
  controller: ChatController;
  composerRef: RefObject<MessageComposerHandle | null>;
  onOpenTasks?: () => void;
}) {
  const draftKey = `aether_draft:${controller.email}`;
  const [parts, setParts] = useState<MessagePart[]>(() => {
    try {
      const text = sessionStorage.getItem(draftKey);
      return text ? [{ type: 'text', text }] : [];
    } catch {
      return [];
    }
  });
  const initialParts = useRef(parts);
  const [error, setError] = useState('');
  const [reading, setReading] = useState(false);
  const readingRef = useRef(false);
  const input = useRef<RichMessageInputHandle | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const composerPadding = useRef<HTMLDivElement>(null);
  const [sending, setSending] = useState(false);
  const sendingRef = useRef(false);
  // 同一份草稿重发时沿用同一个消息 ID：上次其实已保存、只是确认没送达时，服务器按 ID 去重，不会存成两条。
  const clientMessageID = useRef(crypto.randomUUID());
  useEffect(() => {
    composerRef.current = {
      insertQuote: (quote) => input.current?.insert([{ type: 'quote', quote }]),
    };
    return () => {
      composerRef.current = null;
    };
  }, [composerRef]);
  useEffect(() => {
    const text = parts.flatMap((part) => (part.type === 'text' ? [part.text] : [])).join('');
    try {
      if (text) sessionStorage.setItem(draftKey, text);
      else sessionStorage.removeItem(draftKey);
    } catch {
      /* 禁用存储时草稿仍保留在内存。 */
    }
  }, [parts, draftKey]);
  useLayoutEffect(() => {
    const element = composerPadding.current;
    if (!element) return;
    const resize = () =>
      element.parentElement?.style.setProperty('--composer-height', `${element.offsetHeight}px`);
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  // addFiles 统一处理选择、粘贴与拖放的图片和工作区文件，失败保留其余草稿以便继续编辑。
  const addFiles = async (files: File[]) => {
    if (readingRef.current || files.length === 0) return;
    readingRef.current = true;
    setReading(true);
    try {
      const uploaded = await uploadAttachments(
        files,
        parts,
        controller.desktopAvailable,
        (file) => controller.uploadImage(file),
        (file) => controller.uploadFile(file),
      );
      input.current?.insert(uploaded);
      setError('');
    } catch (error) {
      setError(error instanceof Error ? error.message : '附件上传失败');
    } finally {
      readingRef.current = false;
      setReading(false);
    }
  };
  // send 在提交边界检查总长度，禁止图片尚未准备好时发出不完整消息；等服务器保存后才清空，失败时保留内容并显示错误。
  const send = async () => {
    if (
      sendingRef.current ||
      readingRef.current ||
      !parts.some((part) => part.type !== 'text' || part.text.trim())
    )
      return;
    if (parts.filter((part) => part.type === 'quote').length > 20) {
      setError('每条消息最多添加 20 段引用');
      return;
    }
    const text = parts
      .flatMap((part) =>
        part.type === 'text' ? [part.text] : part.type === 'quote' ? [part.quote.text] : [],
      )
      .join('');
    if (Array.from(text).length > 64000) {
      setError('正文和引用合计不能超过 64,000 个字符');
      return;
    }
    sendingRef.current = true;
    setSending(true);
    setError('');
    try {
      if (await controller.send(parts, clientMessageID.current)) {
        input.current?.clear();
        setParts([]);
        clientMessageID.current = crypto.randomUUID();
      }
    } catch (error) {
      setError(`发送失败：${error instanceof Error ? error.message : '请稍后重试'}`);
    } finally {
      sendingRef.current = false;
      setSending(false);
    }
  };
  // 提取未结束任务，供输入框上方胶囊实时展示当前主任务与剩余排队情况。
  const openTasks = controller.tasks.filter(isOpenTask);
  const leadTask = openTasks.find((task) => task.status === 'in_progress') ?? openTasks[0];
  let queueSummary = '';
  if (leadTask) {
    if (leadTask.status === 'in_progress') {
      const otherInProgressCount =
        openTasks.filter((task) => task.status === 'in_progress').length - 1;
      const queuedCount = openTasks.filter((task) => task.status === 'queued').length;
      const waitingCount = openTasks.filter((task) => task.status === 'waiting').length;
      const restParts: string[] = [];
      if (otherInProgressCount > 0) restParts.push(`其余 ${otherInProgressCount} 项并行中`);
      if (queuedCount > 0) restParts.push(`${queuedCount} 项排队`);
      if (waitingCount > 0) restParts.push(`${waitingCount} 项等待`);
      queueSummary = restParts.length > 0 ? restParts.join(' · ') : '查看详情';
    } else {
      queueSummary = openTasks.length > 1 ? `共 ${openTasks.length} 项待办` : '查看详情';
    }
  }

  return (
    <div className="composer-padding" ref={composerPadding}>
      {leadTask && (
        <button
          type="button"
          className="composer-task-bar"
          onClick={onOpenTasks}
          aria-label={`查看任务进展：${taskStatusLabel(leadTask.status)} ${leadTask.title}`}
        >
          <span className="composer-task-main">
            <span className={`task-status-pill is-${leadTask.status}`}>
              {leadTask.status === 'in_progress' && (
                <span
                  className={`task-live-dot${controller.connected && controller.mainThinking ? ' is-pulsing' : ''}`}
                  aria-hidden="true"
                />
              )}
              {taskStatusLabel(leadTask.status)}
            </span>
            <span className="composer-task-title">{leadTask.title}</span>
          </span>
          <span className="composer-task-meta">
            <span>{queueSummary}</span>
            <svg viewBox="0 0 24 24" aria-hidden="true">
              <path d="m9 6 6 6-6 6" />
            </svg>
          </span>
        </button>
      )}
      {!controller.connected && (
        <p className="composer-connection-hint" role="status">
          {typeof navigator !== 'undefined' && !navigator.onLine
            ? '当前处于离线状态，可先编辑草稿，恢复网络后自动连接'
            : '连接已中断，正在重新连接…'}
        </p>
      )}
      {error && (
        <p className="error composer-error" role="alert">
          {error}
        </p>
      )}
      <form
        className={`composer multimodal-composer${sending ? ' is-sending' : ''}`}
        onSubmit={(event) => {
          event.preventDefault();
          void send();
        }}
        onDragOver={(event) => {
          if (event.dataTransfer.types.includes('Files')) {
            event.preventDefault();
            event.dataTransfer.dropEffect = 'copy';
          }
        }}
        onDrop={(event) => {
          if (event.dataTransfer.files.length) {
            event.preventDefault();
            void addFiles(Array.from(event.dataTransfer.files));
          }
        }}
      >
        <input
          ref={fileInput}
          type="file"
          multiple
          accept={
            controller.desktopAvailable ? undefined : 'image/png,image/jpeg,image/gif,image/webp'
          }
          hidden
          aria-label={controller.desktopAvailable ? '选择图片或文件' : '选择图片'}
          onChange={(event) => {
            void addFiles(Array.from(event.currentTarget.files ?? []));
            event.currentTarget.value = '';
          }}
        />
        <RichMessageInput
          inputRef={input}
          initialParts={initialParts.current}
          onChange={(value) => {
            setParts(value);
            setError('');
            clientMessageID.current = crypto.randomUUID();
          }}
          onFiles={(files) => void addFiles(files)}
          onSubmit={() => void send()}
        />
        {reading && (
          <span className="upload-status" role="status">
            正在上传附件…
          </span>
        )}
        <div className="composer-tools">
          <button
            type="button"
            className="attach-button"
            aria-label={controller.desktopAvailable ? '添加图片或文件' : '添加图片'}
            title={
              controller.desktopAvailable
                ? '添加图片或文件（上限 50 MB），也可粘贴或拖入'
                : '添加图片，也可粘贴或拖入'
            }
            disabled={reading}
            onClick={() => fileInput.current?.click()}
          >
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.8"
              strokeLinecap="round"
              aria-hidden="true"
            >
              <path d="M12 5v14M5 12h14" />
            </svg>
          </button>
          <span className="composer-shortcut" aria-hidden="true">
            <kbd>Ctrl</kbd>
            <span>↵</span>
            <span>发送</span>
          </span>
          <button
            type="submit"
            className={`send-button${sending ? ' is-sending' : ''}`}
            aria-label={sending ? '发送中' : '发送消息'}
            aria-busy={sending}
            disabled={
              reading || sending || !parts.some((part) => part.type !== 'text' || part.text.trim())
            }
          >
            {sending ? (
              <span className="send-spinner" aria-hidden="true" />
            ) : (
              <svg
                className="send-arrow"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden="true"
              >
                <path d="M12 19V5m-6 6 6-6 6 6" />
              </svg>
            )}
          </button>
        </div>
      </form>
    </div>
  );
}
