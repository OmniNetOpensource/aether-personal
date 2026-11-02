// 本文件是多模态编辑器：用 Tiptap 管理文字、引用和图片的光标顺序、删除与撤销，外部只读写结构化内容。

import { Editor, Node, mergeAttributes } from '@tiptap/core';
import type { JSONContent, NodeViewRendererProps } from '@tiptap/core';
import StarterKit from '@tiptap/starter-kit';
import { useEffect, useRef } from 'react';
import { createRoot } from 'react-dom/client';
import { ImagePreview } from './ImagePreview.js';
import { messagePartSchema } from './messageAttachments.js';
import type { MessagePart } from './messageAttachments.js';

// editorDocument 将结构化内容转换为编辑器节点，换行单独保留为 hardBreak。
function editorDocument(parts: MessagePart[]): JSONContent {
  return {
    type: 'doc',
    content: [
      {
        type: 'paragraph',
        content: parts.flatMap((part): JSONContent[] => {
          if (part.type !== 'text') return [{ type: 'contentChip', attrs: { part } }];
          return part.text
            .split('\n')
            .flatMap((text, index) => [
              ...(index ? [{ type: 'hardBreak' }] : []),
              ...(text ? [{ type: 'text', text }] : []),
            ]);
        }),
      },
    ],
  };
}

// messageParts 只从编辑器的受控节点读内容；HTML 粘贴不会导入任意附件或引用。
function messageParts(json: JSONContent): MessagePart[] {
  const parts: MessagePart[] = [];
  const append = (text: string) => {
    const last = parts[parts.length - 1];
    if (last?.type === 'text') last.text += text;
    else if (text) parts.push({ type: 'text', text });
  };
  for (const [index, paragraph] of (json.content ?? []).entries()) {
    if (index) append('\n');
    for (const node of paragraph.content ?? []) {
      if (node.type === 'text') append(node.text ?? '');
      else if (node.type === 'hardBreak') append('\n');
      else if (node.type === 'contentChip') parts.push(messagePartSchema.parse(node.attrs?.part));
    }
  }
  return parts;
}

// chipView 用带留白的外壳包住不可编辑卡片，让原生光标落在卡片外；删除仍通过可撤销的编辑器事务工作。
function chipView(props: NodeViewRendererProps) {
  let current = props.node;
  const dom = document.createElement('span');
  dom.contentEditable = 'false';
  dom.className = 'composer-node';
  const chip = document.createElement('span');
  dom.append(chip);
  const root = createRoot(chip);
  const render = () => {
    const part = messagePartSchema.parse(current.attrs.part);
    if (part.type === 'text') return;
    chip.className = `composer-chip composer-chip--${part.type}`;
    root.render(
      <>
        {part.type === 'quote' ? (
          <span className="composer-quote" title={part.quote.text}>
            <svg
              className="composer-quote-icon"
              viewBox="0 0 24 24"
              fill="currentColor"
              aria-hidden="true"
            >
              <path d="M10 5H3v7h4c0 2-1 3-3 4v3c4-1 6-4 6-8V5Zm11 0h-7v7h4c0 2-1 3-3 4v3c4-1 6-4 6-8V5Z" />
            </svg>
            <span className="composer-quote-text">{part.quote.text}</span>
          </span>
        ) : (
          <ImagePreview image={part.image} />
        )}
        <button
          type="button"
          className="chip-remove"
          aria-label={part.type === 'quote' ? '移除引用' : `移除 ${part.image.name}`}
          title={part.type === 'quote' ? '移除引用' : '移除图片'}
          onMouseDown={(event) => event.preventDefault()}
          onClick={() => {
            const position = props.getPos();
            if (typeof position === 'number')
              props.view.dispatch(
                props.view.state.tr.delete(position, position + current.nodeSize),
              );
          }}
        >
          <svg viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <path
              d="m4 4 8 8M12 4l-8 8"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
            />
          </svg>
        </button>
      </>,
    );
  };
  render();
  return {
    dom,
    stopEvent: (event: Event) =>
      event.target instanceof Element && Boolean(event.target.closest('button, dialog')),
    update(node: typeof props.node) {
      if (node.type !== current.type) return false;
      current = node;
      render();
      return true;
    },
    destroy() {
      queueMicrotask(() => root.unmount());
    },
  };
}

