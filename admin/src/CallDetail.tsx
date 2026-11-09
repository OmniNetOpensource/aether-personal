// 本文件展示运行过程中的模型调用详情，按需加载实际请求与响应 JSON。

import { useEffect, useRef, useState } from 'react';
import {
  api,
  ApiError,
  formatDuration,
  formatMoney,
  formatNumber,
  formatTime,
  type Call,
  type CallDetail,
} from './api.js';
import { Pill, RequestIds } from './ui.js';

// CallDetailDialog 是模型调用详情弹窗：顶部先展示元数据，再按调用 ID 单独加载请求与响应正文。
export function CallDetailDialog({
  call,
  onClose,
  onAccessLost,
  notify,
}: {
  call: Call;
  onClose: () => void;
  onAccessLost: (error: ApiError) => void;
  notify: (message: string) => void;
}) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const [detail, setDetail] = useState<CallDetail | null>(null);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);

  // 弹窗随选中调用打开，关闭时通知上层清掉选中状态。
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog && !dialog.open) dialog.showModal();
  }, []);

  // 详情正文单独加载，不把完整上下文塞进列表；关闭或切换调用后丢弃过期结果。
  useEffect(() => {
    let cancelled = false;
    setDetail(null);
    setError('');
    api<CallDetail>(`/admin/calls/${call.id}`)
      .then((data) => {
        if (!cancelled) setDetail(data);
      })
      .catch((loadError: unknown) => {
        if (cancelled) return;
        if (loadError instanceof ApiError && (loadError.status === 401 || loadError.status === 403)) {
          onAccessLost(loadError);
          return;
        }
        setError(loadError instanceof Error ? loadError.message : '加载失败，请稍后重试。');
      });
    return () => {
      cancelled = true;
    };
  }, [call.id, attempt, onAccessLost]);

  const compactReasons: Record<string, string> = {
    user_message: '用户新消息',
    idle: '回答后超时',
    tokens: '上下文阈值',
    capacity: '模型容量',
  };
  const fields: [string, React.ReactNode][] = [
    ['用途', call.purpose === 'compact' ? '上下文压缩' : 'Agent 推理'],
    ['压缩原因', compactReasons[call.compact_reason ?? ''] || '—'],
    [
      '压缩前 Token（上次官方用量）',
      call.context_tokens_before == null ? '—' : formatNumber(call.context_tokens_before),
    ],
    ['摘要覆盖序号', call.summary_through_sequence ?? '—'],
    ['用户', call.email],
    ['Agent', 'Main'],
    ['请求模型', call.requested_model],
    ['响应模型', call.response_model],
    ['结果', <Pill key="outcome" status={call.outcome} />],
    ['HTTP 状态', call.http_status],
    ['失败分类', call.failure_kind || '—'],
    ['开始时间', formatTime(call.started_at)],
    ['结束时间', formatTime(call.finished_at)],
    ['请求耗时', formatDuration(call.duration_ms)],
    [
      '输入 / 输出 Token',
      `${call.input_tokens == null ? '未知' : formatNumber(call.input_tokens)} / ${call.output_tokens == null ? '未知' : formatNumber(call.output_tokens)}`,
    ],
    ['提供方费用 USD', formatMoney(call.cost)],
    ['模型提出的工具', call.tool_names?.join('、') || '无'],
    ['逻辑步骤 ID', call.step_id],
  ];

  return (
    <dialog
      id="detail"
      ref={dialogRef}
      onClick={(event) => {
        if (event.target === dialogRef.current) onClose();
      }}
      onClose={onClose}
    >
      <div id="detail-content">
        <div className="detail-header">
          <div>
            <div className="eyebrow teal">MODEL REQUEST</div>
            <h2>模型调用详情</h2>
            <p>调用元数据与实际请求、响应 JSON</p>
          </div>
          <button type="button" className="close-button" onClick={() => dialogRef.current?.close()}>
            ×
          </button>
        </div>
        <RequestIds id={call.id} notify={notify} />
        <dl className="details-grid">
          {fields.map(([label, value]) => (
            <div key={label}>
              <dt>{label}</dt>
              <dd>{value == null ? '未知' : value}</dd>
            </div>
          ))}
        </dl>
        <div className="data-note">请求中断或连接失败时，提供方可能已执行并计费。未知费用不等于 0。</div>
        <section className="request-json">
          <h3>请求与响应 JSON</h3>
          <p className="muted">按本次实际调用保存，包含消息上下文与工具定义；不含 HTTP 认证头。</p>
          <div className="request-json-content">
            {error ? (
              <>
                <p className="form-error">{error}</p>
                <button type="button" className="button" onClick={() => setAttempt((value) => value + 1)}>
                  重新加载
                </button>
              </>
            ) : !detail ? (
              '正在读取请求与响应…'
            ) : (
              <>
                <PayloadSection label="请求 JSON" value={detail.request} rawText={null} truncated={false} />
                <PayloadSection
                  label="响应 JSON"
                  value={detail.response}
                  rawText={detail.response_text}
                  truncated={detail.response_truncated}
                />
              </>
            )}
          </div>
        </section>
      </div>
    </dialog>
  );
}

// PayloadSection 展示一段请求或响应正文：有效 JSON 格式化显示，非 JSON 显示原始文本，未记录时说明原因。
function PayloadSection({
  label,
  value,
  rawText,
  truncated,
}: {
  label: string;
  value: unknown;
  rawText: string | null;
  truncated: boolean;
}) {
  const isRequest = label === '请求 JSON';
  return (
    <section className="payload-section">
      <h4>{label}</h4>
      {truncated ? <p className="data-note">响应超过 8 MiB 接收上限，以下仅为已接收的部分正文。</p> : null}
      {value == null && rawText == null ? (
        <p className="data-note">
          {isRequest
            ? '该日志产生时尚未记录请求 JSON，无法还原当时的实际请求。新调用会自动保存。'
            : '该调用未记录响应正文，或尚未收到响应。新调用收到正文后会自动保存。'}
        </p>
      ) : (
        <>
          {rawText != null ? (
            <p className="data-note">提供方返回的正文不是有效 JSON，以下展示原始文本。</p>
          ) : null}
          <pre className="json-code" tabIndex={0} aria-label={label}>
            {rawText ?? JSON.stringify(value, null, 2)}
          </pre>
        </>
      )}
    </section>
  );
}
