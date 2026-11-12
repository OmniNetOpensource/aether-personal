// 本文件是管理看板配置页的模型部分：分别选择 Main、后台 Subagent 与压缩模型，填写压缩阈值与闲置后台反思时长，保存后从下一次请求或扫描生效。

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  api,
  ApiError,
  formatNumber,
  formatTime,
  type ModelCatalog,
  type ModelSettings,
} from './api.js';
import { Empty } from './ui.js';

// ModelSettingsPage 自己负责读取与保存；页面不自动刷新，避免覆盖尚未保存的选择。上层用 refreshKey 触发手动刷新。
export function ModelSettingsPage({
  refreshKey,
  onUpdated,
  onAccessLost,
  onLoadingChange,
  notify,
}: {
  refreshKey: number;
  onUpdated: (text: string) => void;
  onAccessLost: (error: ApiError) => void;
  onLoadingChange: (loading: boolean) => void;
  notify: (message: string) => void;
}) {
  const [settings, setSettings] = useState<ModelSettings | null>(null);
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null);
  const [draft, setDraft] = useState<
    Pick<
      ModelSettings,
      'main_model' | 'subagent_model' | 'compact_model' | 'compact_at_tokens' | 'reflection_idle_minutes'
    > | null
  >(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [loadError, setLoadError] = useState(false);
  const generation = useRef(0);

  // 读取当前生效配置与实时模型目录；过期的一次读取结果直接丢弃。
  const load = useCallback(async () => {
    const current = ++generation.current;
    onLoadingChange(true);
    try {
      const [loadedSettings, loadedCatalog] = await Promise.all([
        api<ModelSettings>('/admin/model-settings'),
        api<ModelCatalog>('/admin/models'),
      ]);
      if (current !== generation.current) return;
      setSettings(loadedSettings);
      setCatalog(loadedCatalog);
      setDraft({
        main_model: loadedSettings.main_model,
        subagent_model: loadedSettings.subagent_model ?? '',
        compact_model: loadedSettings.compact_model,
        compact_at_tokens: loadedSettings.compact_at_tokens,
        reflection_idle_minutes: loadedSettings.reflection_idle_minutes,
      });
      setLoadError(false);
      onUpdated(`${formatTime(loadedCatalog.fetched_at)} 已更新目录`);
    } catch (error) {
      if (current !== generation.current) return;
      if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
        onAccessLost(error);
        return;
      }
      setLoadError(true);
    } finally {
      if (current === generation.current) onLoadingChange(false);
    }
  }, [onAccessLost, onLoadingChange, onUpdated]);

  useEffect(() => {
    void load();
  }, [load, refreshKey]);

  if (loadError && !settings)
    return <Empty title="暂时无法读取模型配置" description="恢复连接后可重新加载。" />;
  if (!settings || !catalog || !draft)
    return <div className="loading-card">正在读取当前配置与可用模型…</div>;

  const selected = catalog.items.find((model) => model.id === draft.main_model);
  const selectedSubagent = catalog.items.find((model) => model.id === draft.subagent_model);
  const selectedCompact = catalog.items.find((model) => model.id === draft.compact_model);
  const changed =
    draft.main_model !== settings.main_model ||
    draft.subagent_model !== (settings.subagent_model ?? '') ||
    draft.compact_model !== settings.compact_model ||
    draft.compact_at_tokens !== settings.compact_at_tokens ||
    draft.reflection_idle_minutes !== settings.reflection_idle_minutes;
  const perMillion = (value: string) =>
    value !== '' && Number.isFinite(Number(value)) ? `$${Number(value) * 1000000}` : '未知';

  // 保存前在页面内先校验目录与数值范围，服务端仍会重新检查并用版本校验防覆盖。
  async function save(event: React.FormEvent) {
    event.preventDefault();
    if (saving || !settings || !catalog || !draft) return;
    setSaveError('');
    if (
      !catalog.items.some((model) => model.id === draft.main_model) ||
      (draft.subagent_model !== '' && !catalog.items.some((model) => model.id === draft.subagent_model)) ||
      (draft.compact_model !== '' && !catalog.items.some((model) => model.id === draft.compact_model))
    ) {
      setSaveError('请选择目录中的完整模型 ID，或刷新目录后重试。');
      return;
    }
    if (!Number.isInteger(draft.compact_at_tokens) || draft.compact_at_tokens < 1000 || draft.compact_at_tokens > 2000000) {
      setSaveError('压缩阈值需为 1000–2000000 Token 的整数。');
      return;
    }
    if (!Number.isInteger(draft.reflection_idle_minutes) || draft.reflection_idle_minutes < 10 || draft.reflection_idle_minutes > 10080) {
      setSaveError('闲置时长需为 10–10080 分钟的整数。');
      return;
    }
    const current = generation.current;
    setSaving(true);
    try {
      const saved = await api<ModelSettings>('/admin/model-settings', {
        method: 'PUT',
        body: JSON.stringify({ ...draft, version: settings.version }),
      });
      if (current !== generation.current) return;
      setSettings(saved);
      setDraft({
        main_model: saved.main_model,
        subagent_model: saved.subagent_model ?? '',
        compact_model: saved.compact_model,
        compact_at_tokens: saved.compact_at_tokens,
        reflection_idle_minutes: saved.reflection_idle_minutes,
      });
      notify('模型配置已应用，下一次请求开始生效。');
    } catch (error) {
      if (current !== generation.current) return;
      if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
        onAccessLost(error);
        return;
      }
      setSaveError(
        error instanceof ApiError && error.status === 409
          ? '其他管理员已修改配置，请刷新后重新选择。'
          : error instanceof ApiError && error.status === 400
            ? '配置无效，或模型已不可用，请检查压缩阈值、反思闲置时长与模型选择。'
            : '未能确认保存结果，请刷新查看当前生效配置后重试。',
      );
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      {loadError ? (
        <div className="load-error" role="alert">
          模型配置或目录暂时无法读取，请点击刷新重试。当前生效配置保持不变。
        </div>
      ) : null}
      <section className="model-intro">
        <div className="eyebrow teal">MODEL ROUTING</div>
        <h2>选择对话、Subagent 与压缩模型</h2>
        <p>应用于所有用户。配置保存后立即生效，服务重启后继续保留；正在执行的请求会正常完成。</p>
        <p>输出额度由供应商决定，应用不发送输出限制参数。</p>
        <div className="model-config-meta">
          <span className="pill">{settings.source === 'database' ? '已保存' : '部署默认'}</span>
          <span>
            {settings.updated_at
              ? `最近修改 ${formatTime(settings.updated_at)} · ${settings.updated_by ?? ''}`
              : '当前使用部署时配置的默认模型'}
          </span>
        </div>
      </section>
      {(catalog.warnings ?? []).map((warning) => (
        <p key={warning} className="model-catalog-note">
          {warning}
        </p>
      ))}
      <form className="model-config-form" onSubmit={save}>
        <div className="model-role-grid">
          <section className="panel model-role-card">
            <div className="model-role-heading">
              <span className="model-role-icon">◈</span>
              <h3>Main Agent</h3>
            </div>
            <p className="muted">负责每位用户的主对话、任务编排、电脑执行与结果把关。</p>
            <div className="model-active">
              <span>当前生效</span>
              <strong>{settings.main_model}</strong>
            </div>
            <label className="model-picker">
              选择 Main 模型
              <input
                list="main-model-list"
                autoComplete="off"
                placeholder="输入提供方或模型名称搜索"
                aria-label="选择 Main 模型"
                value={draft.main_model}
                disabled={saving}
                onChange={(event) => setDraft({ ...draft, main_model: event.target.value.trim() })}
              />
              <datalist id="main-model-list">
                {catalog.items.map((model) => (
                  <option key={model.id} value={model.id}>
                    {model.name}
                  </option>
                ))}
              </datalist>
            </label>
            <div className="model-price">
              {selected ? (
                <>
                  <span>
                    {`${selected.provider} · ${selected.context_length > 0 ? `${formatNumber(selected.context_length)} ${selected.provider === 'ikun Gemini' ? '输入上限' : '上下文'} Token` : '模型容量未知'}`}
                  </span>
                  <span>
                    {`输入 ${perMillion(selected.pricing.prompt)} / 输出 ${perMillion(selected.pricing.completion)} · USD / 百万 Token`}
                  </span>
                </>
              ) : (
                <span>请从可用模型目录中选择一个完整模型 ID。</span>
              )}
            </div>
          </section>
          <section className="panel model-role-card">
            <div className="model-role-heading">
              <span className="model-role-icon">◇</span>
              <h3>Subagent</h3>
            </div>
            <p className="muted">负责并行后台只读调研与独立子任务成稿，留空时自动跟随 Main。</p>
            <div className="model-active">
              <span>当前生效</span>
              <strong>{settings.subagent_model || `跟随 Main · ${settings.main_model}`}</strong>
            </div>
            <label className="model-picker">
              选择 Subagent 模型
              <input
                list="subagent-model-list"
                autoComplete="off"
                placeholder="留空跟随 Main；也可独立选择模型"
                aria-label="选择 Subagent 模型"
                value={draft.subagent_model}
                disabled={saving}
                onChange={(event) => setDraft({ ...draft, subagent_model: event.target.value.trim() })}
              />
              <datalist id="subagent-model-list">
                {catalog.items.map((model) => (
                  <option key={model.id} value={model.id}>
                    {model.name}
                  </option>
                ))}
              </datalist>
            </label>
            <div className="model-price">
              {selectedSubagent ? (
                <>
                  <span>
                    {`${selectedSubagent.provider} · ${selectedSubagent.context_length > 0 ? `${formatNumber(selectedSubagent.context_length)} ${selectedSubagent.provider === 'ikun Gemini' ? '输入上限' : '上下文'} Token` : '模型容量未知'}`}
                  </span>
                  <span>
                    {`输入 ${perMillion(selectedSubagent.pricing.prompt)} / 输出 ${perMillion(selectedSubagent.pricing.completion)} · USD / 百万 Token`}
                  </span>
                </>
              ) : draft.subagent_model === '' ? (
                <span>未单独选择时，后台 Subagent 默认跟随 Main 模型。</span>
              ) : (
                <span>请从可用模型目录中选择一个完整模型 ID，或清空以跟随 Main。</span>
              )}
            </div>
          </section>
        </div>
        <section className="panel model-role-card compact-settings">
          <h3>自动压缩上下文</h3>
          <p className="muted">
            上下文达到阈值后，在下一次调用模型前压缩；恰好是用户新消息触发时，只压它之前的历史并保留这条消息原文。空闲时不产生请求，完整聊天历史保留。
          </p>
          <div className="model-active">
            <span>当前压缩模型</span>
            <strong>{settings.compact_model || `跟随 Main · ${settings.main_model}`}</strong>
          </div>
          <label className="model-picker">
            选择压缩模型
            <input
              list="compact-model-list"
              autoComplete="off"
              placeholder="留空跟随 Main；也可独立选择模型"
              aria-label="选择压缩模型"
              value={draft.compact_model}
              disabled={saving}
              onChange={(event) => setDraft({ ...draft, compact_model: event.target.value.trim() })}
            />
            <datalist id="compact-model-list">
              {catalog.items.map((model) => (
                <option key={model.id} value={model.id}>{model.name}</option>
              ))}
            </datalist>
          </label>
          <p className="muted">
            {selectedCompact
              ? `${selectedCompact.provider} · ${selectedCompact.context_length > 0 ? `${formatNumber(selectedCompact.context_length)} Token 容量` : '模型容量未知'}。仅生成摘要，后续回复继续由当前 Agent 完成。`
              : draft.compact_model === '' ? '未单独选择时，压缩跟随当前 Agent 模型。' : '请从目录中选择完整的压缩模型 ID。'}
          </p>
          <p className="muted">摘要使用模型自身的输出额度；Claude 使用其最大输出能力。跨模型压缩不保证复用原缓存。</p>
          <div className="model-role-grid">
            <label className="model-picker">
              上下文阈值（Token）
              <input
                type="number"
                min={1000}
                max={2000000}
                step={1}
                required
                value={draft.compact_at_tokens}
                disabled={saving}
                onChange={(event) => setDraft({ ...draft, compact_at_tokens: Number(event.target.value) })}
              />
            </label>
          </div>
          <p className="muted">
            触发依据该 Agent 上一次同模型成功请求的官方输入用量，并受已知容量上限约束；请选择能容纳待压缩历史的压缩模型。
          </p>
        </section>
        <section className="panel model-role-card compact-settings">
          <h3>闲置后台反思</h3>
          <p className="muted">
            距离该用户最近一次请求结束满设定时长且当前空闲时，自动由 Main 在后台运行一次：有新对话时整理记忆，并视情况发一条近期跟进或新推荐，无事可说则保持沉默；跑完后重新计时并持续按周期触发。
          </p>
          <div className="model-role-grid">
            <label className="model-picker">
              闲置时长（分钟）
              <input
                type="number"
                min={10}
                max={10080}
                step={1}
                required
                value={draft.reflection_idle_minutes}
                disabled={saving}
                onChange={(event) => setDraft({ ...draft, reflection_idle_minutes: Number(event.target.value) })}
              />
            </label>
          </div>
        </section>
        <div className="model-save-bar">
          <div>
            <strong>下一次模型请求使用新配置</strong>
            <p className="muted">目录价格供估算，实际费用以请求日志为准。</p>
          </div>
          <button type="submit" className="button primary" disabled={saving || !changed}>
            {saving ? '正在应用…' : changed ? '应用模型配置 →' : '配置已生效'}
          </button>
        </div>
        <div className="form-error" role="alert">
          {saveError}
        </div>
      </form>
      <p className="model-catalog-note">
        {`来自已配置的 OpenRouter、ikun Gemini 与 ikun Claude 实时目录，共 ${formatNumber(catalog.items.length)} 个模型候选。ikun Claude 可用于 Main、Subagent 与压缩。目录不保证即时可用性，渠道未返回的费用显示未知。`}
      </p>
    </>
  );
}
