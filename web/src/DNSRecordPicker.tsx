import { useEffect, useRef, useState } from 'react';
import { Button, Form, Select, Typography } from 'antd';
import { api, APIError } from './api';

type Domain = { name: string; status: string };
type PickedRecord = { id: string; name: string; hostname: string; line: string; line_id: string; value: string; enabled: boolean; reason: string; owner: string };

// 绑定表单内的账号 → 主域名 → A 记录三级选择；记录 ID 与线路随所选记录带出，绑定时由面板读回并完整校验。
export function DNSRecordPicker({ accounts, onError }: { accounts: { id: string; name: string }[]; onError: (e: unknown) => void }) {
  const form = Form.useFormInstance();
  const accountID: string | undefined = Form.useWatch('account_id', form);
  const domain: string | undefined = Form.useWatch('domain', form);
  const [domains, setDomains] = useState<Domain[]>([]);
  const [records, setRecords] = useState<PickedRecord[]>([]);
  const [loading, setLoading] = useState({ domains: false, records: false });
  const [loadError, setLoadError] = useState({ domains: '', records: '' });
  // 两级列表各自计数，只采用最近一次请求的结果，切换账号或域名后旧响应作废。
  const requests = useRef({ domains: 0, records: 0 });
  const errorHandler = useRef(onError); errorHandler.current = onError;
  async function fetchList<T>(kind: 'domains' | 'records', path: string, apply: (value: T) => void) {
    const id = ++requests.current[kind];
    const current = () => id === requests.current[kind];
    setLoading(v => ({ ...v, [kind]: true })); setLoadError(v => ({ ...v, [kind]: '' }));
    try {
      const value = await api<T>(path);
      if (current()) apply(value);
    } catch (e) {
      if (!current()) return;
      if (e instanceof APIError && (e.status === 401 || e.status === 428)) errorHandler.current(e);
      else setLoadError(v => ({ ...v, [kind]: (e as Error).message }));
    } finally { if (current()) setLoading(v => ({ ...v, [kind]: false })); }
  }
  function loadDomains() {
    requests.current.domains++; setDomains([]); setLoadError(v => ({ ...v, domains: '' }));
    if (accountID) void fetchList<{ domains: Domain[] }>('domains', `/dns-accounts/${accountID}/domains`, v => setDomains(v.domains));
  }
  function loadRecords() {
    requests.current.records++; setRecords([]); setLoadError(v => ({ ...v, records: '' }));
    if (accountID && domain) void fetchList<{ records: PickedRecord[] }>('records', `/dns-accounts/${accountID}/records?domain=${encodeURIComponent(domain)}`, v => setRecords(v.records));
  }
  useEffect(loadDomains, [accountID]);
  useEffect(loadRecords, [accountID, domain]);
  const recordStatus = (r: PickedRecord) => `${r.value} · ${r.line} · ${r.enabled ? '启用' : '暂停'}`;
  return <>
    <Form.Item name="account_id" label="DNSPod 账号" rules={[{ required: true, message: '请选择账号' }]}>
      <Select placeholder="选择账号" options={accounts.map(a => ({ value: a.id, label: a.name }))} onChange={() => form.setFieldsValue({ domain: undefined, record_id: undefined, line_id: undefined })} />
    </Form.Item>
    <Form.Item name="domain" label="主域名" rules={[{ required: true, message: '请选择主域名' }]}>
      <Select showSearch optionFilterProp="label" placeholder={accountID ? '选择 DNSPod 中的域名' : '请先选择账号'} disabled={!accountID} loading={loading.domains}
        status={loadError.domains ? 'error' : undefined}
        notFoundContent={loading.domains ? '正在读取…' : '该账号下没有域名'}
        options={domains.map(d => ({ value: d.name, label: d.status && d.status !== 'enable' ? `${d.name}（${d.status === 'pause' ? '已暂停解析' : d.status}）` : d.name }))}
        onChange={() => form.setFieldsValue({ record_id: undefined, line_id: undefined })} />
    </Form.Item>
    <Form.Item name="record_id" label="A 记录" rules={[{ required: true, message: '请选择要接管的 A 记录' }]}
      extra={<>可选暂停的单线路 A 记录，不可接管的会标明原因。<Button type="link" size="small" style={{ padding: 0 }} disabled={!domain || loading.records} onClick={loadRecords}>重新读取记录</Button></>}>
      <Select showSearch optionFilterProp="label" placeholder={domain ? '选择记录' : '请先选择主域名'} disabled={!domain} loading={loading.records}
        status={loadError.records ? 'error' : undefined}
        notFoundContent={loading.records ? '正在读取…' : '该域名下没有 A 记录'}
        options={records.map(r => ({ value: r.id, label: `${r.hostname} · ${recordStatus(r)}`, disabled: !!r.reason, record: r }))}
        optionRender={option => {
          const r = option.data.record as PickedRecord;
          return <div>
            <div>{r.hostname} <Typography.Text type="secondary">{recordStatus(r)}</Typography.Text></div>
            {(r.reason || r.owner) && <Typography.Text type={r.reason ? 'danger' : 'warning'} style={{ fontSize: 12 }}>{r.reason || `已绑定：${r.owner}`}</Typography.Text>}
          </div>;
        }}
        onChange={(id: string) => form.setFieldsValue({ line_id: records.find(r => r.id === id)?.line_id })} />
    </Form.Item>
    <Form.Item name="line_id" hidden><input type="hidden" /></Form.Item>
    {(loadError.domains || loadError.records) && <Typography.Paragraph type="danger">{loadError.domains || loadError.records}</Typography.Paragraph>}
  </>;
}
