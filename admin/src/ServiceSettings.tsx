// 本文件是管理看板配置页的服务凭证部分：通过 Effect Atom 并发拉取脱敏状态与实时额度，并独立提交各服务凭证。

import { useState } from 'react';
import { useAtomSet, useAtomValue } from '@effect/atom-react';
import { Exit, Option } from 'effect';
import { AsyncResult } from 'effect/reactivity';
import { extractAdminHttpError, formatTime, quotaHeadline } from './api.js';
import type { QuotaItem, ServiceItem } from './api.js';
import { notifyToastAtom, saveServiceSettingAtom, serviceSettingsAtom } from './state.js';
import { Empty } from './ui.js';

const descriptions: Record<string, string> = {
  resend: '发送正式环境的登录验证码邮件。',
  serper: 'Google 搜索。',
  parallel: '普通网页与 Reddit 正文读取。',
  everyinfra: '微信公众号与小红书搜索、正文读取，以及 X（Twitter）推文搜索。',
  aliyun_oss: '聊天图片保存到私有 Bucket，模型调用前由后端读取并转为 base64。',
  aliyun_fc: '保存 FC 沙箱 API Key。电脑的地域与模板由部署配置指定，长期工作区由维护者按用户绑定。',
};

// ServiceSettingsPage 订阅服务配置与额度 Atom；刷新失败时保留上次成功加载的表单状态。
export function ServiceSettingsPage() {
  const result = useAtomValue(serviceSettingsAtom);
  const data = AsyncResult.isSuccess(result)
    ? result.value
    : AsyncResult.isFailure(result) && Option.isSome(result.previousSuccess)
      ? result.previousSuccess.value.value
      : null;
  const loadError = AsyncResult.isFailure(result);

  if (loadError && !data)
    return <Empty title="无法读取服务配置" description="连接恢复后可重新加载。" />;
  if (!data) return <div className="loading-card">正在读取服务配置…</div>;

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
        <p>
          这些配置供所有用户共用。保存后从下一次请求生效；已保存的 Key
          不回显完整内容，留空表示保留当前 Key。
        </p>
      </section>
      <div className="model-role-grid service-settings-grid">
        {data.items.map((service) => (
          <ServiceCard
            key={service.id}
            service={service}
            quota={data.quotas?.find((item) => item.id === service.id)}
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
function ServiceCard({ service, quota }: { service: ServiceItem; quota?: QuotaItem }) {
  const saveService = useAtomSet(saveServiceSettingAtom, { mode: 'promiseExit' });
  const notify = useAtomSet(notifyToastAtom);

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

  // 保存或清除单个服务；成功后由 Atom 自动重拉脱敏状态回显，失败时只在本卡片提示。
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
      const exit = await saveService({ serviceID: service.id, body });
      if (Exit.isFailure(exit)) {
        const httpError = extractAdminHttpError(exit.cause);
        setError(
          httpError?.status === 400
            ? '配置未保存。请检查凭证、Bucket、地域和发件人格式。'
            : '未能确认保存结果，请刷新查看当前配置后重试。',
        );
        return;
      }
      notify(
        clearKey
          ? `${service.name} Key 已清除，后续请求将停止使用此服务。`
          : `${service.name} 配置已保存，下次请求生效；尚未验证供应商可用性。`,
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
      <p className="service-updated">
        {service.updated_at ? `最近修改 ${formatTime(service.updated_at)}` : '尚未保存'}
      </p>
      <p className="service-quota">
        {quota
          ? `剩余额度：${quotaHeadline(quota)}${quota.detail ? ` · ${quota.detail}` : ''}`
          : '剩余额度：暂时无法读取'}
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
            placeholder="aether <noreply@已验证域名>"
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
