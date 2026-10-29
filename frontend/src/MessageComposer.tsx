// 本文件是消息输入框组件：管理草稿、多行输入与发送入口，发送后通知消息列表贴底。

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { ChatController } from './ChatController.js';

export function MessageComposer({
  controller,
  onMessageSent,
}: {
  controller: ChatController;
  onMessageSent: () => void;
}) {
  const draftKey = `aether_draft:${controller.email}`;
  const [draft, setDraft] = useState(() => {
    try {
      return sessionStorage.getItem(draftKey) ?? '';
    } catch {
      return '';
    }
  });
  const [error, setError] = useState('');
  const [files, setFiles] = useState<File[]>([]);
  const input = useRef<HTMLTextAreaElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const composerPadding = useRef<HTMLDivElement>(null);
  const sending = useRef(false);
  useEffect(() => {
    try {
      if (draft) sessionStorage.setItem(draftKey, draft);
      else sessionStorage.removeItem(draftKey);
    } catch {
      /* Drafts still work in memory when storage is disabled. */
    }
  }, [draft, draftKey]);
  useLayoutEffect(() => {
    const element = input.current;
    if (!element) return;
    element.style.height = '0px';
    element.style.height = `${Math.min(160, Math.max(48, element.scrollHeight))}px`;
  }, [draft]);
  useLayoutEffect(() => {
    const element = composerPadding.current;
    if (!element) return;
    const resize = () => {
      element.parentElement?.style.setProperty('--composer-height', `${element.offsetHeight}px`);
    };
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const send = async () => {
    if (sending.current || !draft.trim()) return;
    if (Array.from(draft.trim()).length > 32000) {
      setError('消息不能超过 32,000 个字符');
      return;
    }
    sending.current = true;
    try {
      if (await controller.send(draft)) {
        setDraft('');
        setError('');
        onMessageSent();
        input.current?.focus({ preventScroll: true });
      }
    } finally {
      sending.current = false;
    }
  };
  return (
    <div className="composer-padding" ref={composerPadding}>
      {files.length > 0 && (
        <div className="selected-files" aria-label="已选择的文件">
          {files.map((file, index) => (
            <span className="selected-file" key={`${file.name}-${index}`}>
              <span className="selected-file-name">{file.name}</span>
              <button
                type="button"
                className="remove-file"
                aria-label={`移除 ${file.name}`}
                onClick={() =>
                  setFiles((selected) => selected.filter((_, position) => position !== index))
                }
              >
                ×
              </button>
            </span>
          ))}
        </div>
      )}
      {error && (
        <p className="error composer-error" role="alert">
          {error}
        </p>
      )}
      <form
        className="composer"
        onSubmit={(event) => {
          event.preventDefault();
          void send();
        }}
      >
        <input
          ref={fileInput}
          type="file"
          multiple
          hidden
          aria-label="选择文件"
          onChange={(event) => {
            setFiles(Array.from(event.currentTarget.files ?? []));
            event.currentTarget.value = '';
          }}
        />
        <button
          type="button"
          className="attach-button"
          aria-label="添加文件"
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
        <textarea
          ref={input}
          id="message-input"
          className="message-input"
          aria-label="消息"
          placeholder="发消息给阿芒"
          value={draft}
          maxLength={128000}
          rows={1}
          enterKeyHint="send"
          onChange={(event) => {
            setDraft(event.target.value);
            setError('');
          }}
          onKeyDown={(event) => {
            if (
              event.key === 'Enter' &&
              !event.shiftKey &&
              !event.ctrlKey &&
              !event.metaKey &&
              !event.nativeEvent.isComposing &&
              event.nativeEvent.keyCode !== 229
            ) {
              event.preventDefault();
              void send();
            }
          }}
        />
        <button
          type="submit"
          className="send-button"
          aria-label="发送消息"
          disabled={!draft.trim()}
        >
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
        </button>
      </form>
    </div>
  );
}
