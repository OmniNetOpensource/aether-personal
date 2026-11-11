// 本文件是管理看板的运行过程页：按时间从新到旧渲染 Main 的输入、模型回复、工具调用与失败请求，长内容折叠，图片按需加载展示。

import type { ReactNode } from 'react';
import {
  callMeta,
  formatDuration,
  formatTime,
  shortModel,
  type Call,
  type Snapshot,
  type Timeline,
  type TimelineItem,
} from './api.js';
import { CopyChip, Empty, Foldable, Panel, Pill, RequestIds } from './ui.js';

// TimelinePage 需要的外部状态与操作都由上层持有，自动刷新重绘后展开与全文不丢失。
export interface TimelineViewProps {
  user: string;
  snapshot: Snapshot;
  timeline: (Timeline & { key: string }) | null;
  openState: Map<string, boolean>;
  fullItems: Map<string, TimelineItem>;
  onLoadFull: (id: string, button: HTMLButtonElement) => void;
  onLoadEarlier: (button: HTMLButtonElement) => void;
  onOpenCall: (call: Call) => void;
  notify: (message: string) => void;
}

// TimelinePage 按时间降序渲染 Main 的时间线：最新在上，工具结果并入对应的工具调用卡片，未产出消息的请求单独显示。
export function TimelinePage(props: TimelineViewProps) {
  const { user, snapshot, timeline } = props;
  if (!user)
    return <Empty title="选择一个用户" description="在上方选择用户并点击查询，查看该用户 Main 的运行过程。" />;
  if (!timeline || timeline.key !== user)
    return <div className="loading-card">正在读取运行过程…</div>;
  const summary = snapshot.agents.items[0];
  const email = snapshot.users.items.find((item) => item.id === user)?.email ?? user;
  const results = new Map(
    timeline.items.filter((item) => item.role === 'tool').map((item) => [item.tool_call_id, item]),
  );
  const paired = new Set(
    timeline.items.flatMap((item) => (item.tool_calls ?? []).map((call) => call.id)).filter((id) => results.has(id)),
  );
  return (
    <Panel
      title={`Main · ${email}`}
      description="最新在上；按查询条件显示消息与请求，工具结果并入对应调用。"
      right={summary ? <Pill status={summary.status} /> : null}
    >
      <ol className="timeline">
        {timeline.system_prompt ? (
          <TimelineNode kind="runtime" title="不可见指令" at={null}>
            <span className="timeline-meta">每次请求的第一条 user 消息，不落库，随部署生效</span>
            <Foldable
              storageKey="instructions"
              label="展开查看"
              text={timeline.system_prompt.text}
              openState={props.openState}
            />
          </TimelineNode>
        ) : null}
        {[...timeline.items].reverse().map((item, index) => {
          if (item.type === 'call' && item.call)
            return <FailedCallNode key={`call-${item.call.id}-${index}`} item={item} email={email} {...props} />;
          if (item.role === 'assistant')
            return <AssistantNode key={item.id ?? index} item={item} results={results} email={email} {...props} />;
          if (item.role === 'tool') {
            if (item.tool_call_id && paired.has(item.tool_call_id)) return null;
            return <ToolResultNode key={item.id ?? index} item={item} {...props} />;
          }
          return <InputNode key={item.id ?? index} item={item} email={email} {...props} />;
        })}
        {!timeline.items.length ? <li className="timeline-empty">没有符合查询条件的运行记录。</li> : null}
        {timeline.next_cursor ? (
          <li className="timeline-more">
            <button type="button" className="button" onClick={(event) => props.onLoadEarlier(event.currentTarget)}>
              加载更早的记录
            </button>
          </li>
        ) : null}
      </ol>
    </Panel>
  );
}

// TimelineNode 是时间线上一个节点的外框：标题行显示名称、时间与附加信息。
function TimelineNode({
  kind,
  title,
  at,
  extras,
  children,
}: {
  kind: string;
  title: string;
  at: string | null;
  extras?: ReactNode;
  children: ReactNode;
}) {
  return (
    <li className={`timeline-node ${kind}`}>
      <div className="timeline-head">
        <span className="timeline-title">{title}</span>
        <time className="timeline-time">{formatTime(at)}</time>
        {extras}
      </div>
      {children}
    </li>
  );
}

