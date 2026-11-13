import { useEffect, useRef, useState } from 'react';
import { Alert, App, Button, Dropdown, Form, Input, Modal, Tooltip, Typography } from 'antd';
import { ClusterOutlined, DownOutlined, NodeIndexOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons';
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
  const { modal } = App.useApp();
  const [rows, setRows] = useState<EndpointGroup[]>([]);
  const [nodes, setNodes] = useState<EndpointNode[]>([]);
  const [exits, setExits] = useState<Endpoint[]>([]);
  const [query, setQuery] = useState('');
  const [editing, setEditing] = useState<EndpointGroup | null>();
  const [error, setError] = useState('');
  const [saveError, setSaveError] = useState('');
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
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
      setRows(groups.exit_groups); setNodes(members.nodes); setExits(configured.exits); setError(''); setLoaded(true);
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }
  const order = useDragOrder(rows, setRows, '/exit-groups', { filtered: !!query.trim(), onError: failed, reload: load });
  useEffect(() => { void load(); }, []);
  useEffect(() => {
    if (!focus || !loaded) return;
    const index = rows.findIndex(row => row.id === focus);
    if (index >= 0) {
      setQuery(''); order.onChange(undefined, undefined, {});
      setPage(Math.floor(index / pageSize) + 1);
    }
  }, [focus, loaded, pageSize]);
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
  const currentPage = Math.min(page, Math.max(1, Math.ceil(visible.length / pageSize)));
  const focused = useFocusRow(focus, !loading && visible.slice((currentPage - 1) * pageSize, currentPage * pageSize).some(row => row.id === focus));
  return <div className="endpoint-page">
    <PageHeader title={pageTitles['endpoint-groups']} description="按主备顺序组织转发端点，供单跳转发端口使用。">
      <Button href="#/endpoints">管理转发端点</Button>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => edit(null)}>创建端点组</Button>
    </PageHeader>
    {(error || order.error) && <Alert title={error || order.error} type="error" showIcon className="message" />}
    <FilterCard onRefresh={() => void load()} loading={loading}>
      <FilterField label="搜索" htmlFor="endpoint-group-search"><Input id="endpoint-group-search" prefix={<SearchOutlined />} allowClear placeholder="名称、ID 或成员" value={query} onChange={event => { setQuery(event.target.value); setPage(1); }} /></FilterField>
    </FilterCard>
    <p className="endpoint-count">显示 <span className="num">{visible.length} / {rows.length}</span> 条</p>
    <TableCard<EndpointGroup> actions={order.restore ? <>{order.restore}</> : undefined} rowKey="id" dataSource={visible} loading={loading}
      onChange={(pagination, tableFilters, sorter, extra) => { setPage(extra.action === 'sort' || pagination.pageSize !== pageSize ? 1 : pagination.current ?? 1); setPageSize(pagination.pageSize ?? 20); order.onChange(pagination, tableFilters, sorter); }} onRow={order.onRow}
      rowClassName={row => `endpoint-group-record ${focused(row.id)} ${order.rowClass(row.id)}`}
      pagination={{ current: currentPage, pageSize, showSizeChanger: true, pageSizeOptions: [10, 20, 50, 100] }} scroll={{ x: 600 }} locale={{ emptyText: query.trim() ? '没有匹配的端点组' : '暂无端点组，请先配置转发端点，再创建端点组。' }} columns={[
      { ...order.handle, width: 24, className: 'endpoint-group-drag-cell', onCell: () => ({ tabIndex: 0, 'aria-label': '拖动调整顺序' }) },
      { title: 'ID', key: 'sequence', fixed: 'left', width: 56, render: (_, _row, index) => <span className="endpoint-sequence mono">{(currentPage - 1) * pageSize + index + 1}</span> },
      { title: '名称 / 成员', dataIndex: 'name', fixed: 'left', width: 240, ...order.sorted('name'), sorter: byText(row => row.name), render: (_, row) => {
        const details = <div className="endpoint-address-details">{row.member_addresses.map((member, index) => <div key={memberKey(member)}>{index + 1}. {index === 0 ? '主' : '备'} · {memberDescription(member)}</div>)}</div>;
        return <><span className="node-name">{row.name}</span>{row.member_addresses.length ? <div className="cell-sub endpoint-group-members">
          <Tooltip title={details}><span className="endpoint-group-primary" tabIndex={0}>{memberDescription(row.member_addresses[0])}</span></Tooltip>
          {row.member_addresses.length > 1 && <Tooltip title={details}><span className="endpoint-group-backups" tabIndex={0}><ColorTag>+{row.member_addresses.length - 1} 备用</ColorTag></span></Tooltip>}
        </div> : <span className="cell-sub muted">暂无成员</span>}</>;
      } },
      { title: '成员数', width: 90, render: (_, row) => <span className="mono">{row.member_addresses.length}</span> },
      { title: '操作', key: 'actions', fixed: 'right', width: 120, align: 'right', render: (_, row) => <div className="row-actions endpoint-row-actions">
        <Button type="link" size="small" onClick={() => edit(row)}>编辑</Button>
        <Dropdown trigger={['click']} placement="bottomRight" menu={{ items: [
          { key: 'delete', label: '删除', danger: true },
        ], onClick: ({ key }) => { if (key === 'delete') modal.confirm({ title: '删除端点组？', content: '须先删除引用该组的转发端口。', okText: '删除', okButtonProps: { danger: true }, onOk: () => remove(row.id) }); } }}>
          <Button type="link" size="small" aria-label={`${row.name} 的更多操作`}>更多 <DownOutlined /></Button>
        </Dropdown>
      </div> },
    ]} />
    <p className="footnote">可拖动列表手柄调整端点组的显示顺序；跨页排序时可把每页条数调大。组内第一项为主端点，其余按顺序备用；故障恢复后连续探测成功 60 秒才重新接新连接。</p>
    <Modal title={editing ? '编辑端点组' : '创建端点组'} open={editing !== undefined} onCancel={close} footer={null} width={1060} closable={!saving} maskClosable={!saving} keyboard={!saving} forceRender className="endpoint-modal">
      <Form form={form} layout="vertical" onFinish={save} disabled={saving} requiredMark={false}>
        {saveError && <Alert title={saveError} type="error" showIcon className="message" />}
        <FormSection title="基本信息" icon={<ClusterOutlined />}>
          {editing && <Form.Item label="完整 ID"><Typography.Text className="endpoint-id mono" copyable>{editing.id}</Typography.Text></Form.Item>}
          <Form.Item name="name" label="端点组名称" rules={[{ required: true, whitespace: true, message: '请输入端点组名称' }]}><Input maxLength={200} placeholder="请输入端点组名称" /></Form.Item>
          <Form.Item label="转发策略" extra="第一项为主端点，其余按顺序备用；恢复后连续探测成功 60 秒再接新连接。"><ColorTag tone="orange">主备 · 按顺序</ColorTag></Form.Item>
        </FormSection>
        <FormSection title="转发端点" icon={<NodeIndexOutlined />} description="选择服务器作为转发端点，用箭头调整主备顺序。">
          {!exits.length && <Alert type="info" showIcon title={<>暂无已配置的转发端点，请先前往 <a href="#/endpoints">转发端点</a> 添加。</>} className="message" />}
          <Form.Item name="member_addresses" rules={[{ validator: async (_, members: MemberAddress[]) => {
            if (!members?.length) throw new Error('至少选择一个转发端点');
            if (new Set(members.map(member => member.node_id)).size !== members.length) throw new Error('同一服务器只能出现一次');
            if (members.some(member => !options.some(option => option.id === memberKey(member)))) throw new Error('存在未配置的端点，请移除后重新选择');
          } }]}><EndpointMembers disabled={saving} options={options} /></Form.Item>
        </FormSection>
        <div className="endpoint-form-footer"><Button onClick={close}>取消</Button><Button type="primary" htmlType="submit" loading={saving}>保存端点组</Button></div>
      </Form>
    </Modal>
  </div>;
}
