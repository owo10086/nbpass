import { useEffect, useRef, useState } from 'react';
import { Alert, App, Button, Card, Descriptions, Form, Input, Modal, Popconfirm, Space, Typography } from 'antd';
import { api, APIError } from './api';
import { DNSRecordPicker, useRecordBinding } from './DNSRecordPicker';

type Account = { id: string; name: string };
export type NodeRecord = { account_id: string; domain: string; record_id: string; line_id: string; line: string; hostname: string; current_value: string; current_enabled: boolean; warnings: string[] };
type Publication = { desired_version: number; expected_value: string; reported_ipv4: string; address_mismatch: boolean; published: boolean; written_at: string | null; action: { stage: string; last_error: string } | null };
type Node = { id: string; name: string };

export function NodeManagedRecord({ node, onClose, onSaved, onError }: { node: Node; onClose: () => void; onSaved: () => Promise<void>; onError: (e: unknown) => void }) {
  const { modal } = App.useApp();
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [record, setRecord] = useState<NodeRecord | null>(null);
  const [publication, setPublication] = useState<Publication>();
  const [error, setError] = useState('');
  const [warnings, setWarnings] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [addingAccount, setAddingAccount] = useState(false);
  const [form] = Form.useForm();
  const [accountForm] = Form.useForm();
  const dirty = useRef(false);
  const active = useRef(true);
  const path = `/nodes/${node.id}`;
  function failed(e: unknown) {
    if (!active.current) return;
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) onError(e);
    else setError((e as Error).message);
  }
  async function load() {
    const [list, binding, state] = await Promise.all([
      api<{ accounts: Account[] }>('/dns-accounts'),
      api<NodeRecord>(`${path}/managed-record`).catch(e => { if (e instanceof APIError && e.status === 404) return null; throw e; }),
      api<Publication>(`${path}/publication`),
    ]);
    if (!active.current) return;
    setAccounts(list.accounts); setRecord(binding); setPublication(state); setLoaded(true);
    if (!dirty.current && binding) form.setFieldsValue(binding);
  }
  useEffect(() => {
    active.current = true;
    void load().catch(failed);
    const timer = window.setInterval(() => void load().catch(failed), 2000);
    return () => { active.current = false; window.clearInterval(timer); };
  }, []);
  async function operate(action: () => Promise<unknown>) {
    setBusy(true); setError('');
    try { await action(); await load(); await onSaved(); }
    catch (e) { failed(e); }
    finally { if (active.current) setBusy(false); }
  }
  function sync() {
    if (!publication?.reported_ipv4) return;
    const ipv4 = publication.reported_ipv4;
    modal.confirm({ title: `确认同步地址 ${ipv4}？`, content: '请确认这是该节点可直接接收业务连接的公网 IPv4。同步会覆盖节点受管记录及该节点在各全部发布组的成员记录，各自保持启用或暂停状态。', okText: '确认并同步', onOk: () => operate(() => api(`${path}/sync-address`, 'POST', { ipv4, confirm_inbound: true })) });
  }
  function publish() {
    modal.confirm({ title: '确认首次发布节点域名？', content: `请确认 ${publication?.expected_value} 可接收入站业务连接。面板会先写地址，再明确启用这条记录。`, okText: '确认并首次发布', onOk: () => operate(() => api(`${path}/publish`, 'POST', { expected_version: publication?.desired_version, confirm_inbound: true })) });
  }
  const binder = useRecordBinding(async body => { const result = await api<NodeRecord>(`${path}/managed-record`, 'PUT', body); setWarnings([]); dirty.current = false; return result; }, operate);
  async function unbind() {
    await operate(async () => { const result = await api<{ warnings: string[] }>(`${path}/managed-record`, 'DELETE'); setWarnings(result.warnings); form.resetFields(); dirty.current = false; });
  }
  async function addAccount(values: { name: string; secret_id: string; secret_key: string }) {
    await operate(async () => { const account = await api<Account>('/dns-accounts', 'POST', values); setAddingAccount(false); accountForm.resetFields(); dirty.current = true; form.setFieldsValue({ account_id: account.id }); });
  }
  const pending = publication?.action?.stage === '期望已变更' || publication?.action?.stage === '结果未确认';
  return <Modal title={`${node.name} · 域名与地址`} open onCancel={onClose} footer={null} width={720}>
    <Space orientation="vertical" style={{ width: '100%' }} size="middle">
      {error && <Alert type="error" showIcon title={error} />}
      {warnings.map((warning, i) => <Alert key={i} type="warning" showIcon title={warning} />)}
      <Card title="节点地址" loading={!loaded}>
        <Descriptions column={1} size="small" items={[
          { key: 'reported', label: '上报地址', children: publication?.reported_ipv4 || '尚未查询到' },
          { key: 'confirmed', label: '确认地址 / 记录期望', children: publication?.expected_value || '地址待确认' },
        ]} />
        {publication?.address_mismatch && <Alert type="warning" showIcon title="上报地址与确认地址不一致，面板只通知，受管记录保持原状。" style={{ marginTop: 12 }} />}
        <Button style={{ marginTop: 12 }} loading={busy} disabled={!publication?.reported_ipv4 || pending} onClick={sync}>同步地址</Button>
      </Card>
      <Card title="节点受管记录" loading={!loaded} extra={<Button onClick={() => setAddingAccount(true)}>添加 DNSPod 账号</Button>}>
        {record && <>
          <Descriptions column={1} size="small" items={[
            { key: 'domain', label: '绑定域名', children: record.hostname },
            { key: 'record', label: '记录 / 线路', children: `${record.record_id} · ${record.line}（${record.line_id}）` },
            { key: 'status', label: '服务商最近确认', children: `${record.current_value} · ${record.current_enabled ? '启用' : '暂停'}` },
            { key: 'publication', label: '首次发布', children: publication?.published ? '已完成' : '尚未完成' },
            { key: 'action', label: '最新动作', children: publication?.action?.stage || '尚无动作' },
          ]} />
          {publication?.expected_value && record.current_value !== publication.expected_value && <Alert type="warning" showIcon title="记录地址与期望地址不一致，等待同步完成。" />}
          {publication?.action?.last_error && <Alert type="warning" showIcon title={publication.action.last_error} />}
          {record.warnings.map((warning, i) => <Alert key={i} type="warning" showIcon title={warning} style={{ marginTop: 8 }} />)}
          <Space style={{ margin: '12px 0' }}>
            {!publication?.published && <Button type="primary" disabled={!publication?.expected_value || pending} loading={busy} onClick={publish}>首次发布</Button>}
            <Popconfirm title="解绑后旧 DNSPod 记录保持原状，请自行核对和处理。" onConfirm={unbind}><Button danger disabled={pending} loading={busy}>解绑记录</Button></Popconfirm>
          </Space>
        </>}
        <Typography.Paragraph type="secondary">更换或解绑后，旧记录保持原状。受管域名自动加入解析监控，以确认地址为期望。</Typography.Paragraph>
        <Form form={form} layout="vertical" onValuesChange={() => { dirty.current = true; }} onFinish={binder.bind} disabled={busy || pending}>
          <DNSRecordPicker accounts={accounts} onError={failed} />
          <Button htmlType="submit" loading={busy} disabled={!loaded || pending}>{record ? '更换绑定' : '绑定记录'}</Button>
        </Form>
        {binder.chooser}
      </Card>
    </Space>
    <Modal title="添加 DNSPod 账号" open={addingAccount} onCancel={() => { setAddingAccount(false); accountForm.resetFields(); }} footer={null} destroyOnHidden>
      <Form form={accountForm} layout="vertical" onFinish={addAccount} clearOnDestroy>
        <Typography.Paragraph type="secondary">使用腾讯云 API 密钥（访问管理 → API 密钥管理），建议用只授予 DNSPod 权限的子账号。凭据加密保存在面板，保存后不回显。</Typography.Paragraph>
        <Form.Item name="name" label="账号名称" rules={[{ required: true, whitespace: true, message: '请输入账号名称' }]}><Input maxLength={200} /></Form.Item>
        <Form.Item name="secret_id" label="SecretId" rules={[{ required: true, whitespace: true, message: '请输入 SecretId' }]}><Input autoComplete="off" maxLength={128} placeholder="AKID…" /></Form.Item>
        <Form.Item name="secret_key" label="SecretKey" rules={[{ required: true, whitespace: true, message: '请输入 SecretKey' }]}><Input.Password autoComplete="new-password" maxLength={128} /></Form.Item>
        <Button type="primary" htmlType="submit" loading={busy}>加密保存账号</Button>
      </Form>
    </Modal>
  </Modal>;
}
