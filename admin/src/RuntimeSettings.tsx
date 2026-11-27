// 本文件是管理看板的全局运行设置，通过 Effect Atom 读写主动压缩提醒线与热压缩触发线。

import { useState } from 'react';
import { useAtomSet, useAtomValue } from '@effect/atom-react';
import { Exit } from 'effect';
import { AsyncResult } from 'effect/reactivity';
import { extractAdminHttpError } from './api.js';
import type { RuntimeSettings } from './api.js';
import { notifyToastAtom, runtimeSettingsAtom, saveRuntimeSettingsAtom } from './state.js';
import { Empty } from './ui.js';

// RuntimeSettingsPage 读取已保存的参数，提交时用版本校验避免多个页面互相覆盖。
export function RuntimeSettingsPage() {
  const result = useAtomValue(runtimeSettingsAtom);
  const saveRuntimeSettings = useAtomSet(saveRuntimeSettingsAtom, { mode: 'promiseExit' });
  const notify = useAtomSet(notifyToastAtom);

  const settings = AsyncResult.isSuccess(result) ? result.value : null;
  const [edited, setEdited] = useState<{
    readonly version: number;
    readonly draft: RuntimeSettings;
  } | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');

  if (AsyncResult.isInitial(result)) {
    return <div className="loading-card">正在读取运行参数…</div>;
  }
  if (!settings) {
    return <Empty title="暂时无法读取运行参数" description="暂时无法读取运行参数，请刷新重试。" />;
  }

  const draft = edited && edited.version === settings.version ? edited.draft : settings;

  // 保存成功后使用后端返回的新版本号，并自动刷新配置 Atom。
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (saving) return;
    setSaving(true);
    setSaveError('');
    try {
      const exit = await saveRuntimeSettings(draft);
      if (Exit.isFailure(exit)) {
        const httpError = extractAdminHttpError(exit.cause);
        setSaveError(
          httpError?.status === 409
            ? '运行参数已在其他页面更新，请刷新后重试。'
            : '无法确认保存结果，请刷新查看当前设置。',
        );
        return;
      }
      setEdited(null);
      notify('运行参数已保存。');
    } finally {
      setSaving(false);
    }
  }

  const changed =
    settings.compact_at_tokens !== draft.compact_at_tokens ||
    settings.hot_compact_at_tokens !== draft.hot_compact_at_tokens;

  return (
    <form onSubmit={save} className="panel model-role-card">
      <h3>全局运行参数</h3>
      <div className="model-role-grid">
        <label className="model-picker">
          主动 compact 提醒线（Token）
          <input
            type="number"
            min={1000}
            max={2000000}
            required
            disabled={saving}
            value={draft.compact_at_tokens || ''}
            onChange={(event) =>
              setEdited({
                version: settings.version,
                draft: {
                  ...draft,
                  compact_at_tokens: Number(event.target.value),
                },
              })
            }
          />
        </label>
        <label className="model-picker">
          热压缩触发线（Token）
          <input
            type="number"
            min={1000}
            max={2000000}
            required
            disabled={saving}
            value={draft.hot_compact_at_tokens || ''}
            onChange={(event) =>
              setEdited({
                version: settings.version,
                draft: {
                  ...draft,
                  hot_compact_at_tokens: Number(event.target.value),
                },
              })
            }
          />
        </label>
      </div>
      <div className="service-actions">
        <button className="button primary" type="submit" disabled={saving || !changed}>
          {saving ? '正在保存…' : '保存运行参数'}
        </button>
      </div>
      {saveError && (
        <p className="form-error" role="alert">
          {saveError}
        </p>
      )}
    </form>
  );
}
