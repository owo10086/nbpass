import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Dropdown, Form, Input, InputNumber, Modal, Select, Space, Tooltip, Typography } from 'antd';
import { CloudServerOutlined, DownOutlined, LockOutlined, PlusOutlined, SafetyCertificateOutlined, SearchOutlined } from '@ant-design/icons';
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
import { pageTitles, useFocusRow } from './route';
import { endpointAddressDetails, endpointFormValues, endpointPayload, effectiveEndpointPort } from './EndpointData';
import type { Endpoint, EndpointGroup, EndpointNode, EndpointValues } from './EndpointData';
import './Endpoints.css';

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
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [rowAction, setRowAction] = useState<{ kind: 'rotation' | 'blocked'; nodeID: string }>();
  const [form] = Form.useForm<EndpointValues>();
  const nodeID = Form.useWatch('node_id', form);
  const configured = exits.find(exit => exit.node_id === nodeID);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
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
      setExits(configured.exits); setNodes(members.nodes); setGroups(grouped.exit_groups); setError(''); setLoaded(true);
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }
  useEffect(() => { void load(); }, []);
  useEffect(() => {
    if (!focus || !loaded) return;
    const index = exits.findIndex(exit => exit.node_id === focus);
    if (index >= 0) {
      setQuery(''); setStatus(undefined); setGroup(undefined);
      setPage(Math.floor(index / pageSize) + 1);
    }
  }, [focus, loaded, pageSize]);
  function edit(row: Endpoint | null) {
    setSaveError(''); form.resetFields();
    form.setFieldsValue(endpointFormValues(row ?? undefined));
    setEditing(row);
  }
  function close() { setEditing(undefined); setSaveError(''); form.resetFields(); }
  async function save(values: EndpointValues) {
    setSaving(true); setSaveError('');
    try {
      await api(`/nodes/${values.node_id}/tunnel`, 'PUT', endpointPayload(values));
      close(); await load();
    } catch (e) { failed(e, true); }
    finally { setSaving(false); }
  }
  const nodeName = (id: string) => nodes.find(node => node.id === id)?.name ?? id;
  const endpointGroups = (id: string) => groups.filter(group => group.member_addresses.some(member => member.node_id === id));
  const visible = exits.filter(exit => {
    const memberships = endpointGroups(exit.node_id);
    const node = nodes.find(node => node.id === exit.node_id);
    return `${exit.node_id} ${nodeName(exit.node_id)} ${exit.address}:${effectiveEndpointPort(exit)}`.toLowerCase().includes(query.trim().toLowerCase())
      && (!status || (status === 'online' ? node?.connected === true : node?.connected === false))
      && (!group || (group === 'ungrouped' ? !memberships.length : memberships.some(item => item.id === group)));
  });
  const currentPage = Math.min(page, Math.max(1, Math.ceil(visible.length / pageSize)));
  const focused = useFocusRow(focus, !loading && visible.slice((currentPage - 1) * pageSize, currentPage * pageSize).some(row => row.node_id === focus));
  return <div className="endpoint-page">
    <PageHeader title={pageTitles.endpoints} description="配置服务器的 WSS 转发地址，管理端点组归属、证书身份与链路健康。">
      <Button href="#/endpoint-groups">管理端点组</Button>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => edit(null)}>添加转发端点</Button>
    </PageHeader>
    {error && <Alert title={error} type="error" showIcon className="message" />}
    <FilterCard onRefresh={() => void load()} loading={loading}>
      <FilterField label="搜索" htmlFor="endpoint-search"><Input id="endpoint-search" prefix={<SearchOutlined />} allowClear placeholder="名称、地址或 ID" value={query} onChange={event => { setQuery(event.target.value); setPage(1); }} /></FilterField>
      <FilterField label="控制状态" htmlFor="endpoint-status"><Select id="endpoint-status" allowClear placeholder="全部状态" value={status} onChange={value => { setStatus(value); setPage(1); }} options={[{ value: 'online', label: '控制在线' }, { value: 'offline', label: '控制离线' }]} /></FilterField>
      <FilterField label="端点组" htmlFor="endpoint-group"><Select id="endpoint-group" showSearch optionFilterProp="label" allowClear placeholder="全部分组" value={group} onChange={value => { setGroup(value); setPage(1); }} options={[{ value: 'ungrouped', label: '未分组' }, ...groups.map(group => ({ value: group.id, label: group.name }))]} /></FilterField>
    </FilterCard>
    <p className="endpoint-count">显示 <span className="num">{visible.length} / {exits.length}</span> 条</p>
    <TableCard<Endpoint> rowKey="node_id" dataSource={visible} loading={loading}
      onChange={pagination => { setPage(pagination.pageSize !== pageSize ? 1 : pagination.current ?? 1); setPageSize(pagination.pageSize ?? 20); }}
      pagination={{ current: currentPage, pageSize, showSizeChanger: true, pageSizeOptions: [10, 20, 50, 100] }} rowClassName={row => focused(row.node_id)} scroll={{ x: 800 }} locale={{ emptyText: query.trim() || status || group ? '没有匹配的转发端点' : '暂无转发端点，请先添加服务器并配置 WSS 地址。' }} columns={[
      { title: 'ID', key: 'sequence', fixed: 'left', width: 56, render: (_, _row, index) => <span className="endpoint-sequence mono">{(currentPage - 1) * pageSize + index + 1}</span> },
      { title: '名称 / 地址', fixed: 'left', width: 220, render: (_, row) => {
        const details = <div className="endpoint-address-details">{endpointAddressDetails(row).map((detail, index) => <div key={index}>{detail.label}：<span className="mono">{detail.value}</span></div>)}</div>;
        return <><div className="endpoint-name-line"><span className="node-name">{nodeName(row.node_id)}</span>
        </div><Tooltip title={details}><span className="cell-sub mono endpoint-address" tabIndex={0}>{row.address}:{effectiveEndpointPort(row)}</span></Tooltip></>;
      } },
      { title: '协议', width: 72, render: () => <ColorTag tone="blue">WSS</ColorTag> },
      { title: '状态', width: 120, render: (_, row) => { const node = nodes.find(node => node.id === row.node_id); return <StatusDot tone={!node ? 'muted' : node.connected ? 'ok' : 'danger'}>{!node ? '控制状态未知' : node.connected ? '控制在线' : '控制离线'}</StatusDot>; } },
      { title: '所属端点组', render: (_, row) => { const memberships = endpointGroups(row.node_id); return <Space wrap size={[4, 4]}>{memberships.length ? memberships.map(group => <a key={group.id} href={`#/endpoint-groups?focus=${encodeURIComponent(group.id)}`}><ColorTag>{group.name}</ColorTag></a>) : <span className="muted">未分组</span>}</Space>; } },
      { title: '操作', key: 'actions', fixed: 'right', width: 120, align: 'right', render: (_, row) => <div className="row-actions endpoint-row-actions">
        <Button type="link" size="small" onClick={() => edit(row)}>编辑</Button>
        <Dropdown trigger={['click']} placement="bottomRight" menu={{ items: [
          { key: 'rotation', label: '证书与凭据轮换' }, { key: 'blocked', label: '被墙处理与地址进度' },
        ], onClick: ({ key }) => setRowAction({ kind: key as 'rotation' | 'blocked', nodeID: row.node_id }) }}>
          <Button type="link" size="small" aria-label={`${nodeName(row.node_id)} 的更多操作`}>更多 <DownOutlined /></Button>
        </Dropdown>
      </div> },
    ]} />
    <p className="footnote">状态表示服务器与面板的控制连接；各入口到端点的链路状态请查看下方健康观测。端点名称沿用所属服务器名称，可在服务器管理中修改。</p>
    <EndpointHealth exits={exits} nodes={nodes} onSessionExpired={onSessionExpired} />
    {rowAction?.kind === 'rotation' && <TunnelRotation nodeID={rowAction.nodeID} open onClose={() => setRowAction(undefined)} onSessionExpired={onSessionExpired} onChanged={() => void load()} />}
    {rowAction?.kind === 'blocked' && <ExitBlocked nodeID={rowAction.nodeID} open onClose={() => setRowAction(undefined)} onSessionExpired={onSessionExpired} onChanged={() => void load()} />}
    <Modal title={editing ? '编辑转发端点' : '添加转发端点'} open={editing !== undefined} onCancel={close} footer={null} width={760} closable={!saving} keyboard={!saving} forceRender className="endpoint-modal">
      <Form form={form} layout="vertical" onFinish={save} disabled={saving} requiredMark={false}>
        {saveError && <Alert title={saveError} type="error" showIcon className="message" />}
        <FormSection title="端点配置" icon={<CloudServerOutlined />}>
          {!nodes.length && <Alert type="info" showIcon title={<>请先在 <a href="#/servers">服务器管理</a> 添加服务器。</>} className="message" />}
          {editing && <Form.Item label="完整 ID（node_id）"><Typography.Text className="endpoint-id mono" copyable>{editing.node_id}</Typography.Text></Form.Item>}
          <Form.Item name="node_id" label="所属服务器" extra="一个服务器对应一个 WSS 转发端点，端点名称沿用服务器名称。" rules={[{ required: true, message: '请选择所属服务器' }]}><Select showSearch optionFilterProp="label" disabled={saving || !!editing} placeholder="选择服务器" options={nodes.map(node => ({ value: node.id, label: `${node.name}${exits.some(exit => exit.node_id === node.id) ? '（已配置）' : ''}` }))} onChange={id => { const exit = exits.find(exit => exit.node_id === id); form.setFieldsValue(endpointFormValues(exit)); }} /></Form.Item>
          {!editing && configured && <Alert type="info" showIcon title="该服务器已有转发端点，保存将更新其地址与端口。" className="message" />}
          <Form.Item name="address" label="连接地址（IPv4 或域名）" extra="填写前一跳可连接的 IPv4 或域名，可使用公网或内网地址。自动换公网 IP 会更新此地址，内网或专线部署请关闭自动换 IP。域名只解析 IPv4。" rules={[{ required: true, whitespace: true, message: '请输入 IPv4 或域名' }]}><Input maxLength={253} placeholder="例如：203.0.113.20 或 exit.example.com" /></Form.Item>
          <div className="endpoint-form-grid">
            <Form.Item label="传输协议" extra="使用 WSS（加密 WebSocket）转发连接。"><ColorTag tone="blue">WSS</ColorTag></Form.Item>
            <Form.Item name="listen_port" label="本机 WSS 监听端口" extra="服务器实际监听的端口，须放行且不能与本机业务或检测端口冲突。" rules={[{ required: true, message: '请输入监听端口' }]}><InputNumber min={1} max={65535} precision={0} className="endpoint-port" placeholder="1–65535" /></Form.Item>
          </div>
          <Form.Item name="public_port" label="对外端口（可空）" extra="前一跳连接对外端口，由运营商/NAT 映射到本机监听端口；留空使用本机监听端口。"><InputNumber min={1} max={65535} precision={0} className="endpoint-port" placeholder="同本机监听端口" /></Form.Item>
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
