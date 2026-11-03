// 本文件是管理看板的共用小组件：状态徽章、面板、空状态、指标卡、ID 复制与长文本折叠，各页面拼装时复用。

import { useRef, useState, type ReactNode } from 'react';
import { outcomeLabels } from './api.js';

// Pill 是结果状态徽章，样式类名直接沿用结果取值。
export function Pill({ status }: { status: string }) {
  return <span className={`pill ${status}`}>{outcomeLabels[status] || status}</span>;
}

// Empty 是列表为空时的占位说明。
export function Empty({ title, description }: { title: string; description: string }) {
  return (
    <div className="empty">
      <div className="empty-icon">◇</div>
      <h3>{title}</h3>
      <p>{description}</p>
    </div>
  );
}

// Panel 是带标题与说明的内容面板，右侧可放一个操作控件。
export function Panel({
  title,
  description,
  right,
  children,
}: {
  title: string;
  description?: string;
  right?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="panel">
      <div className="panel-head">
        <div>
          <h2>{title}</h2>
          {description ? <p>{description}</p> : null}
        </div>
        {right}
      </div>
      {children}
    </section>
  );
}

// MetricCard 是概览顶部的一张数字卡片。
export function MetricCard({
  label,
  value,
  note,
  className = '',
}: {
  label: string;
  value: string;
  note: string;
  className?: string;
}) {
  return (
    <article className={`metric-card ${className}`}>
      <div className="metric-label">{label}</div>
      <strong className="metric-value">{value}</strong>
      <p className="metric-note">{note}</p>
    </article>
  );
}

// CopyChip 把一条 ID 显示在按钮外面，点击「复制」写入完整值，不必从长 JSON 里挑选。
export function CopyChip({
  label,
  value,
  notify,
}: {
  label: string;
  value: string;
  notify: (message: string) => void;
}) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // 选中复制必须发生在点击当次，不能等 Clipboard API 异步失败后再做。页面没有焦点时直接走这条。
  function copy() {
    const copySelection = () => {
      const area = document.createElement('textarea');
      area.value = value;
      area.setAttribute('readonly', '');
      area.style.position = 'fixed';
      area.style.left = '-9999px';
      document.body.append(area);
      area.select();
      const ok = document.execCommand('copy');
      area.remove();
      return ok;
    };
    const finish = (ok: boolean) => {
      if (!ok) {
        notify('无法写入剪贴板，请手动选择 ID');
        return;
      }
      setCopied(true);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1200);
    };
    if (!navigator.clipboard?.writeText || !document.hasFocus()) {
      finish(copySelection());
      return;
    }
    navigator.clipboard.writeText(value).then(
      () => finish(true),
      () => finish(copySelection()),
    );
  }

  return (
    <span className="copy-id">
      <span className="copy-id-label">{label}</span>
      <code className="copy-id-value">{value}</code>
      <button type="button" className="copy-id-button" aria-label={`复制${label}`} onClick={copy}>
        {copied ? '已复制' : '复制'}
      </button>
    </span>
  );
}

// RequestIds 把这次模型请求在本系统中的调用 ID 放到列表、时间线与详情顶部，供直接复制。
export function RequestIds({ id, notify }: { id: string; notify: (message: string) => void }) {
  return (
    <div className="request-ids">
      <CopyChip label="调用 ID" value={id} notify={notify} />
    </div>
  );
}

// Foldable 把长文本放进可折叠区域；展开状态按 storageKey 记在外部 Map 里，自动刷新重绘后保持不变。短文本默认展开。
export function Foldable({
  storageKey,
  label,
  text,
  className = '',
  openState,
}: {
  storageKey: string;
  label: string;
  text: string;
  className?: string;
  openState: Map<string, boolean>;
}) {
  const preview = text.replace(/\s+/g, ' ').slice(0, 120);
  return (
    <details
      className={`timeline-fold ${className}`}
      open={openState.get(storageKey) ?? text.length <= 280}
      onToggle={(event) => openState.set(storageKey, (event.target as HTMLDetailsElement).open)}
    >
      <summary>
        <span className="fold-label">{label}</span>
        <span className="fold-preview">{preview}</span>
      </summary>
      <pre className="timeline-text">{text}</pre>
    </details>
  );
}

// DataTable 是看板各列表共用的表格外壳。
export function DataTable({ headers, children }: { headers: string[]; children: ReactNode }) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            {headers.map((header) => (
              <th key={header}>{header}</th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

// Identity 是表格里「名称 + ID」的两行单元格。
export function Identity({ name, id }: { name: string; id: string }) {
  return (
    <div className="identity">
      <strong>{name}</strong>
      <small>{id}</small>
    </div>
  );
}
