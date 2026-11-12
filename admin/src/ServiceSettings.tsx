// 本文件是管理看板配置页的服务凭证部分：OpenRouter 只显示环境变量配置状态，其余服务凭证可独立保存。

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  api,
  ApiError,
  formatTime,
  quotaHeadline,
  type QuotaItem,
  type ServiceItem,
} from './api.js';
import { Empty } from './ui.js';

const descriptions: Record<string, string> = {
  ikun_gemini: '通过 ikun 使用 Gemini 原生模型接口。',
  ikun_claude: '通过 ikun 使用 Claude 处理 Main 对话与上下文压缩，需要 Claude 专用 Key。',
  resend: '发送正式环境的登录验证码邮件。',
  serper: 'Google 搜索。',
  parallel: '普通网页与 Reddit 正文读取。',
  everyinfra: '微信公众号与小红书搜索、正文读取，以及 X（Twitter）推文搜索。',
  aliyun_oss: '聊天图片保存到私有 Bucket，模型调用前由后端读取并转为 base64。',
  aliyun_fc: '保存 FC 沙箱 API Key。电脑的地域与模板由部署配置指定，长期工作区由维护者按用户绑定。',
};

// ServiceSettingsPage 自己负责读取；页面不自动刷新，避免覆盖正在输入的新 Key。上层用 refreshKey 触发手动刷新。
export function ServiceSettingsPage({
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
  const [items, setItems] = useState<ServiceItem[] | null>(null);
  const [quotas, setQuotas] = useState<{ items: QuotaItem[] } | null>(null);
  const [loadError, setLoadError] = useState(false);
  const generation = useRef(0);

  // 服务配置与额度明细一起加载；额度查询失败不阻断凭证管理。
  const load = useCallback(async () => {
    const current = ++generation.current;
    onLoadingChange(true);
    try {
      const [config, loadedQuotas] = await Promise.all([
        api<{ items: ServiceItem[] }>('/admin/services'),
        api<{ items: QuotaItem[] }>('/admin/quotas').catch((error: unknown) => {
          if (error instanceof ApiError && (error.status === 401 || error.status === 403)) throw error;
          return null;
        }),
      ]);
      if (current !== generation.current) return;
      setItems(config.items);
      if (loadedQuotas) setQuotas(loadedQuotas);
      setLoadError(false);
      onUpdated(`${formatTime(new Date().toISOString())} 已更新`);
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

  if (loadError && !items)
    return <Empty title="无法读取服务配置" description="连接恢复后可重新加载。" />;
  if (!items) return <div className="loading-card">正在读取服务配置…</div>;

  return (
    <>
      {loadError ? (
        <div className="load-error" role="alert">
          暂时无法读取服务配置，请刷新重试。
        </div>
      ) : null}
      <section className="model-intro">
        <div className="eyebrow teal">SERVICE SETTINGS</div>
        <h2>统一管理服务凭证</h2>
        <p>这些配置供所有用户共用。保存后从下一次请求生效；已保存的 Key 不回显完整内容，留空表示保留当前 Key。</p>
      </section>
      <div className="model-role-grid service-settings-grid">
        {items.map((service) => (
          <ServiceCard
            key={service.id}
            service={service}
            quota={quotas?.items?.find((item) => item.id === service.id)}
            onSaved={(next) => setItems(next)}
            onAccessLost={onAccessLost}
            notify={notify}
          />
        ))}
      </div>
      <p className="model-catalog-note">
        “已保存”仅表示凭证已保存；剩余额度为打开页面时向供应商实时查询的结果，查询失败或无查询接口时会说明原因。
      </p>
    </>
  );
}

// ServiceCard 是一个服务的独立保存表单：新 Key 只存在输入框和本次请求中，提交结束后清空。
function ServiceCard({
  service,
  quota,
  onSaved,
  onAccessLost,
  notify,
}: {
  service: ServiceItem;
  quota?: QuotaItem;
  onSaved: (items: ServiceItem[]) => void;
  onAccessLost: (error: ApiError) => void;
  notify: (message: string) => void;
}) {
  const [key, setKey] = useState('');
  const [accessKeyId, setAccessKeyId] = useState('');
  const [bucket, setBucket] = useState(service.oss?.bucket || '');
  const [region, setRegion] = useState(service.oss?.region || '');
  const [emailFrom, setEmailFrom] = useState(service.email_from);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const isOss = service.id === 'aliyun_oss';
  const statusLabels: Record<string, string> = {
    saved: '已保存 · 未验证',
    not_configured: '未配置',
    unreadable: '无法解密',
  };

  // OpenRouter 的 Key 随后端启动加载，看板只展示配置状态及额度。
  if (service.id === 'openrouter') {
    const configured = service.status === 'configured';
    return (
      <section className="panel model-role-card service-card">
        <div className="service-heading">
          <h3>{service.name}</h3>
          <span className={`service-status ${configured ? 'saved' : 'not_configured'}`}>
            {configured ? '已配置' : '未配置'}
          </span>
        </div>
        <p className="muted">模型目录和请求共用部署环境的 OPENROUTER_API_KEY。</p>
        <p className="service-quota">
          {quota ? `剩余额度：${quotaHeadline(quota)} · ${quota.detail}` : '剩余额度：暂时无法读取'}
        </p>
      </section>
    );
  }

  // 保存或清除单个服务；成功后重新读取脱敏状态回显，失败时只在本卡片提示。
  async function submit(clearKey: boolean) {
    if (saving) return;
    setError('');
    if (!clearKey && service.status !== 'saved' && !key) {
      setError('请输入新的 API Key。');
      return;
    }
    if (!clearKey && isOss && (key || accessKeyId) && (!key || !accessKeyId)) {
      setError('替换凭证时请同时填写 AccessKey ID 和 Secret。');
      return;
    }
    const body: Record<string, unknown> = clearKey
      ? { api_key: '' }
      : { api_key: key || null, ...(service.id === 'resend' ? { email_from: emailFrom } : {}) };
    if (!clearKey && isOss && key)
      body.api_key = JSON.stringify({
        access_key_secret: key,
        access_key_id: accessKeyId,
        bucket,
        region,
      });
    if (!clearKey && isOss) {
      body.oss_bucket = bucket;
      body.oss_region = region;
    }
    setSaving(true);
    try {
      await api<null>(`/admin/services/${service.id}`, { method: 'PUT', body: JSON.stringify(body) });
      const config = await api<{ items: ServiceItem[] }>('/admin/services');
      onSaved(config.items);
      notify(
        clearKey
          ? `${service.name} Key 已清除，后续请求将停止使用此服务。`
          : `${service.name} 配置已保存，下次请求生效；尚未验证供应商可用性。`,
      );
    } catch (loadError) {
      if (loadError instanceof ApiError && (loadError.status === 401 || loadError.status === 403)) {
        onAccessLost(loadError);
        return;
      }
      setError(
        loadError instanceof ApiError && loadError.status === 400
          ? '配置未保存。请检查凭证、Bucket、地域和发件人格式。'
          : '未能确认保存结果，请刷新查看当前配置后重试。',
      );
    } finally {
      setKey('');
      setAccessKeyId('');
      setSaving(false);
    }
  }

  return (
    <form
      className="panel model-role-card service-card"
      onSubmit={(event) => {
        event.preventDefault();
        void submit(false);
      }}
    >
      <div className="service-heading">
        <h3>{service.name}</h3>
        <span className={`service-status ${service.status}`}>{statusLabels[service.status]}</span>
      </div>
      <p className="muted">{descriptions[service.id]}</p>
      <p className="service-current">
        {service.status === 'not_configured'
          ? '当前没有 API Key'
          : service.key_suffix
            ? `当前 Key · •••• ${service.key_suffix}`
            : '当前 Key 已隐藏'}
      </p>
      <p className="service-updated">{service.updated_at ? `最近修改 ${formatTime(service.updated_at)}` : '尚未保存'}</p>
      <p className="service-quota">
        {quota ? `剩余额度：${quotaHeadline(quota)}${quota.detail ? ` · ${quota.detail}` : ''}` : '剩余额度：暂时无法读取'}
      </p>
      {service.status === 'unreadable' ? (
        <p className="form-error">无法读取当前凭证。请核对部署加密主密钥，或重新输入 Key 替换。</p>
      ) : null}
      <label className="model-picker">
        {isOss ? '新的 AccessKey Secret' : '新的 API Key'}
        <input
          type="password"
          autoComplete="new-password"
          maxLength={8192}
          spellCheck={false}
          placeholder="输入新 Key；留空保留当前值"
          aria-label={`${service.name} 新的 API Key`}
          value={key}
          disabled={saving}
          onChange={(event) => setKey(event.target.value)}
        />
      </label>
      {isOss ? (
        <>
          <label className="model-picker">
            新的 AccessKey ID
            <input
              autoComplete="off"
              spellCheck={false}
              maxLength={128}
              value={accessKeyId}
              disabled={saving}
              onChange={(event) => setAccessKeyId(event.target.value)}
            />
          </label>
          <label className="model-picker">
            Bucket
            <input
              autoComplete="off"
              spellCheck={false}
              maxLength={128}
              value={bucket}
              disabled={saving}
              onChange={(event) => setBucket(event.target.value)}
            />
          </label>
          <label className="model-picker">
            地域，例如 cn-hangzhou
            <input
              autoComplete="off"
              spellCheck={false}
              maxLength={128}
              value={region}
              disabled={saving}
              onChange={(event) => setRegion(event.target.value)}
            />
          </label>
        </>
      ) : null}
      {service.id === 'resend' ? (
        <label className="model-picker">
          发件人
          <input
            maxLength={320}
            placeholder="Aether <noreply@已验证域名>"
            value={emailFrom}
            disabled={saving}
            onChange={(event) => setEmailFrom(event.target.value)}
          />
        </label>
      ) : null}
      <div className="form-error" role="alert">
        {error}
      </div>
      <div className="service-actions">
        <button type="submit" className="button primary" disabled={saving}>
          保存配置
        </button>
        <button
          type="button"
          className="button"
          disabled={saving || service.status === 'not_configured'}
          onClick={() => void submit(true)}
        >
          清除 Key
        </button>
      </div>
    </form>
  );
}
