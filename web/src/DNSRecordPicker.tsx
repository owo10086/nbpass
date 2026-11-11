import { useEffect, useRef, useState } from 'react';
import { App, AutoComplete, Button, Form, Modal, Radio, Select, Space, Typography } from 'antd';
import { api, APIError } from './api';

type Domain = { name: string; status: string };
type PickedRecord = { id: string; name: string; type: string; hostname: string; line: string; line_id: string; value: string; enabled: boolean; reason: string; owner: string };
type ProviderRecord = { id: string; name: string; type: string; line: string; line_id: string; value: string; enabled: boolean };
export type RecordTarget = { account_id: string; domain: string; name: string; type?: 'A' | 'CNAME' };
type Choice = { message: string; candidates: ProviderRecord[]; conflicts: ProviderRecord[]; values: RecordTarget };

// 绑定表单内的账号 → 主域名 → 主机记录名（主备入口组还可选 CNAME）；没有同名记录时面板新建，已有时接管，由绑定接口完整校验。
export function DNSRecordPicker({ accounts, onError, allowCNAME }: { accounts: { id: string; name: string }[]; onError: (e: unknown) => void; allowCNAME?: boolean }) {
  const form = Form.useFormInstance();
  const accountID: string | undefined = Form.useWatch('account_id', form);
  const domain: string | undefined = Form.useWatch('domain', form);
  const kind: string = Form.useWatch('type', form) ?? 'A';
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
  const recordStatus = (r: PickedRecord) => `${r.type} ${r.value} · ${r.line} · ${r.enabled ? '启用' : '暂停'}`;
  return <>
    <Form.Item name="account_id" label="DNSPod 账号" rules={[{ required: true, message: '请选择账号' }]}>
      <Select placeholder="选择账号" options={accounts.map(a => ({ value: a.id, label: a.name }))} onChange={() => form.setFieldsValue({ domain: undefined, name: undefined })} />
    </Form.Item>
    <Form.Item name="domain" label="主域名" rules={[{ required: true, message: '请选择主域名' }]}>
      <Select showSearch optionFilterProp="label" placeholder={accountID ? '选择 DNSPod 中的域名' : '请先选择账号'} disabled={!accountID} loading={loading.domains}
        status={loadError.domains ? 'error' : undefined}
        notFoundContent={loading.domains ? '正在读取…' : '该账号下没有域名'}
        options={domains.map(d => ({ value: d.name, label: d.status && d.status !== 'enable' ? `${d.name}（${d.status === 'pause' ? '已暂停解析' : d.status}）` : d.name }))} />
    </Form.Item>
    {allowCNAME && <Form.Item name="type" label="记录类型" initialValue="A"
      extra={kind === 'CNAME' ? 'CNAME 指向活动入口自己的节点记录域名（节点“域名与地址”里绑定的记录），切换时只改指向；没有已发布节点记录的成员不能成为活动入口。' : 'A 记录直接写活动入口的公网 IPv4。'}>
      <Radio.Group options={[{ value: 'A', label: 'A 记录' }, { value: 'CNAME', label: 'CNAME 记录' }]} />
    </Form.Item>}
    <Form.Item label="主机记录" required
      extra={<>填 www、@ 等，记录类型为 {kind}{allowCNAME ? '' : '（这里须直接指向节点 IP，CNAME 只用于主备入口组）'}。没有同名记录时面板在默认线路新建一条暂停的记录，首次发布或加回时启用；已有时接管，有多条或类型不同时再让你选。<Button type="link" size="small" style={{ padding: 0 }} disabled={!domain || loading.records} onClick={loadRecords}>重新读取已有记录</Button></>}>
      <Space.Compact style={{ width: '100%' }}>
        <Form.Item name="name" noStyle rules={[{ required: true, whitespace: true, message: '请填写主机记录名' }]}>
          <AutoComplete placeholder={domain ? '输入或选择已有记录' : '请先选择主域名'} disabled={!domain} allowClear
            filterOption={(input, option) => String(option?.value ?? '').includes(input.trim().toLowerCase())}
            options={records.map(r => ({ value: r.name, label: <div>
              <div>{r.hostname} <Typography.Text type="secondary">{recordStatus(r)}</Typography.Text></div>
              {(r.reason || r.owner) && <Typography.Text type={r.reason ? 'danger' : 'warning'} style={{ fontSize: 12 }}>{r.reason || `已绑定：${r.owner}`}</Typography.Text>}
            </div> })).filter((o, i, all) => all.findIndex(x => x.value === o.value) === i)} />
        </Form.Item>
        {domain && <Typography.Text type="secondary" style={{ padding: '4px 11px', whiteSpace: 'nowrap' }}>.{domain}</Typography.Text>}
      </Space.Compact>
    </Form.Item>
    {(loadError.domains || loadError.records) && <Typography.Paragraph type="danger">{loadError.domains || loadError.records}</Typography.Paragraph>}
  </>;
}

// useRecordBinding 提交按名字绑定；面板要求再选（多条同名或类型冲突）时弹出选择，选定后带着选择重新提交。
export function useRecordBinding(put: (body: Record<string, unknown>) => Promise<{ created?: boolean } | unknown>, run: (action: () => Promise<unknown>) => Promise<void>) {
  const { message } = App.useApp();
  const [choice, setChoice] = useState<Choice | null>(null);
  const [picked, setPicked] = useState('');
  async function attempt(values: RecordTarget, extra: Record<string, unknown>) {
    try {
      const result = await put({ account_id: values.account_id, domain: values.domain, name: values.name, type: values.type ?? 'A', ...extra }) as { created?: boolean } | undefined;
      setChoice(null);
      if (result?.created) void message.success('已在 DNSPod 新建暂停的记录，首次发布或加回时启用');
    } catch (e) {
      if (!(e instanceof APIError && e.status === 409 && (e.details.candidates || e.details.conflicts))) throw e;
      setPicked('');
      setChoice({ message: e.message, candidates: (e.details.candidates ?? []) as ProviderRecord[], conflicts: (e.details.conflicts ?? []) as ProviderRecord[], values });
    }
  }
  function confirm() {
    if (!choice || !picked) return;
    const [kind, id] = picked.split(':');
    const extra = kind === 'adopt' ? { record_id: id } : kind === 'convert' ? { convert_record_id: id } : { create: true };
    void run(() => attempt(choice.values, extra));
  }
  const kind = choice?.values.type ?? 'A';
  const describe = (r: ProviderRecord) => `${r.type} ${r.value} · ${r.line} · ${r.enabled ? '启用' : '暂停'} · ID ${r.id}`;
  const chooser = <Modal open={!!choice} title="选择要绑定的记录" okText="确认" okButtonProps={{ disabled: !picked }} onOk={confirm} onCancel={() => setChoice(null)} destroyOnHidden>
    <Typography.Paragraph>{choice?.message}</Typography.Paragraph>
    <Radio.Group value={picked} onChange={e => setPicked(e.target.value)} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      {choice?.candidates.map(r => <Radio key={r.id} value={`adopt:${r.id}`}>接管 {describe(r)}</Radio>)}
      {choice?.conflicts.map(r => <Radio key={r.id} value={`convert:${r.id}`}>把这条 {describe(r)} 改成 {kind} 并接管（改后暂停，原值不保留）</Radio>)}
      {!!choice?.candidates.length && <Radio value="create">在默认线路新建一条暂停的 {kind} 记录</Radio>}
    </Radio.Group>
  </Modal>;
  return { bind: (values: RecordTarget) => run(() => attempt(values, {})), chooser };
}
