// 本文件是管理看板的运行概览页：指标卡、API 额度、调用趋势、模型与工具分布和用户使用情况。

import {
  formatCompact,
  formatDuration,
  formatMoney,
  formatNumber,
  formatTime,
  quotaHeadline,
  quotaNote,
  shortModel,
  type QuotaItem,
  type Snapshot,
} from './api.js';
import { DataTable, Empty, Identity, MetricCard, Panel, Pill } from './ui.js';

// MetricCards 是概览与调用页共用的五张指标卡，数据都来自快照的筛选范围。
export function MetricCards({ snapshot }: { snapshot: Snapshot }) {
  const m = snapshot.metrics;
  return (
    <div className="metrics">
      <MetricCard
        label="模型调用尝试"
        value={formatNumber(m.attempts)}
        note={`${formatNumber(m.succeeded)} 次成功 · ${formatNumber(m.in_flight)} 次请求中`}
      />
      <MetricCard
        label="已知模型费用"
        value={formatMoney(m.cost_known_usd)}
        note={`${formatNumber(m.cost_unknown)} 次费用未知 · USD`}
        className="cost-card"
      />
      <MetricCard
        label="已知 Token"
        value={formatCompact(m.input_tokens + m.output_tokens)}
        note={`输入 ${formatCompact(m.input_tokens)} / 输出 ${formatCompact(m.output_tokens)}`}
      />
      <MetricCard
        label="失败 / 中断"
        value={`${formatNumber(m.failed)} / ${formatNumber(m.interrupted)}`}
        note="提供方或响应失败 / 结果未知"
      />
      <MetricCard
        label="平均请求耗时"
        value={formatDuration(m.mean_duration_ms)}
        note={`基于 ${formatNumber(m.duration_samples)} 个已知耗时样本`}
      />
    </div>
  );
}

// TrendChart 用 SVG 柱状图画调用趋势，绿色是全部尝试、红色叠加失败数。
function TrendChart({ snapshot, hours }: { snapshot: Snapshot; hours: number }) {
  const values = snapshot.trend;
  if (!values.length || !values.some((item) => item.attempts))
    return <Empty title="此时间段没有调用" description="新调用将自动显示在趋势中。" />;
  const max = Math.max(...values.map((value) => value.attempts), 1);
  const width = 700 / Math.max(values.length, 1);
  const y = (value: number) => 170 - (value / max) * 140;
  const labelIndexes = [...new Set([0, Math.floor((values.length - 1) / 2), values.length - 1])];
  return (
    <div className="trend-chart">
      <svg viewBox="0 0 760 210" role="img" aria-label="模型请求调用趋势">
        {[0, 1, 2, 3, 4].map((tick) => {
          const height = 170 - tick * 35;
          return (
            <g key={tick}>
              <line x1={40} x2={746} y1={height} y2={height} stroke="var(--line)" />
              <text x={28} y={height + 4} textAnchor="end" fill="var(--text-muted)" fontSize={11}>
                {Math.round((max * tick) / 4)}
              </text>
            </g>
          );
        })}
        {values.map((item, index) => (
          <g key={item.at}>
            <rect
              x={43 + index * width}
              y={y(item.attempts)}
              width={Math.max(2, width - 7)}
              height={Math.max(0, 170 - y(item.attempts))}
              rx={3}
              fill="var(--forest)"
            >
              <title>{`${formatTime(item.at)} · ${item.attempts} 次调用`}</title>
            </rect>
            {item.failed ? (
              <rect
                x={43 + index * width}
                y={y(item.failed)}
                width={Math.max(2, width - 7)}
                height={170 - y(item.failed)}
                rx={3}
                fill="var(--error-chart)"
              />
            ) : null}
          </g>
        ))}
        {labelIndexes.map((index) => (
          <text
            key={index}
            x={43 + index * width}
            y={200}
            fill="var(--text-muted)"
            fontSize={11}
          >
            {new Date(values[index].at).toLocaleString(
              'zh-CN',
              hours <= 48
                ? { hour: '2-digit', minute: '2-digit', hour12: false }
                : { month: '2-digit', day: '2-digit' },
            )}
          </text>
        ))}
      </svg>
    </div>
  );
}

// Distribution 是模型与工具分布的横向条形列表，只显示前八名。
function Distribution({
  items,
  mode,
}: {
  items: { name: string; count: number }[] | { model: string; attempts: number }[];
  mode?: 'model';
}) {
  if (!items.length) return <Empty title="暂无记录" description="有新调用后将显示分布。" />;
  const rows = items.map((item) =>
    mode === 'model'
      ? { label: (item as { model: string }).model, count: (item as { attempts: number }).attempts }
      : { label: (item as { name: string }).name, count: (item as { count: number }).count },
  );
  const max = Math.max(...rows.map((item) => item.count), 1);
  return (
    <div className="distribution">
      {rows.slice(0, 8).map((item) => (
        <div className="distribution-row" key={item.label}>
          <div className="distribution-top">
            <span title={item.label}>{mode === 'model' ? shortModel(item.label) : item.label}</span>
            <strong>{formatNumber(item.count)}</strong>
          </div>
          <div className="bar-track">
            <div className="bar" style={{ width: `${(item.count / max) * 100}%` }} />
          </div>
        </div>
      ))}
    </div>
  );
}

