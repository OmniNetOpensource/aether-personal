// 本文件提供聊天内的个人模型设置浮层，支持搜索各渠道模型并保存 Main、Subagent 和压缩各自的选择。

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import type { ChatController } from '../../lib/ChatController.js';
import type { ModelSelection, ModelSettings } from '../../lib/modelSettings.js';
import './chatModels.css';

// modelRoles 定义三个选择器的顺序与各角色的跟随规则。
const modelRoles = [
  { field: 'main_model', label: 'Main', inherited: '' },
  { field: 'subagent_model', label: 'Subagent', inherited: '跟随 Main' },
  { field: 'compact_model', label: '上下文压缩', inherited: '跟随当前 Agent' },
] as const;

// ChatModelSettings 打开时读取当前配置，成功保存后返回聊天。
export function ChatModelSettings({
  controller,
  onClose,
}: {
  controller: ChatController;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [settings, setSettings] = useState<ModelSettings | null>(null);
  const [draft, setDraft] = useState<ModelSelection | null>(null);
  const [search, setSearch] = useState('');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [reload, setReload] = useState(0);

  // 原生对话框阻止背景交互，避免设置浮层影响聊天位置与草稿。
  useLayoutEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => element?.close();
  }, []);

  // 每次打开或手动重载都读取服务端版本；关闭后取消未完成的读取。
  useEffect(() => {
    const request = new AbortController();
    controller
      .getModelSettings(request.signal)
      .then((loaded) => {
        if (request.signal.aborted) return;
        setSettings(loaded);
        setDraft(loaded.selection);
      })
      .catch(() => {
        if (!request.signal.aborted) setError('暂时无法读取模型设置，请重新加载。');
      });
    return () => request.abort();
  }, [controller, reload]);

  // 重新加载替换整份草稿，适用于另一台设备已经保存过新版本的情况。
  function reloadSettings() {
    setError('');
    setSettings(null);
    setDraft(null);
    setReload((value) => value + 1);
  }

  // 先关闭原生模态，再由聊天页把键盘焦点交还给账号按钮。
  function close() {
    dialog.current?.close();
    onClose();
  }

  // 保存完成后才关闭，失败时保留当前输入供用户调整或重试。
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft || saving) return;
    setSaving(true);
    setError('');
    try {
      await controller.saveModelSettings(draft);
      close();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : '暂时无法保存，请稍后重试。');
    } finally {
      setSaving(false);
    }
  }

  const query = search.trim().toLowerCase();
  const changed =
    settings && draft && modelRoles.some(({ field }) => draft[field] !== settings.selection[field]);

  return (
    <dialog
      ref={dialog}
      className="chat-model-dialog"
      aria-labelledby="chat-model-title"
      onCancel={(event) => {
        event.preventDefault();
        if (!saving) close();
      }}
    >
      <header className="chat-model-header">
        <h2 id="chat-model-title">模型设置</h2>
        <button type="button" disabled={saving} onClick={close}>
          关闭
        </button>
      </header>
      {settings && draft ? (
        <form className="chat-model-form" onSubmit={save}>
          <label>
            搜索模型
            <input
              type="search"
              value={search}
              placeholder="输入模型名称或 ID"
              disabled={saving}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          {modelRoles.map(({ field, label, inherited }) => {
            const value = draft[field];
            const listed = settings.models.some((model) => model.id === value);
            const options = settings.models.filter(
              (model) =>
                model.id === value || `${model.name} ${model.id}`.toLowerCase().includes(query),
            );
            return (
              <label key={field}>
                {label}
                <select
                  value={value}
                  disabled={saving}
                  onChange={(event) => setDraft({ ...draft, [field]: event.target.value })}
                >
                  {inherited && <option value="">{inherited}</option>}
                  {value && !listed && <option value={value}>{value}（当前不可选）</option>}
                  {options.map((model) => (
                    <option key={model.id} value={model.id}>
                      {model.name || model.id}
                    </option>
                  ))}
                </select>
              </label>
            );
          })}
          {settings.catalog_notice && (
            <p className="chat-model-notice" role="status">
              {settings.catalog_notice}
            </p>
          )}
          <div className="chat-model-actions">
            {(error || settings.catalog_notice) && (
              <button type="button" disabled={saving} onClick={reloadSettings}>
                重新加载
              </button>
            )}
            <button type="submit" disabled={saving || !changed}>
              {saving ? '正在保存…' : '保存模型'}
            </button>
          </div>
        </form>
      ) : error ? (
        <button type="button" onClick={reloadSettings}>
          重新加载
        </button>
      ) : (
        <p className="chat-model-notice" role="status">
          正在读取模型设置…
        </p>
      )}
      {error && (
        <p className="chat-model-error" role="alert">
          {error}
        </p>
      )}
    </dialog>
  );
}
