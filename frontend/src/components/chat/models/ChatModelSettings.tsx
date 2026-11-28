// 本文件提供聊天内的个人模型设置浮层，通过 Effect Atom 拉取模型目录并提交 Main 与 Subagent 各自的选择。

import { useState } from 'react';
import type { FormEvent } from 'react';
import { useAtomRefresh, useAtomSet, useAtomValue } from '@effect/atom-react';
import { Exit } from 'effect';
import { AsyncResult } from 'effect/reactivity';
import { formatCauseError } from '../../../lib/errors.js';
import type { ModelSelection } from '../../../lib/modelSettings.js';
import { modelSettingsAtom, saveModelSettingsAtom } from '../../../lib/runtime.js';
import { ModalDialog } from '../../ModalDialog.js';
import './chatModels.css';

// modelRoles 定义两个选择器的顺序与各角色的跟随规则。
const modelRoles = [
  { field: 'main_model', label: 'Main', inherited: '' },
  { field: 'subagent_model', label: 'Subagent', inherited: '跟随 Main' },
] as const;

// ChatModelSettings 打开时通过 Atom 读取配置并在关闭时自动取消在途请求，保存成功后返回聊天。
export function ChatModelSettings({ token, onClose }: { token: string; onClose: () => void }) {
  const settingsAtom = modelSettingsAtom(token);
  const result = useAtomValue(settingsAtom);
  const refreshSettings = useAtomRefresh(settingsAtom);
  const saveModelSettings = useAtomSet(saveModelSettingsAtom, { mode: 'promiseExit' });

  const settings = AsyncResult.isSuccess(result) ? result.value : null;
  const [edited, setEdited] = useState<{
    readonly base: ModelSelection;
    readonly draft: ModelSelection;
  } | null>(null);
  const [search, setSearch] = useState('');
  const [saveError, setSaveError] = useState('');
  const [saving, setSaving] = useState(false);

  const draft = settings
    ? edited && edited.base === settings.selection
      ? edited.draft
      : settings.selection
    : null;

  // 重新加载替换整份草稿，适用于另一台设备已经保存过新版本的情况。
  function reloadSettings() {
    setSaveError('');
    setEdited(null);
    refreshSettings();
  }

  // 保存完成后才关闭，失败时保留当前输入供用户调整或重试。
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft || saving) return;
    setSaving(true);
    setSaveError('');
    try {
      const exit = await saveModelSettings({ token, selection: draft });
      if (Exit.isFailure(exit)) {
        setSaveError(formatCauseError(exit.cause, '暂时无法保存，请稍后重试。'));
        return;
      }
      onClose();
    } finally {
      setSaving(false);
    }
  }

  const loadError = AsyncResult.isFailure(result) ? '暂时无法读取模型设置，请重新加载。' : '';
  const error = saveError || loadError;
  const query = search.trim().toLowerCase();
  const changed =
    settings && draft && modelRoles.some(({ field }) => draft[field] !== settings.selection[field]);

  return (
    <ModalDialog
      canClose={!saving}
      closeOnBackdrop={false}
      className="chat-model-dialog"
      ariaLabelledBy="chat-model-title"
      onClose={onClose}
    >
      <header className="chat-model-header">
        <h2 id="chat-model-title">模型设置</h2>
        <button type="button" disabled={saving} onClick={onClose}>
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
                  onChange={(event) =>
                    setEdited({
                      base: settings.selection,
                      draft: { ...draft, [field]: event.target.value },
                    })
                  }
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
    </ModalDialog>
  );
}