// OverviewPage 拼装整个概览页；额度数据由上层按一分钟缓存加载后传入。
export function OverviewPage({
  snapshot,
  hours,
  quotas,
  onRefreshQuotas,
  onViewUserCalls,
  onViewCalls,
}: {
  snapshot: Snapshot;
  hours: number;
  quotas: { items: QuotaItem[] } | null;
  onRefreshQuotas: () => void;
  onViewUserCalls: (userId: string) => void;
  onViewCalls: () => void;
}) {
  const m = snapshot.metrics;
  const recent = snapshot.users.items.slice(0, 6);
  const first = snapshot.coverage.first_observed_at;
  return (
    <>
      <MetricCards snapshot={snapshot} />
      <Panel
        title="API 额度"
        description={
          quotas?.items
            ? '各供应商剩余额度以供应商返回为准；查不到的服务会说明原因，不会显示估算数字。'
            : '各供应商剩余额度，由后端用已保存的 Key 实时查询。'
        }
        right={
          <button type="button" className="text-button" onClick={onRefreshQuotas}>
            刷新额度
          </button>
        }
      >
        {quotas?.items ? (
          <div className="quota-grid">
            {quotas.items.map((item) => (
              <MetricCard
                key={item.id}
                label={item.name}
                value={quotaHeadline(item)}
                note={quotaNote(item)}
                className="quota-card"
              />
            ))}
          </div>
        ) : (
          <div className="loading-card">正在查询各供应商额度…</div>
        )}
      </Panel>
      <div className="coverage">
        <span className="coverage-icon">◉</span>
        <span>
          {`费用为提供方已知小计，${m.cost_unknown} 次未知；输入 / 输出 Token 分别有 ${m.input_unknown} / ${m.output_unknown} 次未知。`}
        </span>
      </div>
      <Panel
        title="调用趋势"
        description="按请求开始时间统计"
        right={
          <div className="legend">
            <span className="legend-success">调用尝试</span>
            <span className="legend-error">失败</span>
          </div>
        }
      >
        <TrendChart snapshot={snapshot} hours={hours} />
      </Panel>
      <div className="two-columns">
        <Panel title="模型使用分布" description="调用次数 · 当前筛选范围">
          <Distribution items={snapshot.models} mode="model" />
        </Panel>
        <Panel title="工具调用分布" description="模型提出的工具调用，不等于已执行成功">
          <Distribution items={snapshot.tools} />
        </Panel>
      </div>
      <Panel
        title="用户使用情况"
        description="按用户查看模型调用"
        right={
          <button type="button" className="text-button" onClick={onViewCalls}>
            查看调用 →
          </button>
        }
      >
        {recent.length ? (
          <DataTable headers={['用户', 'Agent 状态', '调用 / 失败', '已知 Token', '已知费用', '最近活动', '']}>
            {recent.map((user) => (
              <tr key={user.id}>
                <td>
                  <Identity name={user.email} id={user.id} />
                </td>
                <td>
                  <div className="cell-stack">
                    <Pill status={user.main_active ? 'processing' : 'idle'} />
                    <small>Main</small>
                  </div>
                </td>
                <td>{`${formatNumber(user.attempts)} / ${formatNumber(user.failed)}`}</td>
                <td>{formatCompact(user.input_tokens + user.output_tokens)}</td>
                <td>
                  <div className="cell-stack">
                    <strong>{formatMoney(user.cost_known_usd)}</strong>
                    <small>{`${user.cost_unknown} 次未知`}</small>
                  </div>
                </td>
                <td>{formatTime(user.last_activity_at)}</td>
                <td>
                  <button type="button" className="text-button" onClick={() => onViewUserCalls(user.id)}>
                    查看调用 →
                  </button>
                </td>
              </tr>
            ))}
          </DataTable>
        ) : (
          <Empty title="没有符合条件的用户" description="调整筛选条件，或等待用户首次登录。" />
        )}
      </Panel>
      <div className="data-note">
        {`${first ? `首次观测到调用日志：${formatTime(first)}。` : '尚未观测到调用日志。'} ${formatNumber(snapshot.coverage.legacy_steps)} 条历史步骤未计入调用统计；崩溃中断的请求可能已产生费用。`}
      </div>
    </>
  );
}