const ContentChip = Node.create({
  name: 'contentChip',
  group: 'inline',
  inline: true,
  atom: true,
  selectable: true,
  addAttributes() {
    return { part: { default: null, rendered: false } };
  },
  // HTML 中不接受自带的 chip，只能由本地插入命令创建。
  parseHTML() {
    return [];
  },
  renderHTML({ HTMLAttributes }) {
    return ['span', mergeAttributes(HTMLAttributes, { 'data-content-chip': '' })];
  },
  renderText({ node }) {
    const part = messagePartSchema.parse(node.attrs.part);
    return part.type === 'quote'
      ? `引用：${part.quote.text}`
      : part.type === 'image'
        ? `图片：${part.image.name}`
        : part.text;
  },
  addNodeView() {
    return chipView;
  },
});

// RichMessageInputHandle 只暴露输入框需要的操作，父组件不直接操作编辑器 DOM。
export interface RichMessageInputHandle {
  insert: (parts: MessagePart[]) => void;
  clear: () => void;
  focus: () => void;
}

// RichMessageInput 在同一个编辑区域内混排内容，保留 IME、Enter 换行、Ctrl+Enter 发送和粘贴图片行为。
export function RichMessageInput(props: {
  inputRef: React.RefObject<RichMessageInputHandle | null>;
  initialParts: MessagePart[];
  onChange: (parts: MessagePart[]) => void;
  onFiles: (files: File[]) => void;
  onSubmit: () => void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const latest = useRef(props);
  latest.current = props;
  useEffect(() => {
    if (!host.current) return;
    const editor = new Editor({
      element: host.current,
      extensions: [
        StarterKit.configure({
          blockquote: false,
          bold: false,
          bulletList: false,
          code: false,
          codeBlock: false,
          dropcursor: false,
          gapcursor: false,
          heading: false,
          horizontalRule: false,
          italic: false,
          link: false,
          listItem: false,
          listKeymap: false,
          orderedList: false,
          strike: false,
          trailingNode: false,
          underline: false,
        }),
        ContentChip,
      ],
      content: editorDocument(latest.current.initialParts),
      editorProps: {
        attributes: {
          id: 'message-input',
          class: 'message-input rich-message-input',
          role: 'textbox',
          'aria-label': '消息',
          'aria-multiline': 'true',
          'data-placeholder': '发消息给阿芒',
          enterkeyhint: 'enter',
          'aria-keyshortcuts': 'Control+Enter',
        },
        handleKeyDown: (view, event) => {
          if (
            event.key === 'Enter' &&
            !event.shiftKey &&
            event.ctrlKey &&
            !event.metaKey &&
            !event.isComposing &&
            !view.composing &&
            event.keyCode !== 229
          ) {
            event.preventDefault();
            latest.current.onSubmit();
            return true;
          }
          return false;
        },
        handlePaste: (view, event) => {
          const files = Array.from(event.clipboardData?.files ?? []);
          if (files.length) {
            event.preventDefault();
            latest.current.onFiles(files);
            return true;
          }
          const text = event.clipboardData?.getData('text/plain');
          if (text) {
            event.preventDefault();
            view.dispatch(view.state.tr.insertText(text));
            return true;
          }
          return false;
        },
      },
      onUpdate: ({ editor: updated }) => latest.current.onChange(messageParts(updated.getJSON())),
    });
    latest.current.inputRef.current = {
      insert: (parts) =>
        editor
          .chain()
          .focus(undefined, { scrollIntoView: false })
          .insertContent(editorDocument(parts).content?.[0].content ?? [])
          .run(),
      clear: () => {
        editor.commands.clearContent();
        editor.commands.focus(undefined, { scrollIntoView: false });
      },
      focus: () => editor.commands.focus(undefined, { scrollIntoView: false }),
    };
    const inputRef = latest.current.inputRef;
    return () => {
      inputRef.current = null;
      editor.destroy();
    };
  }, []);
  return <div className="rich-input-host" ref={host} />;
}
