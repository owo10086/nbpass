import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Checkbox, Form, Input, InputNumber, Modal, Popconfirm, Space, Table, Tag, Typography } from 'antd';
import { api, APIError } from './api';
import { DNSRecordPicker, useRecordBinding } from './DNSRecordPicker';
import { EntryDomainWatch } from './EntryDomainWatch';
import type { EntryMemberState } from './EntryReadiness';

type Node = { id: string; name: string };
type RecordBinding = { id: string; account_id: string; domain: string; record_id: string; line_id: string; hostname: string; current_value: string; current_enabled: boolean; warnings: string[] };
export type AllMember = { node_id: string; record: RecordBinding | null; expected_value: string; expected_enabled: boolean; address_pending: boolean; reason: string; withdrawn_at: string | null; last_error: string; manual_locked: boolean; removal_action_id: string; propagation_status: string };
type ActionRecord = { managed_record_id: string; record_id: string; new_value: string; new_enabled: boolean; stage: string; last_error: string; propagation_status: string; propagation_note: string };
type Publication = { first_published: boolean; desired_version: number; minimum_kept: number; effective_minimum_kept: number; members: AllMember[]; member_states: EntryMemberState[]; action: { id: string; stage: string; records: ActionRecord[] } | null };
type Account = { id: string; name: string };

export function MemberRecords({ members, nodes }: { members: AllMember[]; nodes: Node[] }) {
  return <Table<AllMember> size="small" rowKey="node_id" dataSource={members} pagination={false} scroll={{ x: 650 }} columns={[
    { title: '成员', render: (_, m) => nodes.find(n => n.id === m.node_id)?.name ?? m.node_id },
    { title: '受管记录', render: (_, m) => m.record ? `${m.record.hostname} / ${m.record.record_id}` : '未绑定' },
    { title: '服务商最近确认', render: (_, m) => m.record ? <>{m.record.current_value} <Tag>{m.record.current_enabled ? '启用' : '暂停'}</Tag></> : '—' },
    { title: '成员状态', render: (_, m) => <>{m.reason}{m.manual_locked && ' · 手动锁定'}{m.last_error && <Alert type="warning" title={m.last_error} />}</> },
    { title: '摘除时间', render: (_, m) => m.withdrawn_at ? new Date(m.withdrawn_at).toLocaleString() : '—' },
    { title: '地址传播', render: (_, m) => m.propagation_status || '尚未发布' },
  ]} />;
}

