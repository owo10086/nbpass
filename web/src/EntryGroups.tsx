import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd';
import { api, APIError } from './api';
import { ManagedDNS } from './ManagedDNS';
import { GroupSyncDetails, SyncTag, type GroupSyncMember } from './ConfigSync';

type EntryGroup = { id: string; name: string; members: string[]; publication_status: string; sync_status: string; sync_members: GroupSyncMember[] };
type Member = { id: string; name: string };

export function EntryGroups({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [rows, setRows] = useState<EntryGroup[]>([]);
  const [nodes, setNodes] = useState<Member[]>([]);
  const [editing, setEditing] = useState<EntryGroup | null>();
  const [managingDNS, setManagingDNS] = useState<EntryGroup | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else setError((e as Error).message);
  }
  async function load() {
    setLoading(true);
    try {
      const [groups, members] = await Promise.all([api<{ entry_groups: EntryGroup[] }>('/entry-groups'), api<{ nodes: Member[] }>('/nodes')]);
      setRows(groups.entry_groups); setNodes(members.nodes); setError('');
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }
  useEffect(() => { void load(); const timer = window.setInterval(() => void load(), 10_000); return () => window.clearInterval(timer); }, []);
  function edit(row: EntryGroup | null) {
    setError(''); form.resetFields(); form.setFieldsValue(row ? { name: row.name, member: row.members[0] } : {}); setEditing(row);
  }
  async function save(values: { name: string; member: string }) {
    setSaving(true);
    try {
      await api(editing ? `/entry-groups/${editing.id}` : '/entry-groups', editing ? 'PUT' : 'POST', { name: values.name, members: [values.member] });
      setEditing(undefined); form.resetFields(); await load();
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }
  async function remove(id: string) {
    try { await api(`/entry-groups/${id}`, 'DELETE'); await load(); } catch (e) { failed(e); }
  }
  return <>
    <div className="nodes-heading"><div><Typography.Title level={2}>入口组</Typography.Title><Typography.Paragraph type="secondary">将转发规则应用到入口节点。单成员入口组无需绑定 DNS 即可使用。</Typography.Paragraph></div><Space><Button onClick={() => void load()}>刷新</Button><Button type="primary" onClick={() => edit(null)}>创建入口组</Button></Space></div>
    {error && <Alert title={error} type="error" showIcon className="message" />}
    <Card styles={{ body: { padding: 0 } }}><Table<EntryGroup> rowKey="id" dataSource={rows} loading={loading} scroll={{ x: 800 }} locale={{ emptyText: '暂无入口组，请先添加节点，再创建入口组。' }} expandable={{ expandedRowRender: row => <GroupSyncDetails members={row.sync_members} /> }} columns={[
      { title: '入口组', dataIndex: 'name' },
      { title: '成员（首选入口）', render: (_, row) => row.members.map(id => nodes.find(n => n.id === id)?.name ?? id).join('、') },
      { title: '受管记录', render: (_, row) => <Tag>{row.publication_status}</Tag> },
      { title: '同步状态', render: (_, row) => <SyncTag status={row.sync_status} /> },
      { title: '操作', render: (_, row) => <Space><Button size="small" onClick={() => edit(row)}>编辑</Button><Button size="small" onClick={() => setManagingDNS(row)}>受管记录与发布</Button><Popconfirm title="删除入口组？" description="须先删除该入口组的转发规则。" onConfirm={() => remove(row.id)}><Button danger size="small">删除</Button></Popconfirm></Space> },
    ]} /></Card>
    {managingDNS && <ManagedDNS key={managingDNS.id} group={managingDNS} nodes={nodes} onClose={() => { setManagingDNS(null); void load(); }} onSessionExpired={onSessionExpired} />}
    <Modal title={editing ? '编辑入口组' : '创建入口组'} open={editing !== undefined} onCancel={() => { setEditing(undefined); form.resetFields(); }} footer={null} forceRender>
      <Form form={form} layout="vertical" onFinish={save} clearOnDestroy>
        {error && <Alert title={error} type="error" showIcon className="message" />}
        <Form.Item name="name" label="入口组名称" rules={[{ required: true, whitespace: true, message: '请输入入口组名称' }]}><Input maxLength={200} autoFocus /></Form.Item>
        <Form.Item name="member" label="入口节点" rules={[{ required: true, message: '请选择一个入口节点' }]}><Select showSearch optionFilterProp="label" options={nodes.map(n => ({ value: n.id, label: n.name }))} placeholder="选择一个节点" /></Form.Item>
        <Typography.Paragraph type="secondary">受管记录为“未发布”，可直接通过节点 IPv4 和监听端口访问。</Typography.Paragraph>
        <Button type="primary" htmlType="submit" block loading={saving}>保存入口组</Button>
      </Form>
    </Modal>
  </>;
}
