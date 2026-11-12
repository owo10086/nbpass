import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Form, Input, Modal, Popconfirm, Space, Typography } from 'antd';
import { ClusterOutlined, NodeIndexOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { PageHeader } from './PageHeader';
import { FilterCard, FilterField } from './FilterCard';
import { TableCard } from './TableCard';
import { ColorTag } from './ColorTag';
import { FormSection } from './FormSection';
import { EndpointMembers } from './EndpointMembers';
import { byText, useDragOrder } from './ListOrder';
import { pageTitles, useFocusRow } from './route';
import { endpointMemberOptions, memberKey, type Endpoint, type EndpointGroup, type EndpointNode, type MemberAddress } from './EndpointData';
import './Endpoints.css';

export function ExitGroups({ focus, onSessionExpired }: { focus?: string; onSessionExpired: () => void }) {
  const [rows, setRows] = useState<EndpointGroup[]>([]);
  const [nodes, setNodes] = useState<EndpointNode[]>([]);
  const [exits, setExits] = useState<Endpoint[]>([]);
  const [query, setQuery] = useState('');
  const [editing, setEditing] = useState<EndpointGroup | null>();
  const [error, setError] = useState('');
  const [saveError, setSaveError] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [page, setPage] = useState(1);
  const [showAll, setShowAll] = useState(false);
  const [form] = Form.useForm<{ name: string; member_addresses: MemberAddress[] }>();
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  function failed(e: unknown, saving = false) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else (saving ? setSaveError : setError)((e as Error).message);
  }
  async function load() {
    setLoading(true);
    try {
      const [groups, members, configured] = await Promise.all([
        api<{ exit_groups: EndpointGroup[] }>('/exit-groups'), api<{ nodes: EndpointNode[] }>('/nodes'), api<{ exits: Endpoint[] }>('/tunnel-exits'),
      ]);
      setRows(groups.exit_groups); setNodes(members.nodes); setExits(configured.exits); setError('');
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }
  const order = useDragOrder(rows, setRows, '/exit-groups', { filtered: !!query.trim(), onError: failed, reload: load });
  const focused = useFocusRow(focus, !loading && !!rows.length);
  useEffect(() => { void load(); }, []);
  useEffect(() => {
    if (!focus) return;
    const index = rows.findIndex(row => row.id === focus);
    if (index >= 0) setPage(Math.floor(index / 10) + 1);
  }, [focus, rows]);
  function edit(row: EndpointGroup | null) {
    setSaveError(''); form.resetFields();
    form.setFieldsValue(row ? { name: row.name, member_addresses: row.member_addresses.map(member => ({ ...member })) } : { member_addresses: [] });
    setEditing(row);
  }
  function close() { setEditing(undefined); setSaveError(''); form.resetFields(); }
  async function save(values: { name: string; member_addresses: MemberAddress[] }) {
    setSaving(true); setSaveError('');
    try {
      await api(editing ? `/exit-groups/${editing.id}` : '/exit-groups', editing ? 'PUT' : 'POST', {
        name: values.name.trim(), member_addresses: values.member_addresses, strategy: 'primary_backup',
      });
      close(); await load();
    } catch (e) { failed(e, true); }
    finally { setSaving(false); }
  }
  async function remove(id: string) {
    try { await api(`/exit-groups/${id}`, 'DELETE'); await load(); } catch (e) { failed(e); }
  }
  const options = endpointMemberOptions(exits, nodes);
  const memberName = (member: MemberAddress) => options.find(option => option.id === memberKey(member))?.name ?? `${member.node_id} · ${member.address_id}`;
  const memberDescription = (member: MemberAddress) => { const option = options.find(option => option.id === memberKey(member)); return `${memberName(member)} ${option?.address ?? ''}`; };
  const visible = rows.filter(row => `${row.id} ${row.name} ${row.member_addresses.map(memberDescription).join(' ')}`.toLowerCase().includes(query.trim().toLowerCase()));
  return <div className="endpoint-page">
    <PageHeader title={pageTitles['endpoint-groups']} description="按主备顺序组织转发端点，供单跳转发端口使用。">
      <Button href="#/endpoints">管理转发端点</Button>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => edit(null)}>创建端点组</Button>
    </PageHeader>
    {(error || order.error) && <Alert title={error || order.error} type="error" showIcon className="message" />}
    <FilterCard onRefresh={() => void load()} loading={loading}>
      <FilterField label="搜索" htmlFor="endpoint-group-search"><Input id="endpoint-group-search" prefix={<SearchOutlined />} allowClear placeholder="名称、ID 或成员" value={query} onChange={event => { setQuery(event.target.value); setPage(1); }} /></FilterField>
    </FilterCard>
    <TableCard<EndpointGroup> title="端点组列表" actions={<Space wrap>{order.restore}{rows.length > 10 && <Button onClick={() => { setShowAll(!showAll); setPage(1); }}>{showAll ? '恢复分页' : '显示全部以排序'}</Button>}</Space>} rowKey="id" dataSource={visible} loading={loading} pagination={{ current: showAll ? 1 : page, pageSize: showAll ? Math.max(10, visible.length) : 10, showSizeChanger: false, onChange: setPage }} onChange={order.onChange} onRow={order.onRow} rowClassName={row => [order.rowClass(row.id), focused(row.id)].filter(Boolean).join(' ')} scroll={{ x: 900 }} locale={{ emptyText: query.trim() ? '没有匹配的端点组' : '暂无端点组，请先配置转发端点，再创建端点组。' }} columns={[
      { ...order.handle, width: 28 },
      { title: 'ID', dataIndex: 'id', fixed: 'left', width: 120, render: value => <Typography.Text className="endpoint-id mono" ellipsis={{ tooltip: value }} copyable={{ text: value }}>{value}</Typography.Text> },
      { title: '名称', dataIndex: 'name', fixed: 'left', width: 300, ...order.sorted('name'), sorter: byText(row => row.name), render: (_, row) => <><span className="node-name">{row.name}</span><span className="cell-sub endpoint-group-members" title={row.member_addresses.map((member, index) => `${index + 1}. ${memberDescription(member)}`).join(' → ')}>{row.member_addresses.map((member, index) => `${index + 1}. ${memberDescription(member)}`).join(' → ')}</span></> },
      { title: '成员数', width: 100, render: (_, row) => <ColorTag>{row.member_addresses.length}</ColorTag> },
      { title: '策略', width: 150, render: () => <ColorTag tone="orange">主备 · 按顺序</ColorTag> },
      { title: '操作', key: 'actions', fixed: 'right', width: 140, align: 'right', render: (_, row) => <div className="row-actions"><Button size="small" type="text" onClick={() => edit(row)}>编辑</Button><Popconfirm title="删除端点组？" description="须先删除引用该组的转发端口。" onConfirm={() => remove(row.id)}><Button danger size="small" type="text">删除</Button></Popconfirm></div> },
    ]} />
    <p className="footnote">可拖动列表手柄调整端点组的显示顺序；跨页排序时先选择“显示全部以排序”。组内第一项为主端点，其余按顺序备用；故障恢复后连续探测成功 60 秒才重新接新连接。</p>
    <Modal title={editing ? '编辑端点组' : '创建端点组'} open={editing !== undefined} onCancel={close} footer={null} width={1060} closable={!saving} maskClosable={!saving} keyboard={!saving} forceRender className="endpoint-modal">
      <Form form={form} layout="vertical" onFinish={save} disabled={saving} requiredMark={false}>
        {saveError && <Alert title={saveError} type="error" showIcon className="message" />}
        <FormSection title="基本信息" icon={<ClusterOutlined />}>
          <Form.Item name="name" label="端点组名称" rules={[{ required: true, whitespace: true, message: '请输入端点组名称' }]}><Input maxLength={200} placeholder="请输入端点组名称" /></Form.Item>
          <Form.Item label="转发策略" extra="第一项为主端点，其余按顺序备用；恢复后连续探测成功 60 秒再接新连接。"><ColorTag tone="orange">主备 · 按顺序</ColorTag></Form.Item>
        </FormSection>
        <FormSection title="转发端点" icon={<NodeIndexOutlined />} description="每台服务器选择一个公网或专线地址；更换地址须确认替换，用箭头调整主备顺序。">
          {!exits.length && <Alert type="info" showIcon title={<>暂无已配置的转发端点，请先前往 <a href="#/endpoints">转发端点</a> 添加。</>} className="message" />}
          <Form.Item name="member_addresses" rules={[{ validator: async (_, members: MemberAddress[]) => {
            if (!members?.length) throw new Error('至少选择一个转发端点');
            if (new Set(members.map(member => member.node_id)).size !== members.length) throw new Error('同一服务器只能出现一次');
            if (members.some(member => !options.some(option => option.id === memberKey(member)))) throw new Error('存在未配置的地址，请移除后重新选择');
          } }]}><EndpointMembers disabled={saving} options={options} /></Form.Item>
        </FormSection>
        <div className="endpoint-form-footer"><Button onClick={close}>取消</Button><Button type="primary" htmlType="submit" loading={saving}>保存端点组</Button></div>
      </Form>
    </Modal>
  </div>;
}