export function AllPublishDNS({ group, nodes, onClose, onSessionExpired }: { group: { id: string; name: string; members: string[] }; nodes: Node[]; onClose: () => void; onSessionExpired: () => void }) {
  const [publication, setPublication] = useState<Publication | null>(null);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [bindingMember, setBindingMember] = useState<string | null>(null);
  const [addingAccount, setAddingAccount] = useState(false);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [minimumDraft, setMinimumDraft] = useState<number | null>(null);
  const [bindingForm] = Form.useForm();
  const [accountForm] = Form.useForm();
  const [publishForm] = Form.useForm();
  const active = useRef(true);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  const path = `/entry-groups/${group.id}`;
  function failed(e: unknown) {
    if (!active.current) return;
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else setError((e as Error).message);
  }
  async function load() {
    try {
      const [p, a] = await Promise.all([api<Publication>(`${path}/publication`), api<{ accounts: Account[] }>('/dns-accounts')]);
      if (active.current) { setPublication(p); setAccounts(a.accounts); }
    } catch (e) { failed(e); }
  }
  useEffect(() => { active.current = true; void load(); const timer = window.setInterval(() => void load(), 2000); return () => { active.current = false; window.clearInterval(timer); }; }, []);
  async function run(action: () => Promise<unknown>) {
    setSaving(true); setError('');
    try { await action(); await load(); } catch (e) { failed(e); } finally { if (active.current) setSaving(false); }
  }
  const binder = useRecordBinding(async body => { const result = await api(`${path}/members/${bindingMember}/managed-record`, 'PUT', body); setBindingMember(null); return result; }, run);
  const members = publication?.members ?? [];
  const pending = publication?.action && ['期望已变更', '结果未确认'].includes(publication.action.stage);
  const name = (id: string) => nodes.find(n => n.id === id)?.name ?? id;
  return <Modal open title={`${group.name} · 全部发布`} onCancel={onClose} footer={null} width={1000}>
    <Space orientation="vertical" style={{ width: '100%' }} size="middle">
      {error && <Alert type="error" title={error} showIcon />}
      <Alert type="info" showIcon title="每个成员各绑定一条同名、同线路的 A 记录。" description="填同一个主机记录名：没有空闲的同名记录时面板新建一条暂停的记录，首次发布或加回时启用。首次发布前不自动改 DNS；首次发布后新增成员会先暂停，等待加回。面板不删除 DNSPod 记录。" />
      <MemberRecords members={members} nodes={nodes} />
      {publication && <Card size="small" title="自动摘除与加回">
        <Space wrap>
          <span>最少保留数</span>
          <InputNumber aria-label="最少保留数" min={1} precision={0} value={minimumDraft ?? publication.minimum_kept} onChange={setMinimumDraft} />
          <Button disabled={saving || !!pending} onClick={() => run(async () => { await api(`${path}/minimum-kept`, 'PUT', { minimum_kept: minimumDraft ?? publication.minimum_kept }); setMinimumDraft(null); })}>保存</Button>
          <span>当前按 {publication.effective_minimum_kept} 条记录保留</span>
        </Space>
        <Typography.Paragraph type="secondary">自动摘除优先保留仍启用的成员，再按组内顺序选择；不会为凑数加回暂停成员。恢复须连续六次十秒检查成功且地址一致。手动摘除会锁定成员，手动加回后恢复自动规则。</Typography.Paragraph>
      </Card>}
      <Space wrap>
        <Button onClick={() => setAddingAccount(true)}>添加 DNSPod 账号</Button>
        {members.map(m => <Space key={m.node_id}>
          {!m.record && <Button disabled={!!pending} onClick={() => { bindingForm.resetFields(); setBindingMember(m.node_id); }}>绑定 {name(m.node_id)} 的记录</Button>}
          {m.record && !pending && <Button onClick={() => { bindingForm.setFieldsValue(m.record); setBindingMember(m.node_id); }}>更换 {name(m.node_id)} 的记录</Button>}
          {m.record && publication?.first_published && <>
            <Popconfirm title={`手动摘除 ${name(m.node_id)}？`} description="暂停记录并锁定，之后不会自动加回。不能摘除最后一条启用记录。" onConfirm={() => run(() => api(`${path}/members/${m.node_id}/withdraw`, 'POST', { expected_version: publication.desired_version }))}><Button disabled={saving || !!pending || (m.manual_locked && !m.record.current_enabled && !m.last_error)}>摘除 {name(m.node_id)}</Button></Popconfirm>
            <Popconfirm title={`手动加回 ${name(m.node_id)}？`} description={publication.member_states.find(s => s.node_id === m.node_id)?.candidate ? '立即启用记录并解除手动锁，恢复自动规则。' : '警示：该成员当前异常或健康条件尚未确认。仍继续将立即启用并解锁，之后可能被再次自动摘除；请先核对成员状态及记录地址。'} onConfirm={() => run(() => api(`${path}/members/${m.node_id}/restore`, 'POST', { expected_version: publication.desired_version }))}><Button disabled={saving || !!pending || (m.record.current_enabled && !m.manual_locked && !m.last_error)}>加回 {name(m.node_id)}</Button></Popconfirm>
          </>}
          {m.record && <Popconfirm title={`移出 ${name(m.node_id)}？`} description="先暂停记录，服务商确认后解绑并移出。唯一启用记录不能移出。" onConfirm={() => run(() => api(`${path}/members/${m.node_id}`, 'DELETE'))}><Button danger disabled={saving || !!pending}>移出 {name(m.node_id)}</Button></Popconfirm>}
        </Space>)}
      </Space>
      {members.flatMap(m => m.record?.warnings ?? []).map((warning, i) => <Alert key={i} type="warning" title={warning} showIcon />)}
      {publication && !publication.first_published && <Card title="首次发布">
        <Form form={publishForm} layout="vertical" onFinish={values => run(() => api(`${path}/publish`, 'POST', { confirm_inbound: values.confirm_inbound, member_ipv4: Object.fromEntries(Object.entries(values.member_ipv4 ?? {}).filter(([, value]) => value)) }))}>
          <Typography.Paragraph>核对每个成员可直接入站的公网 IPv4。留空使用面板确认地址；没有有效地址的成员保留服务商原值并暂停。至少一个成员须在线、就绪、未被墙且不在维护中。</Typography.Paragraph>
          {members.map(m => <Form.Item key={m.node_id} name={['member_ipv4', m.node_id]} label={`${name(m.node_id)} · 确认 IPv4`}><Input maxLength={15} placeholder={m.expected_value || '无有效地址可留空'} /></Form.Item>)}
          <Form.Item name="confirm_inbound" valuePropName="checked" rules={[{ validator: (_, value) => value ? Promise.resolve() : Promise.reject(new Error('请确认成员地址与入站能力')) }]}><Checkbox>我已核对各成员地址及直接入站能力，确认写入并启用健康成员、暂停其余成员。</Checkbox></Form.Item>
          <Button type="primary" htmlType="submit" loading={saving} disabled={!!pending || members.some(m => !m.record)}>确认并首次发布</Button>
        </Form>
      </Card>}
      {publication?.action && <Card title={`记录状态 · ${publication.action.stage}`} extra={`期望版本 ${publication.desired_version}`}>
        <Table<ActionRecord> size="small" rowKey="managed_record_id" dataSource={publication.action.records} pagination={false} columns={[
          { title: '记录 ID', dataIndex: 'record_id' },
          { title: '目标', render: (_, r) => `${r.new_value || '保留原地址'} · ${r.new_enabled ? '启用' : '暂停'}` },
          { title: '服务商确认', render: (_, r) => <>{r.stage}{r.last_error && <Alert type="warning" title={r.last_error} />}</> },
          { title: '地址传播', render: (_, r) => <>{r.propagation_status || '等待服务商确认'}{r.propagation_note && <div>{r.propagation_note}</div>}</> },
        ]} />
      </Card>}
      <EntryDomainWatch all groupID={group.id} nodes={nodes} onSessionExpired={onSessionExpired} />
    </Space>
    <Modal open={!!bindingMember} title={`绑定 ${bindingMember ? name(bindingMember) : ''} 的记录`} footer={null} onCancel={() => setBindingMember(null)}>
      <Form form={bindingForm} layout="vertical" onFinish={binder.bind}>
        <DNSRecordPicker accounts={accounts} onError={failed} />
        <Button htmlType="submit" type="primary" loading={saving}>绑定记录</Button>
      </Form>
    </Modal>
    {binder.chooser}
    <Modal open={addingAccount} title="添加 DNSPod 账号" footer={null} onCancel={() => { setAddingAccount(false); accountForm.resetFields(); }}>
      <Form form={accountForm} layout="vertical" onFinish={values => run(async () => { await api('/dns-accounts', 'POST', values); setAddingAccount(false); accountForm.resetFields(); })}>
        <Typography.Paragraph type="secondary">使用腾讯云 API 密钥（访问管理 → API 密钥管理），建议用只授予 DNSPod 权限的子账号。凭据加密保存在面板，保存后不回显。</Typography.Paragraph>
        <Form.Item name="name" label="账号名称" rules={[{ required: true }]}><Input maxLength={200} /></Form.Item>
        <Form.Item name="secret_id" label="SecretId" rules={[{ required: true, whitespace: true, message: '请输入 SecretId' }]}><Input autoComplete="off" maxLength={128} placeholder="AKID…" /></Form.Item>
        <Form.Item name="secret_key" label="SecretKey" rules={[{ required: true, whitespace: true, message: '请输入 SecretKey' }]}><Input.Password autoComplete="new-password" maxLength={128} /></Form.Item>
        <Button htmlType="submit" type="primary" loading={saving}>加密保存账号</Button>
      </Form>
    </Modal>
  </Modal>;
}
