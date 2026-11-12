// 本文件展示任务进展浮层：实时呈现当前与排队任务及近期已结束记录，并支持对等待回复的任务直接附带上下文发送回复。

import { useLayoutEffect, useRef, useState } from 'react';
import { isOpenTask, taskStatusLabel } from '../../lib/ChatController.js';
import type { ChatController } from '../../lib/ChatController.js';
import type { ChatTask } from '../../lib/serverEvents.js';
import './chatTasks.css';

// formatTaskTime 将任务更新时间格式化为简短中文时间，同一天省略月日。
function formatTaskTime(isoString: string): string {
  const date = new Date(isoString);
  const now = new Date();
  const sameDay =
    date.getFullYear() === now.getFullYear() &&
    date.getMonth() === now.getMonth() &&
    date.getDate() === now.getDate();
  if (sameDay) {
    return date.toLocaleTimeString('zh-CN', {
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    });
  }
  const sameYear = date.getFullYear() === now.getFullYear();
  return date.toLocaleString('zh-CN', {
    ...(sameYear ? {} : { year: 'numeric' }),
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}

// isLongDescription 判断未结束任务的需求描述是否超过三行折叠阈值。
function isLongDescription(description: string): boolean {
  return Array.from(description).length > 140 || description.split('\n').length > 3;
}

// summarizeOpenCounts 生成浮层顶部的队列状态概览文案。
function summarizeOpenCounts(openTasks: ChatTask[]): string {
  if (openTasks.length === 0) return '当前没有未完成的任务';
  const inProgress = openTasks.filter((task) => task.status === 'in_progress').length;
  const queued = openTasks.filter((task) => task.status === 'queued').length;
  const waiting = openTasks.filter((task) => task.status === 'waiting').length;
  const parts: string[] = [];
  if (inProgress > 0) parts.push(`${inProgress} 项进行中`);
  if (queued > 0) parts.push(`${queued} 项排队`);
  if (waiting > 0) parts.push(`${waiting} 项等待回复`);
  return parts.join(' · ');
}

// ChatTaskSheet 使用原生模态对话框展示实时任务快照，打开期间继续接收后端状态变更，并允许直接回复等待中的任务。
export function ChatTaskSheet({
  controller,
  onClose,
  onSent,
}: {
  controller: ChatController;
  onClose: () => void;
  onSent?: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const closeButton = useRef<HTMLButtonElement>(null);
  const [expandedIDs, setExpandedIDs] = useState(() => new Set<string>());
  const [replyDrafts, setReplyDrafts] = useState<Record<string, string>>(() =>
    Object.fromEntries(controller.taskReplyDrafts),
  );
  const [replyErrors, setReplyErrors] = useState<Record<string, string>>({});
  const [sendingTaskID, setSendingTaskID] = useState<string | null>(null);
  const sendingRef = useRef(false);
  // 每张等待回复卡片复用自己的消息 ID，超时重发时服务端按 ID 去重不重复保存。
  const replyMessageIDs = useRef(new Map<string, string>());

  useLayoutEffect(() => {
    const element = dialog.current;
    element?.showModal();
    closeButton.current?.focus({ preventScroll: true });
    return () => element?.close();
  }, []);

  const openTasks = controller.tasks.filter(isOpenTask);
  const closedTasks = controller.tasks.filter((task) => !isOpenTask(task));

  const toggleExpanded = (taskID: string) => {
    setExpandedIDs((current) => {
      const next = new Set(current);
      if (next.has(taskID)) next.delete(taskID);
      else next.add(taskID);
      return next;
    });
  };

  // updateReplyDraft 同步更新组件草稿与控制器内存缓存，并在编辑后轮换重发去重 ID。
  const updateReplyDraft = (taskID: string, value: string) => {
    if (value) controller.taskReplyDrafts.set(taskID, value);
    else controller.taskReplyDrafts.delete(taskID);
    replyMessageIDs.current.set(taskID, crypto.randomUUID());
    setReplyDrafts((current) => ({ ...current, [taskID]: value }));
    setReplyErrors((current) => {
      if (!current[taskID]) return current;
      const next = { ...current };
      delete next[taskID];
      return next;
    });
  };

  // submitWaitingReply 将等待回复任务的标题、说明与用户补充拼成普通用户消息发出，交由 Agent 核对并更新状态。
  const submitWaitingReply = async (task: ChatTask) => {
    const trimmed = (replyDrafts[task.id] ?? '').trim();
    if (sendingRef.current || !trimmed) return;
    const description = task.description.trim();
    const text =
      `针对等待回复的任务「${task.title}」\n` +
      (description ? `任务说明：${description}\n\n` : '\n') +
      trimmed;
    if (Array.from(text).length > 64000) {
      setReplyErrors((current) => ({
        ...current,
        [task.id]: '回复与任务说明合计不能超过 64,000 个字符',
      }));
      return;
    }
    let clientMessageID = replyMessageIDs.current.get(task.id);
    if (!clientMessageID) {
      clientMessageID = crypto.randomUUID();
      replyMessageIDs.current.set(task.id, clientMessageID);
    }
    sendingRef.current = true;
    setSendingTaskID(task.id);
    setReplyErrors((current) => {
      if (!current[task.id]) return current;
      const next = { ...current };
      delete next[task.id];
      return next;
    });
    try {
      if (await controller.send([{ type: 'text', text }], clientMessageID)) {
        controller.taskReplyDrafts.delete(task.id);
        replyMessageIDs.current.delete(task.id);
        setReplyDrafts((current) => {
          const next = { ...current };
          delete next[task.id];
          return next;
        });
        if (onSent) onSent();
        else onClose();
      }
    } catch (error) {
      setReplyErrors((current) => ({
        ...current,
        [task.id]: `发送失败：${error instanceof Error ? error.message : '请稍后重试'}`,
      }));
    } finally {
      sendingRef.current = false;
      setSendingTaskID(null);
    }
  };

  // renderTaskCard 渲染单张任务卡片：待办默认展示前几行说明，等待回复附带输入框，已结束任务按需展开详情。
  const renderTaskCard = (task: ChatTask, closed: boolean) => {
    const expanded = expandedIDs.has(task.id);
    const collapsible = closed || isLongDescription(task.description);
    const showDescription = !closed || expanded;
    const clamped = !closed && collapsible && !expanded;
    const pulsing =
      task.status === 'in_progress' && controller.connected && controller.mainThinking;
    const replyDraft = replyDrafts[task.id] ?? '';
    const replyError = replyErrors[task.id] ?? '';
    const sendingCurrent = sendingTaskID === task.id;

    return (
      <li key={task.id}>
        <article className={`task-card is-${task.status}`}>
          <div className="task-card-meta">
            <span className={`task-status-pill is-${task.status}`}>
              {task.status === 'in_progress' && (
                <span
                  className={`task-live-dot${pulsing ? ' is-pulsing' : ''}`}
                  aria-hidden="true"
                />
              )}
              {taskStatusLabel(task.status)}
            </span>
            <time className="task-card-time" dateTime={task.updated_at}>
              {formatTaskTime(task.updated_at)}
            </time>
          </div>
          <h4 className="task-card-title">{task.title}</h4>
          {showDescription && task.description && (
            <p className={`task-card-description${clamped ? ' is-clamped' : ''}`}>
              {task.description}
            </p>
          )}
          {collapsible && task.description && (
            <button
              type="button"
              className="task-card-toggle"
              aria-expanded={expanded}
              onClick={() => toggleExpanded(task.id)}
            >
              {expanded ? '收起说明' : closed ? '查看说明' : '展开完整说明'}
            </button>
          )}
          {!closed && task.status === 'waiting' && (
            <form
              className="task-reply-form"
              onSubmit={(event) => {
                event.preventDefault();
                void submitWaitingReply(task);
              }}
            >
              <textarea
                className="task-reply-input"
                rows={2}
                value={replyDraft}
                disabled={sendingCurrent}
                placeholder="输入补充回复，或输入“取消”结束这项任务…"
                aria-label={`回复任务：${task.title}`}
                onChange={(event) => updateReplyDraft(task.id, event.currentTarget.value)}
                onKeyDown={(event) => {
                  if (
                    event.key === 'Enter' &&
                    (event.ctrlKey || event.metaKey) &&
                    !event.nativeEvent.isComposing
                  ) {
                    event.preventDefault();
                    void submitWaitingReply(task);
                  }
                }}
              />
              {replyError && (
                <p className="error task-reply-error" role="alert">
                  {replyError}
                </p>
              )}
              <div className="task-reply-actions">
                <span className="task-reply-hint" aria-hidden="true">
                  <kbd>Ctrl</kbd>
                  <span>↵ 发送</span>
                </span>
                <button
                  type="submit"
                  className={`task-reply-send${sendingCurrent ? ' is-sending' : ''}`}
                  disabled={Boolean(sendingTaskID) || !replyDraft.trim()}
                >
                  {sendingCurrent ? '发送中…' : '发送回复'}
                </button>
              </div>
            </form>
          )}
        </article>
      </li>
    );
  };

  return (
    <dialog
      ref={dialog}
      className="task-sheet-dialog"
      aria-label="任务进展"
      onKeyDown={(event) => {
        if (
          event.key === 'Escape' &&
          !event.nativeEvent.isComposing &&
          event.target instanceof Element &&
          event.target.closest('dialog') === event.currentTarget
        ) {
          event.preventDefault();
          onClose();
        }
      }}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClick={(event) => {
        if (event.target !== event.currentTarget) return;
        const rect = event.currentTarget.getBoundingClientRect();
        if (
          event.clientX < rect.left ||
          event.clientX > rect.right ||
          event.clientY < rect.top ||
          event.clientY > rect.bottom
        ) {
          onClose();
        }
      }}
    >
      <section className="task-sheet-panel">
        <header className="task-sheet-header">
          <div className="task-sheet-title">
            <h2>任务进展</h2>
            <button
              ref={closeButton}
              type="button"
              className="task-sheet-close"
              onClick={onClose}
              aria-label="关闭任务进展"
            >
              <span className="task-sheet-desktop-close" aria-hidden="true">
                ×
              </span>
              <span className="task-sheet-mobile-close" aria-hidden="true">
                关闭
              </span>
            </button>
          </div>
          <div className="task-sheet-summary">
            <span>{summarizeOpenCounts(openTasks)}</span>
            <span className="task-sheet-sync">
              <span
                className={`task-live-dot${controller.connected && controller.mainThinking ? ' is-pulsing' : ''}`}
                aria-hidden="true"
              />
              {!controller.connected
                ? '连接已断开'
                : controller.mainThinking
                  ? '阿芒处理中'
                  : '实时同步'}
            </span>
          </div>
        </header>

        <div className="task-sheet-body">
          {controller.tasks.length === 0 ? (
            <p className="task-sheet-empty">
              还没有登记的任务。在对话中提出需求后，阿芒会自动在这里登记并同步进展。
            </p>
          ) : (
            <>
              {openTasks.length > 0 && (
                <section className="task-group" aria-label="当前与待办任务">
                  <h3 className="task-group-heading">当前与待办（{openTasks.length}）</h3>
                  <ul className="task-list">
                    {openTasks.map((task) => renderTaskCard(task, false))}
                  </ul>
                </section>
              )}
              {closedTasks.length > 0 && (
                <section className="task-group" aria-label="近期已结束任务">
                  <h3 className="task-group-heading">近期已结束（{closedTasks.length}）</h3>
                  <ul className="task-list">
                    {closedTasks.map((task) => renderTaskCard(task, true))}
                  </ul>
                </section>
              )}
            </>
          )}
        </div>

        <footer className="task-sheet-footer">
          <span>等待回复的任务可直接在此补充；调整先后或取消其他任务可在聊天里告诉阿芒</span>
          <span className="task-sheet-escape">
            <kbd>Esc</kbd> 关闭
          </span>
        </footer>
      </section>
    </dialog>
  );
}
