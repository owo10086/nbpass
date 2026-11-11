import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Form, Input, Modal, Popconfirm, Select, Space, Switch, Table, Tag } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { ManagedDNS } from './ManagedDNS';
import { AllPublishDNS, MemberRecords, type AllMember } from './AllPublishDNS';
import { GroupSyncDetails, SyncTag, type GroupSyncMember } from './ConfigSync';
import { EntryMembers } from './EntryMembers';
import { EntryReadiness, type EntryMemberState } from './EntryReadiness';
import { PageHeader } from './PageHeader';
import { useFocusRow } from './route';
import { byText, useDragOrder } from './ListOrder';

type EntryGroup = { observation_enabled: boolean; publication_mode: string; member_records?: AllMember[]; id: string; name: string; members: string[]; publication_status: string; sync_status: string; sync_members: GroupSyncMember[]; member_states: EntryMemberState[] };
type Member = { id: string; name: string };

export function EntryGroups({ focus, onSessionExpired }: { focus?: string; onSessionExpired: () => void }) {
  const [rows, setRows] = useState<EntryGroup[]>([]);
  const [nodes, setNodes] = useState<Member[]>([]);
  const [editing, setEditing] = useState<EntryGroup | null>();
  const [managingDNS, setManagingDNS] = useState<EntryGroup | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();
  const mode = Form.useWatch('publication_mode', form);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else setError((e as Error).message);
  }
  async function load() {
    setLoading(true);
    try {
      const [groups, members] = await Promise.all([api<{ entry_groups: EntryGroup[] }>('/entry-groups'), api<{ nodes: Member[] }>('/nodes')]);
      setRows(groups.entry_groups); setNodes(members.nodes); setError(''); setLoaded(true);
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }
  const order = useDragOrder(rows, setRows, '/entry-groups', { onError: failed, reload: load });
  useEffect(() => { void load(); const timer = window.setInterval(() => { if (!order.busy()) void load(); }, 10_000); return () => window.clearInterval(timer); }, []);
  const rowClassName = useFocusRow(focus, loaded);
  function edit(row: EntryGroup | null) {
    setError(''); form.resetFields(); form.setFieldsValue(row ? { name: row.name, members: row.members, publication_mode: row.publication_mode } : { members: [undefined], publication_mode: 'primary_backup' }); setEditing(row);
  }
  async function save(values: { name: string; members: string[]; publication_mode: string }) {
    setSaving(true);
    try {
      await api(editing ? `/entry-groups/${editing.id}` : '/entry-groups', editing ? 'PUT' : 'POST', values);
      setEditing(undefined); form.resetFields(); await load();
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }
  async function remove(id: string) {
    try { await api(`/entry-groups/${id}`, 'DELETE'); await load(); } catch (e) { failed(e); }
  }
  return <>
    <PageHeader title="入口组" description="将转发规则应用到入口节点。单成员入口组无需绑定 DNS 即可使用。展开一行查看成员就绪与同步详情。">
      <Button icon={<ReloadOutlined spin={loading} />} onClick={() => void load()} aria-label="刷新" />
      <Button type="primary" icon={<PlusOutlined />} onClick={() => edit(null)}>创建入口组</Button>
    </PageHeader>
    {(error || order.error) && <Alert title={error || order.error} type="error" showIcon className="message" />}
    {order.restore && <div className="toolbar"><span className="spacer" />{order.restore}</div>}
    <div className="panel"><Table<EntryGroup> rowKey="id" dataSource={rows} loading={!loaded && !error} pagination={false} onChange={order.onChange} onRow={order.onRow} rowClassName={row => `${rowClassName(row.id)} ${order.rowClass(row.id)}`} scroll={{ x: 836 }} locale={{ emptyText: '暂无入口组，请先添加节点，再创建入口组。' }} expandable={{ defaultExpandedRowKeys: focus ? [focus] : [], expandedRowRender: row => <Space orientation="vertical" size="middle" style={{ width: '100%' }}><EntryReadiness members={row.member_states} />{row.publication_mode === 'all' && <MemberRecords members={row.member_records ?? []} nodes={nodes} />}<ObservationDetails group={row.id} onFailure={failed} /><GroupSyncDetails members={row.sync_members} /></Space> }} columns={[
      order.handle,
      { title: '入口组', dataIndex: 'name', ...order.sorted('name'), sorter: byText(row => row.name), render: value => <span className="node-name">{value}</span> },
      { title: '观测模式', render: (_, row) => <Space>{row.observation_enabled && <Tag color="orange">观测中</Tag>}<Switch checked={row.observation_enabled} checkedChildren="开启" unCheckedChildren="关闭" aria-label={`${row.name}观测模式`} onChange={enabled => { void api(`/entry-groups/${row.id}/observation`, 'PUT', { enabled }).then(load).catch(failed); }} /></Space> },
      { title: '发布模式', render: (_, row) => row.publication_mode === 'all' ? '全部发布' : '主备' },
      { title: '成员', render: (_, row) => row.members.map((id, i) => `${nodes.find(n => n.id === id)?.name ?? id}${i === 0 && row.publication_mode !== 'all' ? '（首选入口）' : ''}`).join('、') },
      { title: '受管记录', render: (_, row) => <Tag>{row.publication_status}</Tag> },
      { title: '同步状态', render: (_, row) => <SyncTag status={row.sync_status} /> },
      { title: '', key: 'actions', align: 'right', render: (_, row) => <div className="row-actions"><Button size="small" type="text" onClick={() => edit(row)}>编辑</Button><Button size="small" type="text" onClick={() => setManagingDNS(row)}>受管记录与发布</Button><Popconfirm title="删除入口组？" description="须先删除该入口组的转发规则。解绑后的旧 DNSPod 记录保持原状，请自行核对处理。" onConfirm={() => remove(row.id)}><Button danger size="small" type="text">删除</Button></Popconfirm></div> },
    ]} /></div>
    {managingDNS?.publication_mode === 'all' && <AllPublishDNS group={managingDNS} nodes={nodes} onClose={() => { setManagingDNS(null); void load(); }} onSessionExpired={onSessionExpired} />}
    {managingDNS && managingDNS.publication_mode !== 'all' && <ManagedDNS key={managingDNS.id} group={managingDNS} nodes={nodes} onClose={() => { setManagingDNS(null); void load(); }} onSessionExpired={onSessionExpired} />}
    <Modal title={editing ? '编辑入口组' : '创建入口组'} open={editing !== undefined} onCancel={() => { setEditing(undefined); form.resetFields(); }} footer={null} forceRender>
      <Form form={form} layout="vertical" onFinish={save} clearOnDestroy>
        {error && <Alert title={error} type="error" showIcon className="message" />}
        <Form.Item name="name" label="入口组名称" rules={[{ required: true, whitespace: true, message: '请输入入口组名称' }]}><Input maxLength={200} autoFocus /></Form.Item>
        <Form.Item name="publication_mode" label="发布模式" extra="创建后不能更改；换模式须新建组再修改规则入口。" rules={[{ required: true }]}><Select disabled={!!editing} options={[{ value: 'primary_backup', label: '主备' }, { value: 'all', label: '全部发布' }]} /></Form.Item>
        <EntryMembers nodes={nodes} all={mode === 'all'} />
        <Button type="primary" htmlType="submit" block loading={saving}>保存入口组</Button>
      </Form>
    </Modal>
  </>;
}


type ObservationAction = { id: string; node_id: string; kind: string; message: string; created_at: string };

function ObservationDetails({ group, onFailure }: { group: string; onFailure: (e: unknown) => void }) {
  const [actions, setActions] = useState<ObservationAction[]>([]);
  const failed = useRef(onFailure); failed.current = onFailure;
  useEffect(() => {
    let active = true;
    async function load() {
      try {
        const view = await api<{ actions: ObservationAction[] }>(`/entry-groups/${group}/observation`);
        if (active) setActions(view.actions);
      } catch (e) { if (active) failed.current(e); }
    }
    void load();
    const timer = window.setInterval(() => void load(), 10_000);
    return () => { active = false; window.clearInterval(timer); };
  }, [group]);
  return <Space orientation="vertical" style={{ width: '100%' }}>
    <Alert type="info" showIcon title="本来会做的动作" description="开启观测后，自动动作只记录并通知。手动操作照常执行；已下发的换 IP 继续核对和同步地址。关闭后按当前状态重新判断。" />
    <Table<ObservationAction> size="small" rowKey="id" dataSource={actions} pagination={{ pageSize: 5, hideOnSinglePage: true }} locale={{ emptyText: '暂无观测动作' }} columns={[
      { title: '时间', dataIndex: 'created_at', render: value => new Date(value).toLocaleString() },
      { title: '本来会做什么', dataIndex: 'message' },
    ]} />
  </Space>;
}