// fullView 优先使用已加载的全文版本。
function fullView(item: TimelineItem, fullItems: Map<string, TimelineItem>): TimelineItem {
  return (item.id ? fullItems.get(item.id) : undefined) ?? item;
}

// LoadFullButton 在节点仍被截断时提供按条加载全文（与截图）的入口。
function LoadFullButton({ item, ...props }: { item: TimelineItem } & TimelineViewProps) {
  if (!item.truncated || !item.id || props.fullItems.has(item.id)) return null;
  return (
    <button
      type="button"
      className="text-button timeline-load"
      onClick={(event) => props.onLoadFull(item.id!, event.currentTarget)}
    >
      {item.image_count ? '加载全文与截图' : '加载全文'}
    </button>
  );
}

// TimelineImages 统一展示输入与工具结果中的图片；尚未加载预览链接时先显示数量。
function TimelineImages({ item }: { item: TimelineItem }) {
  return item.images?.length ? (
    <div className="timeline-images">
      {item.images.map((url) => (
        <img key={url} src={url} alt="截图" />
      ))}
    </div>
  ) : item.image_count ? (
    <p className="timeline-muted">{`包含 ${item.image_count} 张截图`}</p>
  ) : null;
}

// triggerTitles 区分落库的定时触发与三种闲置后台工作消息，避免时间线里统称系统通知。
const triggerTitles: Record<string, string> = {
  schedule_event: '定时任务触发',
  memory_update: '闲置记忆整理',
  recent_followup: '闲置近期跟进',
  unknown_unknowns: '闲置未知探索',
};

// InputNode 是用户输入、定时触发、闲置后台工作与压缩摘要节点的渲染。
function InputNode({
  item,
  email,
  ...props
}: { item: TimelineItem; email: string } & TimelineViewProps) {
  const view = fullView(item, props.fullItems);
  const call = item.is_summary ? item.call : undefined;
  const withEmail = call ? { ...call, email } : null;
  const senders: Record<string, [string, string]> = {
    user: ['input', '用户输入'],
    main: ['input', '来自 Main'],
    system: ['runtime', '系统通知'],
  };
  const triggerTitle = item.name ? triggerTitles[item.name] : undefined;
  const [kind, title] = item.is_summary
    ? ['summary', '上下文压缩摘要']
    : triggerTitle
      ? ['runtime', triggerTitle]
      : (senders[item.sender ?? ''] ?? ['runtime', '运行时提示']);
  return (
    <TimelineNode
      kind={kind}
      title={title}
      at={item.at}
      extras={
        call ? (
          <>
            <span className="timeline-meta">{callMeta(call)}</span>
            {withEmail ? (
              <button type="button" className="text-button" onClick={() => props.onOpenCall(withEmail)}>
                请求详情 →
              </button>
            ) : null}
          </>
        ) : undefined
      }
    >
      {call ? <RequestIds id={call.id} notify={props.notify} /> : null}
      {item.is_summary ? (
        <Foldable storageKey={`${item.id}:text`} label="摘要内容" text={view.text ?? ''} openState={props.openState} />
      ) : view.text ? (
        <div className="timeline-bubble">{view.text}</div>
      ) : null}
      <TimelineImages item={view} />
      <LoadFullButton item={item} {...props} />
    </TimelineNode>
  );
}

