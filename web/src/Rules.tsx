import { useEffect, useRef, useState, type Key } from 'react';
import { Alert, App, Button, Dropdown, Form, Input, InputNumber, Modal, Popconfirm, Segmented, Select, Switch, Tooltip, Typography } from 'antd';
import { DeleteOutlined, DownOutlined, InfoCircleOutlined, LinkOutlined, PauseCircleOutlined, PlayCircleOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { RulesTransfer } from './RulesTransfer';
import { go } from './route';
import { PageHeader } from './PageHeader';
import { FilterCard, FilterField } from './FilterCard';
import { TableCard } from './TableCard';
import { StatusDot } from './StatusDot';
import { ColorTag } from './ColorTag';
import { FormSection } from './FormSection';
import { byMetric, byText, useDragOrder } from './ListOrder';
import { PortSyncDetails, PortSyncStatus } from './PortSyncDetails';
import { buildRuleInput, filterRules, ruleLine, sortRuleDetails, type Group, type Rule, type RuleDetailSort, type RuleFilters, type RuleFormValues } from './RulesData';
import './Rules.css';

type BatchAction = 'enable' | 'disable' | 'delete';
const batchLabels: Record<BatchAction, string> = { enable: '启用', disable: '暂停', delete: '删除' };

function bytes(value: number) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let size = value, unit = 0;
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++; }
  return `${unit ? size.toFixed(size < 10 ? 1 : 0) : size} ${units[unit]}`;
}

