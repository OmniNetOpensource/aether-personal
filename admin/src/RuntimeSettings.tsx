// 本文件是管理看板的全局运行设置，供管理员修改压缩阈值和闲置后台反思时长。

import { useEffect, useState } from "react";
import { api, ApiError, formatTime, type RuntimeSettings } from "./api.js";
import { Empty } from "./ui.js";

// RuntimeSettingsPage 读取已保存的参数，提交时用版本校验避免多个页面互相覆盖。
export function RuntimeSettingsPage({
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
  const [settings, setSettings] = useState<RuntimeSettings | null>(null);
  const [draft, setDraft] = useState<RuntimeSettings | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  // 手动刷新重新载入设置，卸载或刷新后的旧响应不进入当前页面。
  useEffect(() => {
    const request = new AbortController();
    onLoadingChange(true);
    setError("");
    api<RuntimeSettings>("/admin/runtime-settings", { signal: request.signal })
      .then((loaded) => {
        if (request.signal.aborted) return;
        setSettings(loaded);
        setDraft(loaded);
        onUpdated(`${formatTime(new Date().toISOString())} 已更新`);
      })
      .catch((error: unknown) => {
        if (request.signal.aborted) return;
        if (
          error instanceof ApiError &&
          (error.status === 401 || error.status === 403)
        ) {
          onAccessLost(error);
          return;
        }
        setError("暂时无法读取运行参数，请刷新重试。");
      })
      .finally(() => {
        if (!request.signal.aborted) onLoadingChange(false);
      });
    return () => request.abort();
  }, [refreshKey, onUpdated, onAccessLost, onLoadingChange]);

  // 保存成功后使用后端返回的版本作为下一次编辑的起点。
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft || saving) return;
    setSaving(true);
    onLoadingChange(true);
    setError("");
    try {
      const saved = await api<RuntimeSettings>("/admin/runtime-settings", {
        method: "PUT",
        body: JSON.stringify(draft),
      });
      setSettings(saved);
      setDraft(saved);
      notify("运行参数已保存。");
    } catch (error) {
      if (
        error instanceof ApiError &&
        (error.status === 401 || error.status === 403)
      ) {
        onAccessLost(error);
        return;
      }
      setError(
        error instanceof ApiError && error.status === 409
          ? "运行参数已在其他页面更新，请刷新后重试。"
          : "无法确认保存结果，请刷新查看当前设置。",
      );
    } finally {
      setSaving(false);
      onLoadingChange(false);
    }
  }

  if (!settings || !draft) {
    return error ? (
      <Empty title="暂时无法读取运行参数" description={error} />
    ) : (
      <div className="loading-card">正在读取运行参数…</div>
    );
  }

  const changed =
    settings.compact_at_tokens !== draft.compact_at_tokens ||
    settings.reflection_idle_minutes !== draft.reflection_idle_minutes;

  return (
    <form onSubmit={save} className="panel model-role-card">
      <h3>全局运行参数</h3>
      <div className="model-role-grid">
        <label className="model-picker">
          上下文压缩阈值（Token）
          <input
            type="number"
            min={1000}
            max={2000000}
            required
            disabled={saving}
            value={draft.compact_at_tokens || ""}
            onChange={(event) =>
              setDraft({
                ...draft,
                compact_at_tokens: Number(event.target.value),
              })
            }
          />
        </label>
        <label className="model-picker">
          闲置反思时长（分钟）
          <input
            type="number"
            min={10}
            max={10080}
            required
            disabled={saving}
            value={draft.reflection_idle_minutes || ""}
            onChange={(event) =>
              setDraft({
                ...draft,
                reflection_idle_minutes: Number(event.target.value),
              })
            }
          />
        </label>
      </div>
      <div className="service-actions">
        <button
          className="button primary"
          type="submit"
          disabled={saving || !changed}
        >
          {saving ? "正在保存…" : "保存运行参数"}
        </button>
      </div>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
    </form>
  );
}
