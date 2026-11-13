import { useEffect, useRef, useState } from 'react';
import { Alert, App, Button, Dropdown, Form, Input, Modal, Select, Space, Tooltip, Typography } from 'antd';
import { CodeOutlined, DownOutlined, GlobalOutlined, InfoCircleOutlined, PlusOutlined, SearchOutlined, SettingOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { NodeManagedRecord } from './NodeManagedRecord';
import { NodeRegion } from './NodeRegion';
import { ProbeDetails } from './ProbeDetails';
import { NodeValidationActions, type NodeValidationMode } from './NodeValidationActions';
import { NodeRotation } from './NodeRotation';
import { NodeSelfCheck, SelfCheckSettings } from './NodeSelfCheck';
import { NodeInstallation, type Installation } from './NodeInstallation';
import { NodeServerDetails, serverTone, type ServerAction, type ServerDetailTab } from './NodeServerDetails';
import { serverNamePayload, serverOutboundPayload, matchesServer, serverGroupsFor, serverPortCount, type ServerGroup, type ServerPort, type ServerRow } from './NodeServerData';
import { pageTitles, useFocusRow } from './route';
import { PageHeader } from './PageHeader';
import { FilterCard, FilterField } from './FilterCard';
import { TableCard } from './TableCard';
import { StatusDot } from './StatusDot';
import { ColorTag } from './ColorTag';
import { FormSection } from './FormSection';
import { CpuCell, MemoryCell, NetworkCell, networkRate, uptime } from './NodeMetrics';
import { byMetric, byText, useDragOrder } from './ListOrder';
import './servers.css';

type Filter = 'all' | 'online' | 'offline' | 'maintenance' | 'pending';
const filters: { value: Filter; label: string }[] = [
  { value: 'all', label: '全部状态' }, { value: 'online', label: '在线' }, { value: 'offline', label: '离线' },
  { value: 'maintenance', label: '维护' }, { value: 'pending', label: '待接入' },
];
// 状态列升序为在线优先、离线最后；控制连接中断的在线服务器排在正常在线之后。
function statusRank(row: ServerRow) {
  if (row.status === 'online') return row.connected ? 0 : 1;
  return ({ maintenance: 2, pending: 3, offline: 4 } as Record<string, number>)[row.status] ?? 5;
}

export function Nodes({ focus, onSessionExpired }: { focus?: string; onSessionExpired: () => void }) {
  const { message, modal } = App.useApp();
  const [rows, setRows] = useState<ServerRow[]>([]);
  const [groups, setGroups] = useState<ServerGroup[]>([]);
  const [ports, setPorts] = useState<ServerPort[]>([]);
  const [selectedIDs, setSelectedIDs] = useState<string[]>([]);
  const [batching, setBatching] = useState<boolean>();
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState<Filter>('all');
  const [groupID, setGroupID] = useState('all');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [editing, setEditing] = useState<ServerRow | null>();
  const [saving, setSaving] = useState(false);
  const [outbound, setOutbound] = useState('');
  const [outboundSaving, setOutboundSaving] = useState(false);
  const [outboundStatus, setOutboundStatus] = useState('');
  const [installing, setInstalling] = useState<{ node: { id: string; name: string }; ticket?: Installation }>();
  const [regionNode, setRegionNode] = useState<ServerRow>();
  const [recordNode, setRecordNode] = useState<string>();
  const [rotationNode, setRotationNode] = useState<string>();
  const [selfCheckNode, setSelfCheckNode] = useState<string>();
  const [selfCheckSettings, setSelfCheckSettings] = useState(false);
  const [probeNode, setProbeNode] = useState<string>();
  const [details, setDetails] = useState<{ id: string; tab: ServerDetailTab }>();
  const [validation, setValidation] = useState<{ id: string; mode: NodeValidationMode }>();
  const [form] = Form.useForm<{ name: string }>();
  const expired = useRef(onSessionExpired);
  expired.current = onSessionExpired;

  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else setError((e as Error).message);
  }

  async function load() {
    setLoading(true);
    try {
      const [nodes, memberships, rules] = await Promise.all([
        api<{ nodes: ServerRow[] }>('/nodes'), api<{ entry_groups: ServerGroup[] }>('/entry-groups'), api<{ rules: ServerPort[] }>('/rules'),
      ]);
      setRows(nodes.nodes); setGroups(memberships.entry_groups); setPorts(rules.rules);
      setSelectedIDs(ids => ids.filter(id => nodes.nodes.some(row => row.id === id)));
      setError(''); setLoaded(true);
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }

  const order = useDragOrder(rows, setRows, '/nodes', { filtered: filter !== 'all' || groupID !== 'all' || !!query.trim(), onError: failed, reload: load });
  useEffect(() => {
    void load();
    const timer = window.setInterval(() => { if (!order.busy()) void load(); }, 10_000);
    return () => window.clearInterval(timer);
  }, []);
  // 通知定位的服务器可能不在第一页，先切到对应页再由 useFocusRow 高亮滚动。
  useEffect(() => {
    if (focus && loaded) {
      setQuery(''); setFilter('all'); setGroupID('all');
      order.onChange(undefined, undefined, {});
      const index = rows.findIndex(row => row.id === focus);
      if (index >= 0) setPage(Math.floor(index / pageSize) + 1);
    }
  }, [focus, loaded]);

  function edit(row: ServerRow | null) {
    form.resetFields(); form.setFieldsValue({ name: row?.name ?? '' }); setError(''); setOutbound(row?.outbound_ipv4 ?? ''); setOutboundStatus(''); setEditing(row);
  }

  async function save(values: { name: string }) {
    setSaving(true);
    try {
      if (editing) await api(`/nodes/${editing.id}/name`, 'PUT', serverNamePayload(values));
      else {
        const result = await api<Installation>('/nodes', 'POST', serverNamePayload(values));
        setInstalling({ node: { id: result.id, name: values.name }, ticket: result });
      }
      setEditing(undefined); form.resetFields(); await load();
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }

  async function saveOutbound() {
    if (!editing) return;
    const id = editing.id;
    setOutboundSaving(true); setOutboundStatus('');
    try {
      const saved = await api<{ outbound_ipv4: string }>(`/nodes/${id}/outbound`, 'PUT', serverOutboundPayload(outbound));
      setOutbound(saved.outbound_ipv4); setOutboundStatus('已保存，应用结果见服务器同步状态。'); await load();
    } catch (e) { setOutboundStatus((e as Error).message); failed(e); }
    finally { setOutboundSaving(false); }
  }

  async function revoke(row: ServerRow) {
    try { await api(`/nodes/${row.id}/token/revoke`, 'POST'); await load(); }
    catch (e) { failed(e); }
  }

  async function maintenance(row: ServerRow) {
    try {
      await api(`/nodes/${row.id}/maintenance`, 'PUT', { enabled: row.status !== 'maintenance' });
      await load();
    } catch (e) { failed(e); }
  }

  // 维护是逐服务器状态，没有整批事务；逐个调用后汇总成功与失败。
  async function batchMaintenance(enabled: boolean) {
    setBatching(enabled);
    const targets = rows.filter(row => selectedIDs.includes(row.id));
    const results = await Promise.allSettled(targets.map(row => api(`/nodes/${row.id}/maintenance`, 'PUT', { enabled })));
    const failures = results.flatMap((result, i) => result.status === 'rejected' ? [{ name: targets[i].name, error: result.reason }] : []);
    const ok = targets.length - failures.length;
    const label = enabled ? '维护 10 分钟' : '结束维护';
    if (failures.some(f => f.error instanceof APIError && (f.error.status === 401 || f.error.status === 428))) expired.current();
    else if (failures.length) modal.warning({ title: `${label}：成功 ${ok}，失败 ${failures.length}`, content: failures.map(f => `${f.name}：${(f.error as Error).message}`).join('\n'), styles: { body: { whiteSpace: 'pre-line' } } });
    else message.success(`${label}：已处理 ${ok} 台服务器`);
    setBatching(undefined);
    await load();
  }

  function action(row: ServerRow, key: string) {
    setError('');
    if (key === 'edit') edit(row);
    else if (key === 'region') setRegionNode(row);
    else if (key === 'record') setRecordNode(row.id);
    else if (key === 'rotation') setRotationNode(row.id);
    else if (key === 'probe') setProbeNode(row.id);
    else if (key === 'self-check') setSelfCheckNode(row.id);
    else if (key === 'details' || key === 'upgrade') setDetails({ id: row.id, tab: key === 'upgrade' ? 'upgrade' : 'overview' });
    else if (key === 'install') setInstalling({ node: row });
    else if (key === 'maintenance') void maintenance(row);
    else if (key === 'ports' || key === 'delete') setValidation({ id: row.id, mode: key });
    else if (key === 'revoke') modal.confirm({ title: `吊销「${row.name}」的凭据？`, content: '当前控制连接将断开，原凭据将失效。重新接入时请在安装脚本中生成新的安装票据。', okText: '吊销', okButtonProps: { danger: true }, onOk: () => revoke(row) });
  }

  const visible = rows.filter(row => matchesServer(row, groups, { query, status: filter, groupID }));
  const validationNode = rows.find(row => row.id === validation?.id);
  const detailNode = rows.find(row => row.id === details?.id);
  const currentPage = Math.min(page, Math.max(1, Math.ceil(visible.length / pageSize)));
  const rowClassName = useFocusRow(focus, loaded && (!focus || visible.slice((currentPage - 1) * pageSize, currentPage * pageSize).some(row => row.id === focus)));

  return <div className="servers-page">
    <PageHeader title={pageTitles.servers} description="管理入口服务器、安装状态与实时资源。每 10 秒自动刷新。">
      <Button icon={<SettingOutlined />} onClick={() => setSelfCheckSettings(true)}>自检设置</Button>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => edit(null)}>添加服务器</Button>
    </PageHeader>
    {(error || order.error) && <Alert title={error || order.error} type="error" showIcon className="message" />}
    <FilterCard onRefresh={() => void load()} loading={loading}>
      <FilterField label="名称 / 地址" htmlFor="servers-search"><Input id="servers-search" allowClear prefix={<SearchOutlined />} placeholder="搜索名称 / IP / 域名" value={query} onChange={e => { setQuery(e.target.value); setPage(1); }} /></FilterField>
      <FilterField label="状态" htmlFor="servers-status"><Select id="servers-status" value={filter} onChange={value => { setFilter(value); setPage(1); }} options={filters.map(f => ({ value: f.value, label: `${f.label}（${f.value === 'all' ? rows.length : rows.filter(row => row.status === f.value).length}）` }))} /></FilterField>
      <FilterField label="服务器组" htmlFor="servers-group"><Select id="servers-group" showSearch optionFilterProp="label" value={groupID} onChange={value => { setGroupID(value); setPage(1); }} options={[{ value: 'all', label: '全部分组' }, { value: 'ungrouped', label: '未分组' }, ...groups.map(group => ({ value: group.id, label: group.name }))]} /></FilterField>
      <Button onClick={() => { setQuery(''); setFilter('all'); setGroupID('all'); setPage(1); }}>重置</Button>
    </FilterCard>
    <p className="servers-count">显示 <span className="num">{visible.length} / {rows.length}</span> 条</p>
    <TableCard<ServerRow> actions={selectedIDs.length > 0 || order.restore ? <>
      {selectedIDs.length > 0 && <><span className="servers-selection">已选 <b className="num">{selectedIDs.length}</b> 台</span>
        <Button size="small" loading={batching === true} disabled={batching !== undefined} onClick={() => void batchMaintenance(true)}>维护 10 分钟</Button>
        <Button size="small" loading={batching === false} disabled={batching !== undefined} onClick={() => void batchMaintenance(false)}>结束维护</Button>
        <Button size="small" type="text" onClick={() => setSelectedIDs([])}>取消选择</Button></>}
      {order.restore}
    </> : undefined} rowKey="id" dataSource={visible} loading={!loaded && !error}
      rowSelection={{ fixed: true, columnWidth: 30, selectedRowKeys: selectedIDs, preserveSelectedRowKeys: true, onChange: keys => setSelectedIDs(keys.map(String)) }}
      onChange={(pagination, tableFilters, sorter, extra) => { setPage(extra.action === 'sort' || pagination.pageSize !== pageSize ? 1 : pagination.current ?? 1); setPageSize(pagination.pageSize ?? 20); order.onChange(pagination, tableFilters, sorter); }} onRow={order.onRow}
      rowClassName={row => `servers-record ${rowClassName(row.id)} ${order.rowClass(row.id)}`}
      pagination={{ current: currentPage, pageSize, showSizeChanger: true, pageSizeOptions: [10, 20, 50, 100] }} scroll={{ x: 1080 }}
      locale={{ emptyText: rows.length ? '没有符合条件的服务器' : '暂无服务器。添加服务器后，将安装命令复制到目标机器执行。' }} columns={[
        { ...order.handle, width: 24, className: 'servers-drag-cell', onCell: () => ({ tabIndex: 0, 'aria-label': '拖动调整顺序' }) },
        { title: 'ID', key: 'sequence', fixed: 'left', width: 56, render: (_, _row, index) => <span className="servers-id mono">{(currentPage - 1) * pageSize + index + 1}</span> },
        { title: '显示名称 / 入口地址', fixed: 'left', width: 146, ...order.sorted('name'), sorter: byText(row => row.name), render: (_, row) => <>
          <Button type="link" className="servers-name" onClick={() => action(row, 'details')}>{row.name}</Button>
          <span className="cell-sub mono">{row.managed_record?.hostname || row.confirmed_ipv4 || row.public_ipv4 || '尚未查询到 IP'}</span>
          {row.public_ipv4 && row.public_ipv4 !== row.confirmed_ipv4 && <Button size="small" type="text" className="servers-address-warning" onClick={() => action(row, 'record')}>地址待确认</Button>}
          {row.apply_error && <span className="cell-danger">配置同步失败 · 详情中查看</span>}
        </> },
        { title: '状态', width: 102, ...order.sorted('status'), sorter: (a, b) => statusRank(a) - statusRank(b), render: (_, row) => <>
          <StatusDot tone={serverTone(row)}>{row.status_label}</StatusDot>
          {row.status === 'online' && !row.connected && <span className="cell-danger">控制连接已中断</span>}
          {row.status === 'maintenance' && row.maintenance_until ? <span className="cell-sub">至 <span className="num">{new Date(row.maintenance_until).toLocaleTimeString()}</span></span> : row.probe?.uptime_seconds != null && <span className="cell-sub">运行 {uptime(row.probe.uptime_seconds)}</span>}
        </> },
        { title: '服务器组', width: 100, ...order.sorted('groups'), sorter: byText(row => serverGroupsFor(row.id, groups).map(group => group.name).join('、')), render: (_, row) => {
          const memberships = serverGroupsFor(row.id, groups);
          return <Space wrap size={[4, 4]}>{memberships.length ? memberships.map(group => <ColorTag key={group.id}>{group.name}</ColorTag>) : <ColorTag>未分组</ColorTag>}</Space>;
        } },
        { title: '资源使用', width: 128, ...order.sorted('cpu'), sortDirections: ['descend', 'ascend'], sorter: byMetric<ServerRow>(row => row.probe?.cpu_percent), render: (_, row) => <div className="servers-resources"><div><span>CPU</span><CpuCell probe={row.probe} /></div><div><span>内存</span><MemoryCell probe={row.probe} /></div></div> },
        { title: <Tooltip title="本机及所属服务器组中已启用的业务监听端口数，不含转发与检测预留端口；实际应用状态见详情。">端口数</Tooltip>, width: 76, ...order.sorted('ports'), sorter: (a, b) => serverPortCount(a.id, groups, ports) - serverPortCount(b.id, groups, ports), render: (_, row) => <span className="mono">{serverPortCount(row.id, groups, ports)}</span> },
        { title: <Tooltip title="实测网卡上下行速率，包含该机器上的其他网络流量。">网络速率</Tooltip>, width: 102, ...order.sorted('network'), sortDirections: ['descend', 'ascend'], sorter: byMetric<ServerRow>(row => networkRate(row.probe)), render: (_, row) => <NetworkCell probe={row.probe} /> },
        { title: '版本', width: 116, render: (_, row) => <>
          <span className="mono">{row.software_version || '尚未接入'}</span>
          <Button type="link" size="small" className="servers-version" onClick={() => action(row, 'upgrade')}>{row.upgrade ? '查看升级进度' : row.version_mismatch ? '有版本差异' : '版本与升级'}</Button>
        </> },
        { title: '操作', key: 'actions', fixed: 'right', width: 120, align: 'right', render: (_, row) => <div className="row-actions servers-row-actions">
          <Button size="small" type="link" onClick={() => edit(row)}>编辑</Button>
          <Dropdown trigger={['click']} placement="bottomRight" menu={{ onClick: ({ key }) => action(row, key), items: [
            { key: 'details', label: '服务器详情' }, { key: 'install', label: '安装脚本' }, { key: 'upgrade', label: '升级与版本' },
            { type: 'divider' },
            { key: 'maintenance', label: row.status === 'maintenance' ? '结束维护' : '维护 10 分钟' },
            { key: 'self-check', label: '自检与被墙认定' }, { key: 'probe', label: '探针指标详情' }, { key: 'region', label: '地区与检测点' },
            { key: 'rotation', label: '换 IP / 自动轮换' }, { key: 'record', label: '受管记录与地址' }, { key: 'ports', label: '监听端口设置' },
            { type: 'divider' }, { key: 'revoke', label: '吊销凭据', danger: true }, { key: 'delete', label: '删除服务器', danger: true },
          ] }}><Button size="small" type="link" aria-label={`${row.name} 的更多操作`}>更多 <DownOutlined /></Button></Dropdown>
        </div> },
      ]} />
    {selfCheckSettings && <SelfCheckSettings onClose={() => setSelfCheckSettings(false)} onError={failed} />}
    {rows.find(row => row.id === selfCheckNode) && <NodeSelfCheck key={selfCheckNode} node={rows.find(row => row.id === selfCheckNode)!} onClose={() => setSelfCheckNode(undefined)} onError={failed} />}
    {rows.find(row => row.id === recordNode) && <NodeManagedRecord key={recordNode} node={rows.find(row => row.id === recordNode)!} onClose={() => setRecordNode(undefined)} onSaved={load} onError={failed} />}
    <NodeRotation node={rows.find(row => row.id === rotationNode)} error={error} onClose={() => setRotationNode(undefined)} onError={failed} />
    <NodeRegion node={regionNode} error={error} onClose={() => setRegionNode(undefined)} onSaved={load} onError={failed} />
    {rows.find(row => row.id === probeNode) && <ProbeDetails node={rows.find(row => row.id === probeNode)!} onClose={() => setProbeNode(undefined)} onSessionExpired={onSessionExpired} />}
    {validationNode && <NodeValidationActions node={validationNode} mode={validation?.mode} onClose={() => setValidation(undefined)} onSaved={load} onError={failed} />}
    {detailNode && details && <NodeServerDetails key={`${detailNode.id}-${details.tab}`} node={detailNode} groups={groups} initialTab={details.tab} error={error} onClose={() => setDetails(undefined)} onAction={(key: ServerAction) => action(detailNode, key)} onSaved={load} onError={failed} />}
    {installing && <NodeInstallation key={installing.node.id} node={installing.node} initialTicket={installing.ticket} onClose={() => setInstalling(undefined)} onSaved={load} onError={failed} />}
    <Modal title={editing ? '编辑服务器' : '添加服务器'} open={editing !== undefined} onCancel={() => { if (!outboundSaving) { setEditing(undefined); form.resetFields(); } }} footer={null} width={680} className="servers-modal" destroyOnHidden>
      {error && <Alert type="error" showIcon title={error} className="message" />}
      <Form form={form} layout="vertical" onFinish={save} clearOnDestroy>
        <FormSection title="基本信息" icon={<InfoCircleOutlined />} description={editing ? '修改显示名称；服务器地址由实际运行的程序上报。' : '创建服务器后生成一次性安装票据，在目标机器执行安装命令即可接入。'}>
          <Form.Item name="name" label="显示名称" rules={[{ required: true, whitespace: true, message: '请输入服务器名称' }, { validator: (_, value: string) => new TextEncoder().encode(value?.trim() || '').length <= 200 ? Promise.resolve() : Promise.reject(new Error('服务器名称最多 200 字节')) }]} extra="用于区分服务器，例如 HK-CN2-01。"><Input autoFocus maxLength={200} placeholder="请输入服务器名称" /></Form.Item>
          {editing && <Form.Item label="公网 IPv4" extra="地址确认及域名绑定在「受管记录与地址」中管理。"><Input readOnly value={editing.public_ipv4 || '尚未查询到 IP'} /></Form.Item>}
        </FormSection>
        {editing && <FormSection title="服务器设置" icon={<GlobalOutlined />} description="出站 IP、地区、国内检测点和监听端口分别保存；关闭弹窗后仍可在更多操作中管理。" collapsible>
          <Form.Item label="出站 IP" extra="可选 IPv4，仅连接最终目标时绑定。地址须已配置在本机接口；留空使用系统路由。">
            <Space.Compact block><Input value={outbound} onChange={e => { setOutbound(e.target.value); setOutboundStatus(''); }} placeholder="留空使用系统路由" aria-label="出站 IP" disabled={outboundSaving} /><Button onClick={() => void saveOutbound()} loading={outboundSaving} disabled={saving}>保存出站 IP</Button></Space.Compact>
            {outboundStatus && <Typography.Text type="secondary" role="status">{outboundStatus}</Typography.Text>}
          </Form.Item>
          <Space wrap className="servers-section-actions"><Button onClick={() => setRegionNode(editing)}>地区与检测点</Button><Button icon={<CodeOutlined />} onClick={() => setValidation({ id: editing.id, mode: 'ports' })}>监听端口设置</Button></Space>
        </FormSection>}
        <div className="servers-form-footer"><Button disabled={outboundSaving} onClick={() => setEditing(undefined)}>取消</Button><Button type="primary" htmlType="submit" loading={saving} disabled={outboundSaving}>{editing ? '保存' : '生成安装脚本'}</Button></div>
      </Form>
    </Modal>
  </div>;
}