export function Rules({ focus, onSessionExpired }: { focus?: string; onSessionExpired: () => void }) {
  const { message, modal } = App.useApp();
  const [rows, setRows] = useState<Rule[]>([]);
  const [nodes, setNodes] = useState<Group[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [exitGroups, setExitGroups] = useState<Group[]>([]);
  const [editing, setEditing] = useState<Rule | null>();
  const [error, setError] = useState('');
  const [formError, setFormError] = useState('');
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [selectedIDs, setSelectedIDs] = useState<string[]>([]);
  const [expandedIDs, setExpandedIDs] = useState<Key[]>(focus ? [focus] : []);
  const [batching, setBatching] = useState<BatchAction>();
  const [actingID, setActingID] = useState<string>();
  const [filters, setFilters] = useState<RuleFilters>({});
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [detailSort, setDetailSort] = useState<RuleDetailSort>();
  const [form] = Form.useForm<RuleFormValues>();
  const kind = Form.useWatch('kind', form);
  const line = Form.useWatch('line', form);
  const randomPort = Form.useWatch('random_port', form);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;

  function failed(e: unknown, showError = setError) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else showError((e as Error).message);
  }
  async function load() {
    setLoading(true);
    try {
      const [rules, entryGroups, exits, entries] = await Promise.all([
        api<{ rules: Rule[] }>('/rules'), api<{ entry_groups: Group[] }>('/entry-groups'),
        api<{ exit_groups: Group[] }>('/exit-groups'), api<{ nodes: Group[] }>('/nodes'),
      ]);
      setNodes(entries.nodes); setRows(rules.rules); setGroups(entryGroups.entry_groups); setExitGroups(exits.exit_groups);
      setSelectedIDs(ids => ids.filter(id => rules.rules.some(row => row.id === id)));
      setError(''); setLoaded(true);
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }
  const filtered = !!(filters.search?.trim() || filters.line || filters.kind || filters.enabled !== undefined);
  const visibleRows = sortRuleDetails(filterRules(rows, filters, focus), detailSort);
  const order = useDragOrder(rows, setRows, '/rules', { filtered: filtered || !!focus || !!detailSort, onError: failed, reload: load });
  useEffect(() => {
    void load();
    const timer = window.setInterval(() => { if (!order.busy()) void load(); }, 5000);
    return () => window.clearInterval(timer);
  }, []);
  useEffect(() => {
    setFilters({}); setPage(1); setExpandedIDs(focus ? [focus] : []);
  }, [focus]);

  function edit(row: Rule | null) {
    setFormError(''); form.resetFields();
    form.setFieldsValue(row ? { ...row, line: ruleLine(row), random_port: false } : {
      kind: 'direct', enabled: true, allow_private: false, random_port: false,
    });
    setEditing(row);
  }
  function closeEditor() {
    if (saving) return;
    setEditing(undefined); form.resetFields(); setFormError('');
  }
  async function save(values: RuleFormValues) {
    setSaving(true); setFormError('');
    try {
      await api(editing ? `/rules/${editing.id}` : '/rules', editing ? 'PUT' : 'POST', buildRuleInput(values));
      setEditing(undefined); form.resetFields(); await load();
    } catch (e) { failed(e, setFormError); }
    finally { setSaving(false); }
  }
  async function toggle(row: Rule) {
    setActingID(row.id);
    try {
      await api(`/rules/${row.id}`, 'PUT', buildRuleInput({ ...row, line: ruleLine(row), enabled: !row.enabled }));
      await load();
    } catch (e) { failed(e); }
    finally { setActingID(undefined); }
  }
  async function remove(id: string) {
    setActingID(id);
    try { await api(`/rules/${id}`, 'DELETE'); await load(); }
    catch (e) { failed(e); }
    finally { setActingID(undefined); }
  }
  // 整批一次提交：只重新编译一次配置，任一条失败则整批不生效。
  async function batch(action: BatchAction) {
    const ids = [...selectedIDs];
    setBatching(action);
    try {
      await api('/rules/batch', 'POST', { action, ids });
      message.success(`已${batchLabels[action]} ${ids.length} 条端口转发`);
      if (action === 'delete') setSelectedIDs([]);
      await load();
    } catch (e) { failed(e); }
    finally { setBatching(undefined); }
  }
  function confirmBatch(action: BatchAction) {
    if (action === 'enable') { void batch(action); return; }
    modal.confirm({
      title: `${batchLabels[action]}所选 ${selectedIDs.length} 条端口转发？`,
      content: `会断开所有所选端口的现有连接。${action === 'delete' ? '删除后不可恢复。' : ''}`,
      okText: `确认${batchLabels[action]}`, okButtonProps: { danger: action === 'delete' },
      onOk: () => batch(action),
    });
  }
  function toggleDetails(id: string) {
    setExpandedIDs(ids => ids.includes(id) ? ids.filter(key => key !== id) : [...ids, id]);
  }
  function updateFilters(next: Partial<RuleFilters>) {
    setFilters(current => ({ ...current, ...next })); setPage(1);
  }
  const lineOptions = [
    { label: '服务器', options: nodes.map(node => ({ value: `node:${node.id}`, label: node.name })) },
    { label: '服务器组', options: groups.map(group => ({ value: `group:${group.id}`, label: group.name })) },
  ];
  const entryName = (row: Rule) => row.entry_node_id
    ? nodes.find(node => node.id === row.entry_node_id)?.name ?? row.entry_node_id
    : groups.find(group => group.id === row.entry_group_id)?.name ?? row.entry_group_id ?? '';
  const mutating = !!batching || !!actingID;

  return <div className="ports-page">
    <PageHeader title="端口管理" description="管理线路、服务端口与转发目标；运行数据每 5 秒刷新。">
      <RulesTransfer selectedIDs={selectedIDs} onChanged={load} onSessionExpired={onSessionExpired} />
      <Dropdown trigger={['click']} disabled={!selectedIDs.length || mutating} menu={{
        items: [
          { key: 'enable', label: '批量启用', icon: <PlayCircleOutlined /> },
          { key: 'disable', label: '批量暂停', icon: <PauseCircleOutlined /> },
          { type: 'divider' },
          { key: 'delete', label: '批量删除', icon: <DeleteOutlined />, danger: true },
        ],
        onClick: ({ key }) => confirmBatch(key as BatchAction),
      }}><Button disabled={!selectedIDs.length || mutating} loading={!!batching}>批量操作{selectedIDs.length > 0 && ` (${selectedIDs.length})`} <DownOutlined /></Button></Dropdown>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => edit(null)}>添加端口转发</Button>
    </PageHeader>

    {(error || order.error) && <Alert title={error || order.error} type="error" showIcon className="message" />}
    {focus && <Alert title={rows.some(row => row.id === focus) ? '已定位关联端口，展开行可查看成员生效版本。' : '关联端口已删除或尚未读取。'}
      action={<Button onClick={() => go('ports')}>查看全部端口</Button>} type="info" showIcon className="message" />}

    <FilterCard loading={loading} onRefresh={() => { if (!order.busy()) void load(); }}>
      <FilterField label="名称" htmlFor="ports-search"><Input id="ports-search" value={filters.search ?? ''} allowClear prefix={<SearchOutlined />} placeholder="搜索端口名称 / 目标" onChange={event => updateFilters({ search: event.target.value })} /></FilterField>
      <FilterField label="线路" htmlFor="ports-line"><Select id="ports-line" value={filters.line} allowClear showSearch optionFilterProp="label" placeholder="全部线路" options={lineOptions} onChange={value => updateFilters({ line: value })} /></FilterField>
      <FilterField label="模式" htmlFor="ports-mode"><Select id="ports-mode" value={filters.kind} allowClear placeholder="全部模式" options={[{ value: 'direct', label: '直连' }, { value: 'tunnel', label: '单跳转发' }]} onChange={value => updateFilters({ kind: value })} /></FilterField>
      <FilterField label="状态" htmlFor="ports-status"><Select id="ports-status" value={filters.enabled === undefined ? undefined : filters.enabled ? 'enabled' : 'paused'} allowClear placeholder="全部状态" options={[{ value: 'enabled', label: '已启用' }, { value: 'paused', label: '已暂停' }]} onChange={value => updateFilters({ enabled: value === undefined ? undefined : value === 'enabled' })} /></FilterField>
      {filtered && <Button type="text" onClick={() => { setFilters({}); setPage(1); }}>重置筛选</Button>}
    </FilterCard>

    <TableCard<Rule> rowKey="id" dataSource={visibleRows} actions={<>
      {selectedIDs.length > 0 && <><span className="ports-selection">已选 <b className="num">{selectedIDs.length}</b> 条（含其他页及筛选外的端口）</span><Button type="text" disabled={mutating} onClick={() => setSelectedIDs([])}>取消选择</Button></>}
      <Select aria-label="ID 与同步状态排序" placeholder="更多排序" allowClear value={detailSort} options={[
        { value: 'id-ascend', label: 'ID 升序' }, { value: 'id-descend', label: 'ID 降序' },
        { value: 'sync-ascend', label: '同步状态升序' }, { value: 'sync-descend', label: '同步状态降序' },
      ]} onChange={value => { order.onChange(undefined, undefined, {}); setDetailSort(value); setPage(1); }} />
      {detailSort ? <Button onClick={() => setDetailSort(undefined)}>恢复自定义顺序</Button> : order.restore}
    </>}
      rowSelection={{ fixed: true, columnWidth: 30, selectedRowKeys: selectedIDs, preserveSelectedRowKeys: true, onChange: keys => setSelectedIDs(keys.map(String)), getCheckboxProps: () => ({ disabled: mutating }) }}
      onChange={(pagination, tableFilters, sorter, extra) => { if (extra.action === 'sort') setDetailSort(undefined); setPage(pagination.current ?? 1); setPageSize(pagination.pageSize ?? 20); order.onChange(pagination, tableFilters, sorter); }}
      onRow={order.onRow} rowClassName={row => order.rowClass(row.id)} loading={!loaded && !error}
      pagination={{ current: Math.max(1, Math.min(page, Math.ceil(visibleRows.length / pageSize))), pageSize, showSizeChanger: true, pageSizeOptions: [20, 50, 100] }}
      scroll={{ x: 1080 }} locale={{ emptyText: filtered || focus ? '没有符合条件的端口。' : '暂无端口，请先接入服务器，再添加端口转发。' }}
      expandable={{ fixed: 'left', columnWidth: 30, expandedRowKeys: expandedIDs, onExpandedRowsChange: keys => setExpandedIDs([...keys]), expandedRowRender: row => <PortSyncDetails members={row.sync_members} /> }} columns={[
        { ...order.handle, width: 28 },
        { title: '名称 / ID', dataIndex: 'name', fixed: 'left', width: 146, ...order.sorted('name'), sorter: byText(row => row.name), render: (value, row) => <><span className="node-name ports-name">{value}</span><span className="cell-sub"><Typography.Text className="ports-id mono" copyable={{ text: row.id }}><Tooltip title={row.id}><span>{row.id.length > 8 ? `${row.id.slice(0, 8)}…` : row.id}</span></Tooltip></Typography.Text></span></> },
        { title: '线路', width: 100, ...order.sorted('entry'), sorter: byText(entryName), render: (_, row) => <><span className="ports-name">{entryName(row)}</span><span className="cell-sub">{row.entry_node_id ? '服务器' : '服务器组'}</span></> },
        { title: '服务端口', dataIndex: 'listen_port', width: 88, ...order.sorted('listen_port'), sorter: (a, b) => a.listen_port - b.listen_port, render: value => <span className="mono">{value}</span> },
        { title: '目标', width: 140, ...order.sorted('target'), sorter: byText(row => `${row.target_address}:${row.target_port}`), render: (_, row) => <><span className="mono ports-target">{row.target_address}:{row.target_port}</span><span className="cell-sub">{row.allow_private ? '允许私网目标' : '仅公网目标'}</span></> },
        { title: '转发模式', width: 100, render: (_, row) => <><ColorTag tone={row.kind === 'tunnel' ? 'blue' : 'neutral'}>{row.kind === 'tunnel' ? '单跳转发' : '直连'}</ColorTag>{row.kind === 'tunnel' && <span className="cell-sub ports-name">{exitGroups.find(group => group.id === row.exit_group_id)?.name ?? row.exit_group_id}</span>}</> },
        { title: '状态 / 同步', width: 100, ...order.sorted('enabled'), sorter: (a, b) => Number(b.enabled) - Number(a.enabled), render: (_, row) => <><StatusDot tone={row.enabled ? 'ok' : 'warn'}>{row.enabled ? '已启用' : '已暂停'}</StatusDot><span className="cell-sub"><PortSyncStatus status={row.sync_status} /></span></> },
        { title: '流量', width: 96, ...order.sorted('traffic'), sortDirections: ['descend', 'ascend'], sorter: byMetric<Rule>(row => row.traffic ? row.traffic.upload_bytes + row.traffic.download_bytes : null), render: (_, row) => <span className="num ports-traffic" title={`上行 ${(row.traffic?.upload_bytes ?? 0).toLocaleString()} B，下行 ${(row.traffic?.download_bytes ?? 0).toLocaleString()} B`}>↑ {bytes(row.traffic?.upload_bytes ?? 0)}<span className="cell-sub">↓ {bytes(row.traffic?.download_bytes ?? 0)}</span></span> },
        { title: '连接', width: 70, ...order.sorted('connections'), sortDirections: ['descend', 'ascend'], sorter: byMetric<Rule>(row => row.traffic?.active_connections), render: (_, row) => row.traffic?.active_connections == null ? <span className="muted">缺测</span> : <span className="num">{row.traffic.active_connections.toLocaleString()}</span> },
        { title: '操作', key: 'actions', fixed: 'right', width: 152, align: 'right', render: (_, row) => <div className="ports-row-actions">
          <Button size="small" type="link" disabled={mutating} onClick={() => edit(row)}>编辑</Button>
          <Popconfirm title={row.enabled ? '暂停该端口并断开所有现有连接？' : '启用该端口？'} onConfirm={() => toggle(row)}><Button size="small" type="text" disabled={mutating} loading={actingID === row.id}>{row.enabled ? '暂停' : '启用'}</Button></Popconfirm>
          <Dropdown trigger={['click']} menu={{ items: [
            { key: 'sync', label: expandedIDs.includes(row.id) ? '收起成员生效版本' : '查看成员生效版本' },
            { type: 'divider' }, { key: 'delete', label: '删除端口', icon: <DeleteOutlined />, danger: true, disabled: mutating },
          ], onClick: ({ key }) => {
            if (key === 'sync') toggleDetails(row.id);
            if (key === 'delete') modal.confirm({ title: `删除端口「${row.name}」？`, content: '会断开该端口的所有现有连接，删除后不可恢复。', okText: '确认删除', okButtonProps: { danger: true }, onOk: () => remove(row.id) });
          } }}><Button size="small" type="text" aria-label={`端口 ${row.name} 的更多操作`}>更多 <DownOutlined /></Button></Dropdown>
        </div> },
      ]} />

    <div className="ports-notes">
      <p>展开行或使用「更多 → 查看成员生效版本」，核对成员服务器的期望版本、实际生效版本与应用错误。离线服务器恢复控制连接后才能应用改动。</p>
      <p>同步状态按成员确认结果汇总；部分端口应用成功时，服务器的已应用版本仍保留在上一次完整成功的版本。已暂停端口显示「正常」，表示服务器已确认撤销监听。</p>
      <p>流量仅计入口侧业务字节；↑ 为用户到目标，↓ 为目标到用户。离线或样本过期时连接数显示「缺测」。清除排序、筛选和关联定位后，可拖动左侧手柄保存自定义顺序。</p>
    </div>

    <Modal className="ports-editor" title={editing ? '编辑端口转发' : '添加端口转发'} open={editing !== undefined} width={760} onCancel={closeEditor} closable={!saving} maskClosable={!saving} keyboard={!saving} forceRender
      footer={<><Button disabled={saving} onClick={closeEditor}>取消</Button><Button type="primary" loading={saving} onClick={() => form.submit()}>保存端口</Button></>}>
      <Form form={form} layout="horizontal" labelCol={{ flex: '120px' }} wrapperCol={{ flex: 1 }} labelWrap colon={false} onFinish={save} disabled={saving}>
        {formError && <Alert title={formError} type="error" showIcon className="message" />}
        <FormSection title="基本信息" icon={<InfoCircleOutlined />}>
          <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: '请输入端口名称' }]}><Input placeholder="请输入端口名称" maxLength={200} /></Form.Item>
          <Form.Item name="line" label="线路" rules={[{ required: true, message: '请选择服务器或服务器组' }]} extra={line?.startsWith('group:') ? '使用服务器组作为入口线路，成员生效情况可在列表展开查看。' : '客户端连接此服务器的地址；单服务器线路没有入口切换。'}><Select showSearch optionFilterProp="label" placeholder="选择服务器或服务器组" options={lineOptions} /></Form.Item>
          {!editing && <Form.Item name="random_port" label="自动分配端口" valuePropName="checked" extra="保存时从 1024–65535 中分配一个在实际入口服务器配置中均空闲的服务端口，之后固定使用。"><Switch /></Form.Item>}
          {!randomPort && <Form.Item name="listen_port" label="服务端口" preserve={false} rules={[{ required: true, message: '请输入服务端口' }]} extra="客户端连接此 TCP 端口。"><InputNumber min={1} max={65535} precision={0} placeholder="1–65535" /></Form.Item>}
          <Form.Item name="target_address" label="目标地址" rules={[{ required: true, whitespace: true, message: '请输入目标地址' }]} extra="填写单个 IPv4 地址或域名。"><Input placeholder="例如：203.0.113.10 或 target.example.com" maxLength={253} /></Form.Item>
          <Form.Item name="target_port" label="目标端口" rules={[{ required: true, message: '请输入目标端口' }]}><InputNumber min={1} max={65535} precision={0} placeholder="1–65535" /></Form.Item>
        </FormSection>
        <FormSection title="转发配置" icon={<LinkOutlined />}>
          <Form.Item name="kind" label="转发模式" rules={[{ required: true }]} extra="直连：入口服务器直接连接目标；单跳转发：经端点组中的转发端点连接目标。"><Segmented options={[{ value: 'direct', label: '直连' }, { value: 'tunnel', label: '单跳转发' }]} /></Form.Item>
          {kind === 'tunnel' && <Form.Item name="exit_group_id" label="端点组" preserve={false} rules={[{ required: true, message: '请选择端点组' }]} extra="域名目标由转发端点解析。端点组不可用时，新连接失败。"><Select showSearch optionFilterProp="label" placeholder="选择端点组" options={exitGroups.map(group => ({ value: group.id, label: group.name }))} /></Form.Item>}
          <Form.Item name="allow_private" label="允许私网目标" valuePropName="checked" extra="域名解析到私网、回环或链路本地地址时，同样需要显式允许。"><Switch /></Form.Item>
          <Form.Item name="enabled" label="启用端口" valuePropName="checked"><Switch /></Form.Item>
        </FormSection>
        {editing && <Alert type="info" showIcon title="修改转发目标或服务端口等配置，会断开此端口已有的连接。" />}
      </Form>
    </Modal>
  </div>;
}
