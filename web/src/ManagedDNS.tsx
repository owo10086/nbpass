import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Checkbox, Descriptions, Form, Input, Modal, Select, Space, Tag, Typography } from 'antd';
import { api, APIError } from './api';
import { DNSRecordPicker, useRecordBinding } from './DNSRecordPicker';
import { EntryBlockedPolicy } from './EntryBlockedPolicy';
import { EntryMembers } from './EntryMembers';
import { EntryReadiness, type EntryMemberState } from './EntryReadiness';
import { EntrySwitch, type EntrySwitchState } from './EntrySwitch';
import { EntryDomainWatch } from './EntryDomainWatch';

type Account = { id: string; name: string; configured: boolean };
type Binding = { account_id: string; domain: string; record_id: string; line_id: string; line: string; hostname: string; current_value: string; current_ttl: number; warnings: string[] };
type Action = { id: string; stage: string; old_value: string; new_value: string; old_ttl: number; last_error: string };
type Publication = EntrySwitchState & { active_node_id: string; expected_value: string; written_at: string | null; action: Action | null };

export function ManagedDNS({ group, nodes, onClose, onSessionExpired }: {
  group: { id: string; name: string; members: string[] };
  nodes: { id: string; name: string }[];
  onClose: () => void;
  onSessionExpired: () => void;
}) {
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [binding, setBinding] = useState<Binding | null>(null);
  const [publication, setPublication] = useState<Publication | null>(null);
  const [members, setMembers] = useState(group.members);
  const [memberStates, setMemberStates] = useState<EntryMemberState[]>([]);
  const [groupName, setGroupName] = useState(group.name);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [addingAccount, setAddingAccount] = useState(false);
  const [loading, setLoading] = useState(true);
  const [accountForm] = Form.useForm();
  const [bindingForm] = Form.useForm();
  const [publishForm] = Form.useForm();
  const [membersForm] = Form.useForm();
  const membersDirty = useRef(false);
  const bindingDirty = useRef(false);
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
      const [list, record, state, groups] = await Promise.all([
        api<{ accounts: Account[] }>('/dns-accounts'),
        api<Binding>(`${path}/managed-record`).catch(e => { if (e instanceof APIError && e.status === 404) return null; throw e; }),
        api<Publication>(`${path}/publication`),
        api<{ entry_groups: { id: string; name: string; members: string[]; member_states: EntryMemberState[] }[] }>('/entry-groups'),
      ]);
      if (!active.current) return;
      setAccounts(list.accounts); setBinding(record); setPublication(state);
      const current = groups.entry_groups.find(g => g.id === group.id);
      if (current) {
        setMembers(current.members); setGroupName(current.name); setMemberStates(current.member_states);
        if (!membersDirty.current) membersForm.setFieldsValue({ members: current.members });
      }
      if (record && !bindingDirty.current) bindingForm.setFieldsValue(record);
    } catch (e) { failed(e); }
    finally { if (active.current) setLoading(false); }
  }
  useEffect(() => {
    active.current = true; void load();
    const timer = window.setInterval(() => void load(), 10_000);
    return () => { window.clearInterval(timer); active.current = false; accountForm.resetFields(); publishForm.resetFields(); };
  }, []);
  useEffect(() => {
    if (!publication?.action) return;
    if (publication.action.stage === '服务商已写入') {
      void api<Binding>(`${path}/managed-record`).then(record => { if (active.current) setBinding(record); }).catch(failed);
      return;
    }
    const timer = window.setInterval(() => { void load(); }, 2000);
    return () => window.clearInterval(timer);
  }, [publication?.action?.id, publication?.action?.stage]);
  async function addAccount(values: { name: string; secret_id: string; secret_key: string }) {
    setSaving(true); setError('');
    try {
      const account = await api<Account>('/dns-accounts', 'POST', values);
      accountForm.resetFields(); setAddingAccount(false);
      await load(); bindingDirty.current = true; bindingForm.setFieldsValue({ account_id: account.id });
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }
  async function run(action: () => Promise<unknown>) {
    setSaving(true); setError('');
    try { await action(); await load(); }
    catch (e) { failed(e); }
    finally { setSaving(false); }
  }
  const binder = useRecordBinding(async body => { const result = await api(`${path}/managed-record`, 'PUT', body); bindingDirty.current = false; return result; }, run);
  async function publish(values: { active_node_id: string; ipv4: string; confirm_inbound: boolean }) {
    setSaving(true); setError('');
    try { await api(`${path}/publish`, 'POST', values); publishForm.resetFields(); await load(); }
    catch (e) { failed(e); }
    finally { setSaving(false); }
  }
  async function saveMembers(values: { members: string[] }) {
    setSaving(true); setError('');
    try {
      const saved = await api<{ members: string[] }>(path, 'PUT', { name: groupName, members: values.members });
      setMembers(saved.members); membersDirty.current = false;
      membersForm.resetFields(); membersForm.setFieldsValue({ members: saved.members }); await load();
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }
  const started = (publication?.desired_version ?? 0) > 0;
  return <Modal title={`${groupName} · 入口组详情`} open onCancel={onClose} footer={null} width={980}>
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Card title="成员就绪与自动切换候选"><EntryReadiness members={memberStates} /></Card>
      {error && <Alert title={error} type="error" showIcon />}
      <EntryBlockedPolicy path={path} onError={failed} />
      <Card title="成员与排序">
        <Form form={membersForm} initialValues={{ members: group.members }} layout="vertical" onValuesChange={() => { membersDirty.current = true; }} onFinish={saveMembers} disabled={saving}>
          <EntryMembers nodes={nodes} />
          <Button htmlType="submit" loading={saving} style={{ marginTop: 16 }}>保存成员排序</Button>
        </Form>
        <Typography.Paragraph style={{ marginTop: 12 }}>首选入口：{nodes.find(n => n.id === publication?.preferred_node_id)?.name ?? publication?.preferred_node_id ?? '加载中'}</Typography.Paragraph>
      </Card>
      <Alert type="info" showIcon title="绑定后先核对当前记录，再由你明确选择活动入口并首次发布。" description="只管理这一条单线路 A 记录，发布 TTL 为 60 秒。面板新建的记录先暂停，首次发布时写入地址并启用。" />
      <Card title="DNSPod 受管记录" loading={loading} extra={!started && <Button onClick={() => setAddingAccount(true)}>添加 DNSPod 账号</Button>}>
        {!started && <Form form={bindingForm} layout="vertical" onFinish={binder.bind} onValuesChange={() => { bindingDirty.current = true; }}>
          <DNSRecordPicker accounts={accounts} onError={failed} />
          <Button type="primary" htmlType="submit" loading={saving}>绑定记录</Button>
        </Form>}
        {binder.chooser}
        {binding && <>
          <Descriptions column={1} size="small" style={{ marginTop: 16 }} items={[
            { key: 'hostname', label: '域名', children: binding.hostname },
            { key: 'account', label: '账号', children: accounts.find(a => a.id === binding.account_id)?.name ?? binding.account_id },
            { key: 'record', label: '受管记录', children: `${binding.record_id} · A · ${binding.line}（${binding.line_id}）` },
            { key: 'value', label: '服务商最近确认值', children: `${binding.current_value} · TTL ${binding.current_ttl} 秒` },
          ]} />
          {binding.warnings.map((warning, i) => <Alert key={i} type="warning" showIcon title={warning} style={{ marginTop: 8 }} />)}
        </>}
      </Card>
      {binding && !started && <Card title="首次发布">
        <Form form={publishForm} layout="vertical" onFinish={publish}>
          <Form.Item name="active_node_id" label="活动入口" rules={[{ required: true, message: '请明确选择活动入口' }]}><Select placeholder="选择要发布的成员" options={members.map(id => ({ value: id, label: nodes.find(n => n.id === id)?.name ?? id }))} /></Form.Item>
          <Form.Item name="ipv4" label="发布 IPv4" rules={[{ required: true, message: '请输入该入口可入站的公网 IPv4' }]}><Input placeholder="例如 8.8.8.8" maxLength={15} /></Form.Item>
          <Form.Item name="confirm_inbound" valuePropName="checked" rules={[{ validator: (_, value) => value ? Promise.resolve() : Promise.reject(new Error('请确认活动入口与入站地址')) }]}><Checkbox>我确认该 IPv4 属于所选活动入口，可直接接收业务连接，并确认替换受管记录当前值。</Checkbox></Form.Item>
          <Button type="primary" htmlType="submit" loading={saving}>确认并首次发布</Button>
        </Form>
      </Card>}
      {started && publication && <EntrySwitch path={path} members={members} nodes={nodes} publication={publication} saving={saving} setSaving={setSaving} onStart={() => setError('')} onChanged={load} onError={failed} />}
      {started && <Card title="发布进度" extra={<Button onClick={() => void load()}>刷新</Button>}>
        <Descriptions column={1} size="small" items={[
          { key: 'version', label: '期望版本', children: publication?.desired_version },
          { key: 'node', label: '活动入口', children: nodes.find(n => n.id === publication?.active_node_id)?.name ?? publication?.active_node_id },
          { key: 'expected', label: '期望 IPv4', children: publication?.expected_value },
          { key: 'stage', label: '当前阶段', children: <Tag>{publication?.action?.stage}</Tag> },
          { key: 'change', label: '记录变化', children: `${publication?.action?.old_value} → ${publication?.action?.new_value}` },
          { key: 'written', label: '服务商写入确认', children: publication?.written_at ? new Date(publication.written_at).toLocaleString() : '待确认' },
          { key: 'id', label: '动作 ID', children: <Typography.Text copyable>{publication?.action?.id}</Typography.Text> },
        ]} />
        {publication?.action?.last_error && <Alert title={publication.action.last_error} type="warning" showIcon />}
        {publication?.action?.stage === '服务商已写入' && <Typography.Paragraph type="secondary">DNSPod 已确认记录；递归 DNS 缓存可能仍保留旧值。</Typography.Paragraph>}
      </Card>}
      <EntryDomainWatch groupID={group.id} nodes={nodes} onSessionExpired={onSessionExpired} />
    </Space>
    <Modal title="添加 DNSPod 账号" open={addingAccount} onCancel={() => { setAddingAccount(false); accountForm.resetFields(); }} footer={null} destroyOnHidden>
      <Form form={accountForm} layout="vertical" onFinish={addAccount} clearOnDestroy>
        <Typography.Paragraph type="secondary">使用腾讯云 API 密钥（访问管理 → API 密钥管理），建议用只授予 DNSPod 权限的子账号。凭据加密保存在面板，保存后不回显。</Typography.Paragraph>
        <Form.Item name="name" label="账号名称" rules={[{ required: true, whitespace: true, message: '请输入账号名称' }]}><Input maxLength={200} /></Form.Item>
        <Form.Item name="secret_id" label="SecretId" rules={[{ required: true, whitespace: true, message: '请输入 SecretId' }]}><Input autoComplete="off" maxLength={128} placeholder="AKID…" /></Form.Item>
        <Form.Item name="secret_key" label="SecretKey" rules={[{ required: true, whitespace: true, message: '请输入 SecretKey' }]}><Input.Password autoComplete="new-password" maxLength={128} /></Form.Item>
        <Button type="primary" htmlType="submit" loading={saving}>加密保存账号</Button>
      </Form>
    </Modal>
  </Modal>;
}
