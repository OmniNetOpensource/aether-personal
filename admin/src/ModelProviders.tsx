// 本文件是管理看板配置页的自定义模型供应商管理：通过 Effect Atom 读取、新增、编辑与删除外部模型供应商，完整 Key 不回显。

import { useState } from 'react';
import { useAtomSet, useAtomValue } from '@effect/atom-react';
import { Exit, Option } from 'effect';
import { AsyncResult } from 'effect/reactivity';
import { extractAdminHttpError, formatTime } from './api.js';
import type { ModelProvider, ModelProviderProtocol } from './api.js';
import {
  createModelProviderAtom,
  deleteModelProviderAtom,
  modelProvidersAtom,
  notifyToastAtom,
  updateModelProviderAtom,
} from './state.js';
import { Empty } from './ui.js';

const protocolOptions: {
  readonly value: ModelProviderProtocol;
  readonly label: string;
  readonly placeholder: string;
  readonly hint: string;
}[] = [
  {
    value: 'openai',
    label: 'OpenAI 兼容（Chat Completions）',
    placeholder: 'https://api.openai.com/v1',
    hint: '后端将请求 {Base URL}/chat/completions 与 {Base URL}/models。',
  },
  {
    value: 'openai_responses',
    label: 'OpenAI 原生（Responses）',
    placeholder: 'https://api.openai.com/v1',
    hint: '后端将请求 {Base URL}/responses 与 {Base URL}/models。',
  },
  {
    value: 'gemini',
    label: 'Gemini 原生（generateContent）',
    placeholder: 'https://generativelanguage.googleapis.com',
    hint: '后端将请求 {Base URL}/v1beta/models/...。',
  },
  {
    value: 'claude',
    label: 'Claude 原生（Messages）',
    placeholder: 'https://api.anthropic.com',
    hint: '后端将请求 {Base URL}/v1/messages 与 {Base URL}/v1/models。',
  },
];

function protocolMeta(protocol: ModelProviderProtocol) {
  return protocolOptions.find((item) => item.value === protocol) ?? protocolOptions[0];
}

// ModelProvidersPage 通过 Atom 读取自定义模型供应商列表；刷新失败时保留上次成功结果。
export function ModelProvidersPage() {
  const result = useAtomValue(modelProvidersAtom);
  const items = AsyncResult.isSuccess(result)
    ? result.value
    : AsyncResult.isFailure(result) && Option.isSome(result.previousSuccess)
      ? result.previousSuccess.value.value
      : null;
  const loadError = AsyncResult.isFailure(result);

  if (loadError && !items)
    return <Empty title="无法读取模型供应商" description="连接恢复后可重新加载。" />;
  if (!items) return <div className="loading-card">正在读取模型供应商…</div>;

  return (
    <>
      {loadError ? (
        <div className="load-error" role="alert">
          暂时无法读取模型供应商，请刷新重试。
        </div>
      ) : null}
      <section className="model-intro">
        <div className="eyebrow teal">MODEL PROVIDERS</div>
        <h2>自定义模型供应商</h2>
        <p>
          按协议配置任意官方或兼容模型端点（OpenAI 兼容、OpenAI 原生 Responses、Gemini 原生、Claude
          原生）。所有渠道共用同一套模型协议层，与内置 OpenRouter 并存；保存后立即出现在 App
          的模型目录中。
        </p>
      </section>
      <div className="model-role-grid service-settings-grid">
        {items.map((provider) => (
          <ModelProviderCard key={`${provider.id}:${provider.updated_at}`} provider={provider} />
        ))}
        <CreateModelProviderCard />
      </div>
    </>
  );
}

