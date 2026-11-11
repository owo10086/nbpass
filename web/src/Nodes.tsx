import { useEffect, useRef, useState } from 'react';
import { Alert, App, Button, Dropdown, Form, Input, Modal, Segmented, Table, Typography } from 'antd';
import { MoreOutlined, PlusOutlined, ReloadOutlined, SearchOutlined, SettingOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { NodeUpgrade, type NodeUpgradeFields } from './NodeUpgrade';
import { NodeManagedRecord, type NodeRecord } from './NodeManagedRecord';
import { NodeRegion, type NodeRegionFields } from './NodeRegion';
import { ProbeDetails } from './ProbeDetails';
import { NodeValidationActions, type NodeValidationMode } from './NodeValidationActions';
import { NodeRotation } from './NodeRotation';
import { NodeSelfCheck, SelfCheckSettings } from './NodeSelfCheck';
import { useFocusRow } from './route';
import { PageHeader } from './PageHeader';
import { ConnectionsCell, CpuCell, MemoryCell, memoryPercent, NetworkCell, networkRate, uptime, type NodeProbe } from './NodeMetrics';
import { byMetric, byText, useDragOrder } from './ListOrder';

type NodeRow = NodeRegionFields & NodeUpgradeFields & {
  id: string; name: string; status: string; status_label: string; connected: boolean;
  last_seen_at: string | null; maintenance_until: string | null;
  desired_revision: number; applied_revision: number; config_status: string; software_version: string; apply_error: string;
  probe: NodeProbe; confirmed_ipv4: string; managed_record: NodeRecord | null; record_status: string;
};
type Installation = { id: string; installation_command: string; ticket_expires_at: string };
type Filter = 'all' | 'online' | 'offline' | 'maintenance' | 'pending';

const filters: { value: Filter; label: string }[] = [
  { value: 'all', label: '全部' }, { value: 'online', label: '在线' }, { value: 'offline', label: '离线' },
  { value: 'maintenance', label: '维护' }, { value: 'pending', label: '待接入' },
];

function tone(row: NodeRow) {
  if (row.status === 'online') return row.connected ? 'ok' : 'danger';
  if (row.status === 'offline') return 'danger';
  if (row.status === 'maintenance') return 'warn';
  return '';
}

// 状态列升序为在线优先、离线最后；控制连接中断的在线节点排在正常在线之后。
function statusRank(row: NodeRow) {
  if (row.status === 'online') return row.connected ? 0 : 1;
  return ({ maintenance: 2, pending: 3, offline: 4 } as Record<string, number>)[row.status] ?? 5;
}

function ago(value: string) {
  const seconds = Math.max(0, (Date.now() - new Date(value).getTime()) / 1000);
  if (seconds < 60) return '刚刚';
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`;
  return new Date(value).toLocaleDateString();
}

export function Nodes({ focus, onSessionExpired }: { focus?: string; onSessionExpired: () => void }) {
  const { message, modal } = App.useApp();
  const [rows, setRows] = useState<NodeRow[]>([]);
  const [selectedIDs, setSelectedIDs] = useState<string[]>([]);
  const [batching, setBatching] = useState<boolean>();
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState<Filter>('all');
  const [adding, setAdding] = useState(false);
  const [saving, setSaving] = useState(false);
  const [installation, setInstallation] = useState<Installation>();
  const [regionNode, setRegionNode] = useState<NodeRow>();
  const [recordNode, setRecordNode] = useState<string>();
  const [rotationNode, setRotationNode] = useState<string>();
  const [selfCheckNode, setSelfCheckNode] = useState<string>();
  const [selfCheckSettings, setSelfCheckSettings] = useState(false);
  const [probeNode, setProbeNode] = useState<string>();
  const [validation, setValidation] = useState<{ id: string; mode: NodeValidationMode }>();
  const [form] = Form.useForm();
  const expired = useRef(onSessionExpired);
  expired.current = onSessionExpired;
  const rowClassName = useFocusRow(focus, loaded);

  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else setError((e as Error).message);
  }

  async function load() {
    setLoading(true);
    try {
      const nodes = (await api<{ nodes: NodeRow[] }>('/nodes')).nodes;
      setRows(nodes); setSelectedIDs(ids => ids.filter(id => nodes.some(row => row.id === id))); setError(''); setLoaded(true);
    }
    catch (e) { failed(e); }
    finally { setLoading(false); }
  }

  const needle = query.trim().toLowerCase();
  const order = useDragOrder(rows, setRows, '/nodes', { filtered: filter !== 'all' || !!needle, onError: failed, reload: load });
  useEffect(() => {
    void load();
    const timer = window.setInterval(() => { if (!order.busy()) void load(); }, 10_000);
    return () => window.clearInterval(timer);
  }, []);

  async function add(values: { name: string }) {
    setSaving(true);
    try {
      const result = await api<Installation>('/nodes', 'POST', values);
      setAdding(false); form.resetFields(); setInstallation(result); await load();
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }

  async function credential(row: NodeRow, operation: 'reset' | 'revoke') {
    try {
      const result = await api<Installation>(`/nodes/${row.id}/token/${operation}`, 'POST');
      if (operation === 'reset') setInstallation(result);
      await load();
    } catch (e) { failed(e); }
  }

  async function maintenance(row: NodeRow) {
    try {
      await api(`/nodes/${row.id}/maintenance`, 'PUT', { enabled: row.status !== 'maintenance' });
      await load();
    } catch (e) { failed(e); }
  }

  // 维护是逐节点状态，没有整批事务；逐个调用后汇总成功与失败。
  async function batchMaintenance(enabled: boolean) {
    setBatching(enabled);
    const targets = rows.filter(row => selectedIDs.includes(row.id));
    const results = await Promise.allSettled(targets.map(row => api(`/nodes/${row.id}/maintenance`, 'PUT', { enabled })));
    const failures = results.flatMap((result, i) => result.status === 'rejected' ? [{ name: targets[i].name, error: result.reason }] : []);
    const ok = targets.length - failures.length;
    const label = enabled ? '维护 10 分钟' : '结束维护';
    if (failures.some(f => f.error instanceof APIError && (f.error.status === 401 || f.error.status === 428))) expired.current();
    else if (failures.length) modal.warning({ title: `${label}：成功 ${ok}，失败 ${failures.length}`, content: failures.map(f => `${f.name}：${(f.error as Error).message}`).join('\n'), styles: { body: { whiteSpace: 'pre-line' } } });
    else message.success(`${label}：已处理 ${ok} 个节点`);
    setBatching(undefined);
    await load();
  }

  function more(row: NodeRow, key: string) {
    if (key === 'region') setRegionNode(row);
    else if (key === 'record') setRecordNode(row.id);
    else if (key === 'rotation') setRotationNode(row.id);
    else if (key === 'maintenance') void maintenance(row);
    else if (key === 'ports' || key === 'delete') setValidation({ id: row.id, mode: key });
    else if (key === 'reset') modal.confirm({ title: `重置「${row.name}」的 token？`, content: '当前控制连接将断开，需要重新执行安装命令。', okText: '重置', onOk: () => credential(row, 'reset') });
    else if (key === 'revoke') modal.confirm({ title: `吊销「${row.name}」的 token？`, content: '当前控制连接将断开，原凭据将失效。', okText: '吊销', okButtonProps: { danger: true }, onOk: () => credential(row, 'revoke') });
  }

  const counts = Object.fromEntries(filters.map(f => [f.value, f.value === 'all' ? rows.length : rows.filter(row => row.status === f.value).length]));
  // 默认按保存的自定义顺序；要让离线排最后，点状态列排序。
  const visible = rows.filter(row => (filter === 'all' || row.status === filter) && (!needle || row.name.toLowerCase().includes(needle) || row.public_ipv4.includes(needle)));
  const validationNode = rows.find(row => row.id === validation?.id);

  return <>
    <PageHeader title="节点" description="控制连接、实时资源与配置版本。每 10 秒刷新。">
      <Button icon={<SettingOutlined />} onClick={() => setSelfCheckSettings(true)}>自测设置</Button>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => setAdding(true)}>添加节点</Button>
    </PageHeader>
    {(error || order.error) && <Alert title={error || order.error} type="error" showIcon className="message" />}
    <div className="toolbar">
      <Input allowClear prefix={<SearchOutlined style={{ color: 'var(--text-3)' }} />} placeholder="搜索名称或 IP" value={query} onChange={e => setQuery(e.target.value)} style={{ width: 240 }} />
      <Segmented<Filter> value={filter} onChange={setFilter} options={filters.map(f => ({ value: f.value, label: <>{f.label} <span className="num muted">{counts[f.value]}</span></> }))} />
      <span className="spacer" />
      {order.restore}
      <Button icon={<ReloadOutlined spin={loading} />} onClick={() => void load()} aria-label="刷新" />
    </div>
    {selectedIDs.length > 0 && <div className="toolbar batch-bar">
      <span>已选 <span className="num">{selectedIDs.length}</span> 个节点</span>
      <Button size="small" loading={batching === true} disabled={batching !== undefined} onClick={() => void batchMaintenance(true)}>维护 10 分钟</Button>
      <Button size="small" loading={batching === false} disabled={batching !== undefined} onClick={() => void batchMaintenance(false)}>结束维护</Button>
      <Button size="small" type="text" onClick={() => setSelectedIDs([])}>取消选择</Button>
    </div>}
    <div className="panel">
      <Table<NodeRow> rowKey="id" dataSource={visible} loading={!loaded && !error} rowSelection={{ selectedRowKeys: selectedIDs, onChange: keys => setSelectedIDs(keys.map(String)) }} onChange={order.onChange} onRow={order.onRow} rowClassName={row => `${rowClassName(row.id)} ${order.rowClass(row.id)}`} pagination={visible.length > 50 ? { pageSize: 50, showSizeChanger: false } : false} scroll={{ x: 1950 }}
        locale={{ emptyText: rows.length ? '没有符合条件的节点' : '暂无节点。添加节点后，把安装命令复制到服务器执行。' }} columns={[
          order.handle,
          { title: '节点', fixed: 'left', width: 190, ...order.sorted('name'), sorter: byText(row => row.name), render: (_, row) => <>
            <span className="status"><span className={`dot ${tone(row)}`} /><span className="node-name">{row.name}</span></span>
            <span className="cell-sub">{row.probe?.uptime_seconds != null ? <>运行 <span className="num">{uptime(row.probe.uptime_seconds)}</span></> : row.software_version ? '—' : '尚未接入'}</span>
          </> },
          { title: '状态', width: 120, ...order.sorted('status'), sorter: (a, b) => statusRank(a) - statusRank(b), render: (_, row) => <>
            {row.status_label}
            {row.status === 'online' && !row.connected && <span className="cell-danger">控制连接已中断</span>}
            {row.status === 'maintenance' && row.maintenance_until && <span className="cell-sub">至 <span className="num">{new Date(row.maintenance_until).toLocaleTimeString()}</span></span>}
          </> },
          { title: 'CPU', width: 130, ...order.sorted('cpu'), sortDirections: ['descend', 'ascend'], sorter: byMetric<NodeRow>(row => row.probe?.cpu_percent), render: (_, row) => <CpuCell probe={row.probe} /> },
          { title: '内存', width: 130, ...order.sorted('memory'), sortDirections: ['descend', 'ascend'], sorter: byMetric<NodeRow>(row => memoryPercent(row.probe)), render: (_, row) => <MemoryCell probe={row.probe} /> },
          { title: '网络', width: 130, ...order.sorted('network'), sortDirections: ['descend', 'ascend'], sorter: byMetric<NodeRow>(row => networkRate(row.probe)), render: (_, row) => <NetworkCell probe={row.probe} /> },
          { title: '连接', width: 80, ...order.sorted('connections'), sortDirections: ['descend', 'ascend'], sorter: byMetric<NodeRow>(row => row.probe?.business_connections), render: (_, row) => <ConnectionsCell probe={row.probe} /> },
          { title: '地区 / IP', width: 170, ...order.sorted('region'), sorter: byText(row => row.region.name), render: (_, row) => <>
            {row.region.name}{row.region_source === 'manual' && <span className="muted">（人工）</span>}
            <span className="cell-sub mono">{row.public_ipv4 || '尚未查询到 IP'}</span>
          </> },
          { title: '域名 / 地址', key: 'record', width: 260, render: (_, row) => <>
            <Button size="small" type="link" onClick={() => setRecordNode(row.id)}>{row.managed_record?.hostname || '绑定域名 / 同步地址'}</Button>
            {row.managed_record && <span className="cell-sub">{row.managed_record.current_enabled ? '启用' : '暂停'} · {row.record_status}</span>}
            <span className="cell-sub mono">期望 {row.confirmed_ipv4 || '地址待确认'}</span>
            {row.managed_record && row.confirmed_ipv4 && row.managed_record.current_value !== row.confirmed_ipv4 && <span className="cell-danger">记录地址等待同步</span>}
            {row.public_ipv4 && row.public_ipv4 !== row.confirmed_ipv4 && <span className="cell-danger">上报与确认地址不一致 <Button size="small" type="link" onClick={() => setRecordNode(row.id)}>同步地址</Button></span>}
          </> },
          { title: '检测点', key: 'checker', width: 100, render: (_, row) => row.domestic_checker ? <span className="status"><span className="dot ok" />{row.checker_label}</span> : <span className="muted">{row.checker_label}</span> },
          { title: '最近活性', dataIndex: 'last_seen_at', width: 110, ...order.sorted('last_seen_at'), sortDirections: ['descend', 'ascend'], sorter: byMetric<NodeRow>(row => row.last_seen_at ? Date.parse(row.last_seen_at) : null), render: (value: string | null) => value ? <span className="num" title={new Date(value).toLocaleString()}>{ago(value)}</span> : <span className="muted">—</span> },
          { title: '配置', key: 'revision', width: 160, render: (_, row) => <>
            <span className={row.config_status === '正常' ? undefined : 'muted'}>{row.config_status}</span>
            <span className="cell-sub mono">已应用 {row.applied_revision} / 期望 {row.desired_revision}</span>
            {row.apply_error && <span className="cell-danger">{row.apply_error}</span>}
          </> },
          { title: '版本', key: 'upgrade', width: 170, render: (_, row) => <>
            {row.software_version && <div className="mono">{row.software_version}</div>}
            <NodeUpgrade node={row} onSaved={load} onError={failed} />
          </> },
          { title: '', key: 'actions', fixed: 'right', width: 130, render: (_, row) => <div className="row-actions">
            <Button size="small" type="text" onClick={() => setProbeNode(row.id)}>探针</Button>
            <Button size="small" type="text" onClick={() => setSelfCheckNode(row.id)}>自测</Button>
            <Dropdown trigger={['click']} placement="bottomRight" menu={{ onClick: ({ key }) => more(row, key), items: [
              { key: 'region', label: '地区与检测点' },
              { key: 'rotation', label: '换 IP' },
              { key: 'record', label: '域名与地址' },
              { key: 'maintenance', label: row.status === 'maintenance' ? '结束维护' : '维护 10 分钟' },
              { key: 'ports', label: '监听端口' },
              { type: 'divider' },
              { key: 'reset', label: '重置 token' },
              { key: 'revoke', label: '吊销 token', danger: true },
              { key: 'delete', label: '删除节点', danger: true },
            ] }}>
              <Button size="small" type="text" icon={<MoreOutlined />} aria-label={`${row.name} 的更多操作`} />
            </Dropdown>
          </div> },
        ]} />
    </div>
    {selfCheckSettings && <SelfCheckSettings onClose={() => setSelfCheckSettings(false)} onError={failed} />}
    {rows.find(row => row.id === selfCheckNode) && <NodeSelfCheck key={selfCheckNode} node={rows.find(row => row.id === selfCheckNode)!} onClose={() => setSelfCheckNode(undefined)} onError={failed} />}
    {rows.find(row => row.id === recordNode) && <NodeManagedRecord key={recordNode} node={rows.find(row => row.id === recordNode)!} onClose={() => setRecordNode(undefined)} onSaved={load} onError={failed} />}
    <NodeRotation node={rows.find(row => row.id === rotationNode)} onClose={() => setRotationNode(undefined)} onError={failed} />
    <NodeRegion node={regionNode} onClose={() => setRegionNode(undefined)} onSaved={load} onError={failed} />
    {rows.find(row => row.id === probeNode) && <ProbeDetails node={rows.find(row => row.id === probeNode)!} onClose={() => setProbeNode(undefined)} onSessionExpired={onSessionExpired} />}
    {validationNode && <NodeValidationActions node={validationNode} mode={validation?.mode} onClose={() => setValidation(undefined)} onSaved={load} onError={failed} />}
    <Modal title="添加节点" open={adding} onCancel={() => { setAdding(false); form.resetFields(); }} footer={null} destroyOnHidden>
      <Form form={form} layout="vertical" onFinish={add} clearOnDestroy>
        <Form.Item name="name" label="节点名称" rules={[{ required: true, whitespace: true, message: '请输入节点名称' }]}><Input autoFocus maxLength={200} placeholder="例如：HK-CN2-01" /></Form.Item>
        <Button block type="primary" htmlType="submit" loading={saving}>生成安装命令</Button>
      </Form>
    </Modal>
    <Modal title="安装节点" open={!!installation} onCancel={() => setInstallation(undefined)} footer={<Button type="primary" onClick={() => setInstallation(undefined)}>完成</Button>} width={760} destroyOnHidden>
      <Typography.Paragraph>以 root 身份在 Linux amd64 服务器执行以下命令（非 root 用户先执行 <code>sudo -i</code>），安装后回到列表确认在线状态。</Typography.Paragraph>
      <Alert type="info" showIcon title="安装票据只能使用一次，30 分钟后失效。" description={installation ? `有效至 ${new Date(installation.ticket_expires_at).toLocaleString()}` : ''} />
      <Typography.Paragraph className="installation-command" copyable={{ text: installation?.installation_command }}><code>{installation?.installation_command}</code></Typography.Paragraph>
    </Modal>
  </>;
}