// AssistantNode 是一次模型回复：推理、正文（直接发给用户）与它提出的工具调用卡片。
function AssistantNode({
  item,
  results,
  email,
  ...props
}: { item: TimelineItem; results: Map<string | undefined, TimelineItem>; email: string } & TimelineViewProps) {
  const view = fullView(item, props.fullItems);
  const call = item.call;
  const withEmail = call ? { ...call, email } : null;
  return (
    <TimelineNode
      kind="assistant"
      title="模型回复"
      at={item.at}
      extras={
        <>
          <span className="timeline-meta">{call ? callMeta(call) : '未关联请求日志'}</span>
          {withEmail ? (
            <button type="button" className="text-button" onClick={() => props.onOpenCall(withEmail)}>
              请求详情 →
            </button>
          ) : null}
        </>
      }
    >
      {call ? <RequestIds id={call.id} notify={props.notify} /> : null}
      {view.reasoning?.state === 'text' ? (
        <Foldable
          storageKey={`${item.id}:reasoning`}
          label="推理过程"
          text={view.reasoning.text ?? ''}
          className="reasoning"
          openState={props.openState}
        />
      ) : (
        <p className="timeline-muted">
          {view.reasoning?.state === 'encrypted' ? '推理已加密，供应商未返回原文' : '模型未返回推理'}
        </p>
      )}
      {view.text ? (
        <Foldable
          storageKey={`${item.id}:text`}
          label="正文（直接发给用户）"
          text={view.text}
          openState={props.openState}
        />
      ) : null}
      {(view.tool_calls ?? []).map((toolCall) => (
        <ToolCard key={toolCall.id} toolCallId={toolCall.id} name={toolCall.name} args={toolCall.arguments} result={results.get(toolCall.id)} {...props} />
      ))}
      <LoadFullButton item={item} {...props} />
    </TimelineNode>
  );
}

// prettyArguments 尝试把工具参数格式化成缩进 JSON，失败时原样返回。
function prettyArguments(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

// ToolCard 把一次工具调用与它的结果放在一起，并展示可复制的工具调用 ID。
function ToolCard({
  toolCallId,
  name,
  args,
  result,
  ...props
}: {
  toolCallId: string;
  name: string;
  args: string;
  result?: TimelineItem;
} & TimelineViewProps) {
  const view = result ? fullView(result, props.fullItems) : null;
  return (
    <div className="tool-card">
      <div className="tool-card-head">
        <strong>{name}</strong>
        {result ? (
          <span className="timeline-meta">{formatTime(result.at)}</span>
        ) : (
          <span className="timeline-meta pending">尚未返回结果</span>
        )}
      </div>
      {toolCallId ? <CopyChip label="工具调用 ID" value={toolCallId} notify={props.notify} /> : null}
      <Foldable storageKey={`${toolCallId}:args`} label="参数" text={prettyArguments(args)} openState={props.openState} />
      {result && view ? (
        <>
          <Foldable storageKey={`${toolCallId}:result`} label="结果" text={view.text ?? ''} openState={props.openState} />
          <TimelineImages item={view} />
          <LoadFullButton item={result} {...props} />
        </>
      ) : null}
    </div>
  );
}

// ToolResultNode 是找不到对应调用卡片时单独出现的工具结果。
function ToolResultNode({ item, ...props }: { item: TimelineItem } & TimelineViewProps) {
  const view = fullView(item, props.fullItems);
  return (
    <TimelineNode
      kind="tool"
      title="工具结果"
      at={item.at}
      extras={<span className="timeline-meta">对应的调用在更早的记录中</span>}
    >
      {item.tool_call_id ? <CopyChip label="工具调用 ID" value={item.tool_call_id} notify={props.notify} /> : null}
      <Foldable storageKey={`${item.id}:text`} label="结果" text={view.text ?? ''} openState={props.openState} />
      <TimelineImages item={view} />
      <LoadFullButton item={item} {...props} />
    </TimelineNode>
  );
}

// FailedCallNode 是没有产出消息的模型请求：失败、中断或仍在进行中。
function FailedCallNode({
  item,
  email,
  ...props
}: { item: TimelineItem; email: string } & TimelineViewProps) {
  const call = item.call!;
  return (
    <TimelineNode
      kind={`call ${call.outcome}`}
      title={call.purpose === 'compact' ? '上下文压缩请求' : '模型请求'}
      at={item.at}
      extras={
        <>
          <Pill status={call.outcome} />
          <button type="button" className="text-button" onClick={() => props.onOpenCall({ ...call, email })}>
            请求详情 →
          </button>
        </>
      }
    >
      <RequestIds id={call.id} notify={props.notify} />
      <p className="timeline-muted">
        {`${shortModel(call.requested_model)} · HTTP ${call.http_status ?? '—'} · ${call.failure_kind || '进行中'} · ${formatDuration(call.duration_ms)}`}
      </p>
    </TimelineNode>
  );
}