// ModelProviderCard 编辑或删除单个已保存的模型供应商；新 Key 留空则沿用原加密凭证。
function ModelProviderCard({ provider }: { provider: ModelProvider }) {
  const updateProvider = useAtomSet(updateModelProviderAtom, { mode: 'promiseExit' });
  const deleteProvider = useAtomSet(deleteModelProviderAtom, { mode: 'promiseExit' });
  const notify = useAtomSet(notifyToastAtom);

  const [name, setName] = useState(provider.name);
  const [protocol, setProtocol] = useState<ModelProviderProtocol>(provider.protocol);
  const [baseURL, setBaseURL] = useState(provider.base_url);
  const [apiKey, setApiKey] = useState('');
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const meta = protocolMeta(protocol);

  // 提交单条供应商修改；成功后清空密码框并由 Atom 自动刷新列表。
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setError('');
    setBusy(true);
    try {
      const exit = await updateProvider({
        id: provider.id,
        name,
        protocol,
        base_url: baseURL,
        api_key: apiKey || null,
      });
      if (Exit.isFailure(exit)) {
        const httpError = extractAdminHttpError(exit.cause);
        setError(
          httpError?.status === 409
            ? '已存在同名的模型供应商，请更换名称。'
            : httpError?.status === 400
              ? '配置未保存。请检查名称、协议、HTTPS 地址与 API Key 格式。'
              : '未能确认保存结果，请刷新后重试。',
        );
        return;
      }
      setApiKey('');
      notify(`${exit.value.name} 配置已保存，下次请求生效。`);
    } finally {
      setBusy(false);
    }
  }

  // 删除供应商配置；需二次点击确认，避免误删影响正在使用该渠道的用户。
  async function remove() {
    if (busy) return;
    setError('');
    setBusy(true);
    try {
      const exit = await deleteProvider(provider.id);
      if (Exit.isFailure(exit)) {
        const httpError = extractAdminHttpError(exit.cause);
        setError(
          httpError?.status === 404 ? '该供应商已不存在，请刷新页面。' : '删除失败，请刷新后重试。',
        );
        return;
      }
      notify(`${provider.name} 已删除。`);
    } finally {
      setBusy(false);
      setConfirmDelete(false);
    }
  }

  return (
    <form className="panel model-role-card service-card" onSubmit={(event) => void save(event)}>
      <div className="service-heading">
        <h3>{provider.name}</h3>
        <span className={`service-status ${provider.status}`}>
          {provider.status === 'unreadable' ? '无法解密' : '已保存 · 未验证'}
        </span>
      </div>
      <p className="muted">{meta.hint}</p>
      <p className="service-current">
        {provider.key_suffix ? `当前 Key · •••• ${provider.key_suffix}` : '当前 Key 已隐藏'}
      </p>
      <p className="service-updated">最近修改 {formatTime(provider.updated_at)}</p>
      {provider.status === 'unreadable' ? (
        <p className="form-error">
          无法读取当前凭证。请核对部署加密主密钥，或输入新的 API Key 替换。
        </p>
      ) : null}
      <label className="model-picker">
        显示名称
        <input
          maxLength={64}
          required
          value={name}
          disabled={busy}
          onChange={(event) => setName(event.target.value)}
        />
      </label>
      <label className="model-picker">
        模型协议
        <select
          value={protocol}
          disabled={busy}
          onChange={(event) => setProtocol(event.target.value as ModelProviderProtocol)}
        >
          {protocolOptions.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      </label>
      <label className="model-picker">
        Base URL
        <input
          type="url"
          autoComplete="off"
          spellCheck={false}
          maxLength={2048}
          required
          placeholder={meta.placeholder}
          value={baseURL}
          disabled={busy}
          onChange={(event) => setBaseURL(event.target.value)}
        />
      </label>
      <label className="model-picker">
        新的 API Key
        <input
          type="password"
          autoComplete="new-password"
          maxLength={8192}
          spellCheck={false}
          placeholder="输入新 Key；留空保留当前值"
          value={apiKey}
          disabled={busy}
          onChange={(event) => setApiKey(event.target.value)}
        />
      </label>
      <div className="form-error" role="alert">
        {error}
      </div>
      <div className="service-actions">
        <button type="submit" className="button primary" disabled={busy}>
          保存修改
        </button>
        {confirmDelete ? (
          <>
            <button
              type="button"
              className="button danger"
              disabled={busy}
              onClick={() => void remove()}
            >
              确认删除
            </button>
            <button
              type="button"
              className="button"
              disabled={busy}
              onClick={() => setConfirmDelete(false)}
            >
              取消
            </button>
          </>
        ) : (
          <button
            type="button"
            className="button"
            disabled={busy}
            onClick={() => setConfirmDelete(true)}
          >
            删除
          </button>
        )}
      </div>
    </form>
  );
}

// CreateModelProviderCard 提供新增自定义模型供应商的表单卡片，提交后清空明文 Key。
function CreateModelProviderCard() {
  const createProvider = useAtomSet(createModelProviderAtom, { mode: 'promiseExit' });
  const notify = useAtomSet(notifyToastAtom);

  const [name, setName] = useState('');
  const [protocol, setProtocol] = useState<ModelProviderProtocol>('openai');
  const [baseURL, setBaseURL] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);

  const meta = protocolMeta(protocol);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (saving) return;
    setError('');
    setSaving(true);
    try {
      const exit = await createProvider({
        name,
        protocol,
        base_url: baseURL,
        api_key: apiKey,
      });
      if (Exit.isFailure(exit)) {
        const httpError = extractAdminHttpError(exit.cause);
        setError(
          httpError?.status === 409
            ? '已存在同名的模型供应商，请更换名称。'
            : httpError?.status === 400
              ? '未能添加。请检查名称、协议、HTTPS 地址与 API Key 格式。'
              : '未能确认添加结果，请刷新后重试。',
        );
        return;
      }
      setName('');
      setBaseURL('');
      setApiKey('');
      notify(`已添加模型供应商 ${exit.value.name}。`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <form className="panel model-role-card service-card" onSubmit={(event) => void submit(event)}>
      <div className="service-heading">
        <h3>添加模型供应商</h3>
        <span className="service-status">新配置</span>
      </div>
      <p className="muted">{meta.hint}</p>
      <label className="model-picker">
        显示名称
        <input
          maxLength={64}
          required
          placeholder="例如 Google Gemini、Anthropic、DeepSeek"
          value={name}
          disabled={saving}
          onChange={(event) => setName(event.target.value)}
        />
      </label>
      <label className="model-picker">
        模型协议
        <select
          value={protocol}
          disabled={saving}
          onChange={(event) => setProtocol(event.target.value as ModelProviderProtocol)}
        >
          {protocolOptions.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      </label>
      <label className="model-picker">
        Base URL
        <input
          type="url"
          autoComplete="off"
          spellCheck={false}
          maxLength={2048}
          required
          placeholder={meta.placeholder}
          value={baseURL}
          disabled={saving}
          onChange={(event) => setBaseURL(event.target.value)}
        />
      </label>
      <label className="model-picker">
        API Key
        <input
          type="password"
          autoComplete="new-password"
          maxLength={8192}
          spellCheck={false}
          required
          placeholder="输入供应商 API Key"
          value={apiKey}
          disabled={saving}
          onChange={(event) => setApiKey(event.target.value)}
        />
      </label>
      <div className="form-error" role="alert">
        {error}
      </div>
      <div className="service-actions">
        <button type="submit" className="button primary" disabled={saving}>
          {saving ? '正在添加…' : '添加供应商'}
        </button>
      </div>
    </form>
  );
}
