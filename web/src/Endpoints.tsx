import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Form, Input, InputNumber, Modal, Select, Space, Typography } from 'antd';
import { CloudServerOutlined, LockOutlined, PlusOutlined, SafetyCertificateOutlined, SearchOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { PageHeader } from './PageHeader';
import { FilterCard, FilterField } from './FilterCard';
import { TableCard } from './TableCard';
import { StatusDot } from './StatusDot';
import { ColorTag } from './ColorTag';
import { FormSection } from './FormSection';
import { TunnelRotation } from './TunnelRotation';
import { ExitBlocked } from './ExitBlocked';
import { EndpointHealth } from './EndpointHealth';
import { useFocusRow } from './route';
import type { Endpoint, EndpointGroup, EndpointNode } from './EndpointData';
import './Endpoints.css';

type EndpointValues = { node_id: string; address: string; listen_port: number };

export function Endpoints({ focus, onSessionExpired }: { focus?: string; onSessionExpired: () => void }) {
  const [exits, setExits] = useState<Endpoint[]>([]);
  const [nodes, setNodes] = useState<EndpointNode[]>([]);
  const [groups, setGroups] = useState<EndpointGroup[]>([]);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<string>();
  const [group, setGroup] = useState<string>();
  const [editing, setEditing] = useState<Endpoint | null>();
  const [error, setError] = useState('');
  const [saveError, setSaveError] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [page, setPage] = useState(1);
  const [form] = Form.useForm<EndpointValues>();
  const nodeID = Form.useWatch('node_id', form);
  const configured = exits.find(exit => exit.node_id === nodeID);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  const focused = useFocusRow(focus, !loading && !!exits.length);
  function failed(e: unknown, saving = false) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else (saving ? setSaveError : setError)((e as Error).message);
  }
  async function load() {
    setLoading(true);
    try {
      const [configured, members, grouped] = await Promise.all([
        api<{ exits: Endpoint[] }>('/tunnel-exits'), api<{ nodes: EndpointNode[] }>('/nodes'), api<{ exit_groups: EndpointGroup[] }>('/exit-groups'),
      ]);
      setExits(configured.exits); setNodes(members.nodes); setGroups(grouped.exit_groups); setError('');
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }
  useEffect(() => { void load(); }, []);
  useEffect(() => {
    if (!focus) return;
    const index = exits.findIndex(exit => exit.node_id === focus);
    if (index >= 0) setPage(Math.floor(index / 10) + 1);
  }, [focus, exits]);
  function edit(row: Endpoint | null) {
    setSaveError(''); form.resetFields();
    if (row) form.setFieldsValue(row);
    setEditing(row);
  }
  function close() { setEditing(undefined); setSaveError(''); form.resetFields(); }
  async function save(values: EndpointValues) {
    setSaving(true); setSaveError('');
    try {
      await api(`/nodes/${values.node_id}/tunnel`, 'PUT', { address: values.address.trim(), listen_port: values.listen_port, transport: 'wss' });
      close(); await load();
    } catch (e) { failed(e, true); }
    finally { setSaving(false); }
  }
  const nodeName = (id: string) => nodes.find(node => node.id === id)?.name ?? id;
  const endpointGroups = (id: string) => groups.filter(group => group.members.includes(id));
  const visible = exits.filter(exit => {
    const memberships = endpointGroups(exit.node_id);
    const node = nodes.find(node => node.id === exit.node_id);
    return `${exit.node_id} ${nodeName(exit.node_id)} ${exit.address}:${exit.listen_port}`.toLowerCase().includes(query.trim().toLowerCase())
      && (!status || (status === 'online' ? node?.connected === true : node?.connected === false))
      && (!group || (group === 'ungrouped' ? !memberships.length : memberships.some(item => item.id === group)));
  });
  return <div className="endpoint-page">
    <PageHeader title="转发端点" description="配置服务器的 WSS 转发地址，管理端点组归属、证书身份与链路健康。">
      <Button href="#/endpoint-groups">管理端点组</Button>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => edit(null)}>添加转发端点</Button>
    </PageHeader>
    {error && <Alert title={error} type="error" showIcon className="message" />}
    <FilterCard onRefresh={() => void load()} loading={loading}>
      <FilterField label="搜索" htmlFor="endpoint-search"><Input id="endpoint-search" prefix={<SearchOutlined />} allowClear placeholder="名称、地址或 ID" value={query} onChange={event => { setQuery(event.target.value); setPage(1); }} /></FilterField>
      <FilterField label="控制状态" htmlFor="endpoint-status"><Select id="endpoint-status" allowClear placeholder="全部状态" value={status} onChange={value => { setStatus(value); setPage(1); }} options={[{ value: 'online', label: '控制在线' }, { value: 'offline', label: '控制离线' }]} /></FilterField>
      <FilterField label="端点组" htmlFor="endpoint-group"><Select id="endpoint-group" showSearch optionFilterProp="label" allowClear placeholder="全部分组" value={group} onChange={value => { setGroup(value); setPage(1); }} options={[{ value: 'ungrouped', label: '未分组' }, ...groups.map(group => ({ value: group.id, label: group.name }))]} /></FilterField>
    </FilterCard>
    <TableCard<Endpoint> title="转发端点列表" rowKey="node_id" dataSource={visible} loading={loading} pagination={{ current: page, pageSize: 10, showSizeChanger: false, onChange: setPage }} rowClassName={row => focused(row.node_id)} scroll={{ x: 1050 }} locale={{ emptyText: query.trim() || status || group ? '没有匹配的转发端点' : '暂无转发端点，请先添加服务器并配置 WSS 地址。' }} columns={[
      { title: 'ID', dataIndex: 'node_id', fixed: 'left', width: 120, render: value => <Typography.Text className="endpoint-id mono" ellipsis={{ tooltip: value }} copyable={{ text: value }}>{value.slice(0, 8)}…</Typography.Text> },
      { title: '名称 / 地址', fixed: 'left', width: 220, render: (_, row) => <><span className="node-name">{nodeName(row.node_id)}</span><span className="cell-sub mono">{row.address}:{row.listen_port}</span></> },
      { title: '协议', width: 72, render: () => <ColorTag tone="blue">WSS</ColorTag> },
      { title: '状态', width: 120, render: (_, row) => { const node = nodes.find(node => node.id === row.node_id); return <StatusDot tone={!node ? 'muted' : node.connected ? 'ok' : 'danger'}>{!node ? '控制状态未知' : node.connected ? '控制在线' : '控制离线'}</StatusDot>; } },
      { title: '所属端点组', render: (_, row) => { const memberships = endpointGroups(row.node_id); return <Space wrap size={[4, 4]}>{memberships.length ? memberships.map(group => <a key={group.id} href={`#/endpoint-groups?focus=${encodeURIComponent(group.id)}`}><ColorTag>{group.name}</ColorTag></a>) : <span className="muted">未分组</span>}</Space>; } },
      { title: '操作', key: 'actions', fixed: 'right', width: 280, align: 'right', render: (_, row) => <div className="row-actions endpoint-row-actions">
        <Button type="text" size="small" onClick={() => edit(row)}>编辑</Button>
        <TunnelRotation nodeID={row.node_id} onSessionExpired={onSessionExpired} onChanged={() => void load()} />
        <ExitBlocked nodeID={row.node_id} onSessionExpired={onSessionExpired} onChanged={() => void load()} />
      </div> },
    ]} />
    <p className="footnote">状态表示服务器与面板的控制连接；各入口到端点的链路状态请查看下方健康观测。端点名称沿用所属服务器名称，可在服务器管理中修改。</p>
    <EndpointHealth nodes={nodes} onSessionExpired={onSessionExpired} />
    <Modal title={editing ? '编辑转发端点' : '添加转发端点'} open={editing !== undefined} onCancel={close} footer={null} width={760} closable={!saving} maskClosable={!saving} keyboard={!saving} forceRender className="endpoint-modal">
      <Form form={form} layout="vertical" onFinish={save} disabled={saving} requiredMark={false}>
        {saveError && <Alert title={saveError} type="error" showIcon className="message" />}
        <FormSection title="端点配置" icon={<CloudServerOutlined />}>
          {!nodes.length && <Alert type="info" showIcon title={<>请先在 <a href="#/servers">服务器管理</a> 添加服务器。</>} className="message" />}
          <Form.Item name="node_id" label="所属服务器" extra="一个服务器对应一个 WSS 转发端点，端点名称沿用服务器名称。" rules={[{ required: true, message: '请选择所属服务器' }]}><Select showSearch optionFilterProp="label" disabled={saving || !!editing} placeholder="选择服务器" options={nodes.map(node => ({ value: node.id, label: `${node.name}${exits.some(exit => exit.node_id === node.id) ? '（已配置）' : ''}` }))} onChange={id => { const exit = exits.find(exit => exit.node_id === id); form.setFieldsValue({ address: exit?.address, listen_port: exit?.listen_port }); }} /></Form.Item>
          {!editing && configured && <Alert type="info" showIcon title="该服务器已有转发端点，保存将更新其地址与端口。" className="message" />}
          <Form.Item name="address" label="可直接入站的 IPv4" extra="填写入口可以访问的端点地址；出站公网 IP 观测值不能证明可入站。" rules={[{ required: true, whitespace: true, message: '请输入端点 IPv4' }]}><Input maxLength={15} placeholder="例如：203.0.113.20" /></Form.Item>
          <div className="endpoint-form-grid">
            <Form.Item label="传输协议" extra="使用 WSS（加密 WebSocket）转发连接。"><ColorTag tone="blue">WSS</ColorTag></Form.Item>
            <Form.Item name="listen_port" label="WSS 监听端口" extra="须在所属服务器上放行此端口。" rules={[{ required: true, message: '请输入监听端口' }]}><InputNumber min={1} max={65535} precision={0} className="endpoint-port" placeholder="1–65535" /></Form.Item>
          </div>
        </FormSection>
        <FormSection title="证书身份" icon={<SafetyCertificateOutlined />} description="面板自动生成有效期 5 年的端点证书，更换地址会保留证书身份。">
          {configured ? <><Form.Item label="证书身份（SNI）"><Typography.Text copyable className="mono endpoint-certificate">{configured.server_name}</Typography.Text></Form.Item>
            <FormSection title="查看 PEM 证书" icon={<LockOutlined />} collapsible defaultCollapsed><Typography.Paragraph copyable className="mono endpoint-certificate-pem">{configured.certificate_pem}</Typography.Paragraph></FormSection></> : <p className="muted">保存端点配置后自动生成证书身份。</p>}
        </FormSection>
        <p className="footnote">保存后请到服务器管理核对配置同步结果；证书与凭据轮换、被墙处理可从端点列表的行操作打开。</p>
        <div className="endpoint-form-footer"><Button onClick={close}>取消</Button><Button type="primary" htmlType="submit" loading={saving}>保存转发端点</Button></div>
      </Form>
    </Modal>
  </div>;
}
