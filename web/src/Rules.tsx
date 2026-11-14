import { useEffect, useRef, useState, type Key } from 'react';
import { Alert, App, Button, Dropdown, Form, Input, InputNumber, Modal, Segmented, Select, Switch, Tooltip, Typography } from 'antd';
import { CloseCircleOutlined, DeleteOutlined, DownOutlined, ExclamationCircleOutlined, InfoCircleOutlined, LinkOutlined, PauseCircleOutlined, PlayCircleOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { RulesTransfer } from './RulesTransfer';
import { go, pageTitles } from './route';
import { PageHeader } from './PageHeader';
import { FilterCard, FilterField } from './FilterCard';
import { TableCard } from './TableCard';
import { StatusDot } from './StatusDot';
import { ColorTag } from './ColorTag';
import { FormSection } from './FormSection';
import { byMetric, byText, useDragOrder } from './ListOrder';
import { PortSyncDetails } from './PortSyncDetails';
import { buildRuleInput, configurationStatusTone, createRuleReloader, filterRules, ruleFormValues, ruleMode, ruleModeLabels, type Group, type PathNode, type Rule, type RuleFilters, type RuleFormValues } from './RulesData';
import type { ForwardChain } from './ChainsData';
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
  const [nodes, setNodes] = useState<PathNode[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [chains, setChains] = useState<ForwardChain[]>([]);
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
  const [form] = Form.useForm<RuleFormValues>();
  const mode = Form.useWatch('mode', form);
  const selectedChain = Form.useWatch('chain_id', form);
  const line = Form.useWatch('line', form);
  const randomPort = Form.useWatch('random_port', form);
  const reloader = useRef<ReturnType<typeof createRuleReloader> | undefined>(undefined);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;

  function failed(e: unknown, showError = setError) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else showError((e as Error).message);
  }
  function load() { return reloader.current?.refresh() ?? Promise.resolve(); }
  const filtered = !!(filters.search?.trim() || filters.line || filters.mode || filters.enabled !== undefined);
  const visibleRows = filterRules(rows, filters, focus);
  const order = useDragOrder(rows, setRows, '/rules', { filtered: filtered || !!focus, onError: failed, reload: load });
  useEffect(() => {
    const current = createRuleReloader(() => Promise.all([
      api<{ rules: Rule[] }>('/rules'), api<{ entry_groups: Group[] }>('/entry-groups'),
      api<{ exit_groups: Group[] }>('/exit-groups'), api<{ nodes: PathNode[] }>('/nodes'),
      api<{ chains: ForwardChain[] }>('/forward-chains'),
    ]), {
      apply: ([rules, entryGroups, exits, entries, paths]) => {
        setChains(paths.chains); setNodes(entries.nodes); setRows(rules.rules); setGroups(entryGroups.entry_groups); setExitGroups(exits.exit_groups);
        setSelectedIDs(ids => ids.filter(id => rules.rules.some(row => row.id === id)));
        setError(''); setLoaded(true);
      },
      failed, loading: setLoading,
    });
    reloader.current = current;
    void current.refresh();
    const timer = window.setInterval(() => { if (!order.busy()) current.poll(); }, 5000);
    return () => {
      current.dispose();
      if (reloader.current === current) reloader.current = undefined;
      window.clearInterval(timer);
    };
  }, []);
  useEffect(() => {
    setFilters({}); setPage(1); setExpandedIDs(focus ? [focus] : []);
  }, [focus]);

  function edit(row: Rule | null, copy = false) {
    setFormError(''); form.resetFields();
    form.setFieldsValue(row ? ruleFormValues(row) : {
      mode: 'direct', enabled: true, allow_private: false, random_port: false,
    });
    setEditing(copy ? null : row);
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
      await api(`/rules/${row.id}`, 'PUT', buildRuleInput({ ...ruleFormValues(row), enabled: !row.enabled }));
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
    <PageHeader title={pageTitles.ports} description="管理线路、服务端口与转发目标；运行数据每 5 秒刷新。">
      <RulesTransfer selectedIDs={selectedIDs} nodes={nodes} groups={groups} exitGroups={exitGroups} chains={chains} onChanged={load} onSessionExpired={onSessionExpired} />
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
    {focus && <Alert title={rows.some(row => row.id === focus) ? '已定位关联端口，完整路径与同步明细已自动打开。' : '关联端口已删除或尚未读取。'}
      action={<Button onClick={() => go('ports')}>查看全部端口</Button>} type="info" showIcon className="message" />}

    <FilterCard loading={loading} onRefresh={() => { if (!order.busy()) void load(); }}>
      <FilterField label="名称" htmlFor="ports-search"><Input id="ports-search" value={filters.search ?? ''} allowClear prefix={<SearchOutlined />} placeholder="搜索端口名称 / 目标" onChange={event => updateFilters({ search: event.target.value })} /></FilterField>
      <FilterField label="线路" htmlFor="ports-line"><Select id="ports-line" value={filters.line} allowClear showSearch optionFilterProp="label" placeholder="全部线路" options={lineOptions} onChange={value => updateFilters({ line: value })} /></FilterField>
      <FilterField label="模式" htmlFor="ports-mode"><Select id="ports-mode" value={filters.mode} allowClear placeholder="全部模式" options={Object.entries(ruleModeLabels).map(([value, label]) => ({ value, label }))} onChange={value => updateFilters({ mode: value })} /></FilterField>
      <FilterField label="状态" htmlFor="ports-status"><Select id="ports-status" value={filters.enabled === undefined ? undefined : filters.enabled ? 'enabled' : 'paused'} allowClear placeholder="全部状态" options={[{ value: 'enabled', label: '运行中' }, { value: 'paused', label: '已暂停' }]} onChange={value => updateFilters({ enabled: value === undefined ? undefined : value === 'enabled' })} /></FilterField>
      {filtered && <Button type="text" onClick={() => { setFilters({}); setPage(1); }}>重置筛选</Button>}
    </FilterCard>

    <TableCard<Rule> rowKey="id" dataSource={visibleRows} actions={selectedIDs.length > 0 || order.restore ? <>
      {selectedIDs.length > 0 && <><span className="ports-selection">已选 <b className="num">{selectedIDs.length}</b> 条（含其他页及筛选外的端口）</span><Button type="text" disabled={mutating} onClick={() => setSelectedIDs([])}>取消选择</Button></>}
      {order.restore}
    </> : undefined}
      rowSelection={{ fixed: true, columnWidth: 30, selectedRowKeys: selectedIDs, preserveSelectedRowKeys: true, onChange: keys => setSelectedIDs(keys.map(String)), getCheckboxProps: () => ({ disabled: mutating }) }}
      onChange={(pagination, tableFilters, sorter) => { setPage(pagination.current ?? 1); setPageSize(pagination.pageSize ?? 20); order.onChange(pagination, tableFilters, sorter); }}
      onRow={order.onRow} classNames={{ header: { row: 'ports-table-header' } }} rowClassName={row => `ports-record ${order.rowClass(row.id)}`} loading={!loaded && !error}
      pagination={{ current: Math.max(1, Math.min(page, Math.ceil(visibleRows.length / pageSize))), pageSize, showSizeChanger: true, pageSizeOptions: [10, 20, 50, 100] }}
      scroll={{ x: 968 }} locale={{ emptyText: filtered || focus ? '没有符合条件的端口。' : '暂无端口，请先接入服务器，再添加端口转发。' }}
      expandable={{ showExpandColumn: false, expandedRowKeys: expandedIDs, onExpandedRowsChange: keys => setExpandedIDs([...keys]), expandedRowRender: row => <PortSyncDetails rule={row} entryName={entryName(row)} chains={chains} nodes={nodes} /> }} columns={[
        { ...order.handle, width: 24, className: 'ports-drag-cell', onCell: () => ({ tabIndex: 0, 'aria-label': '拖动调整顺序' }) },
        { title: '名称', dataIndex: 'name', fixed: 'left', width: 136, ...order.sorted('name'), sorter: byText(row => row.name), render: (value, row) => <span className="ports-name-cell"><Tooltip title={value}><span className="node-name ports-name">{value}</span></Tooltip><Typography.Text className="ports-id" copyable={{ text: row.id, tooltips: [`复制端口 ID：${row.id}`, '已复制'] }} /></span> },
        { title: '线路', width: 100, render: (_, row) => <Tooltip title={`${row.entry_node_id ? '服务器' : '服务器组'}：${entryName(row)}`}><span className="ports-name">{entryName(row)}</span></Tooltip> },
        { title: '服务端口', dataIndex: 'listen_port', width: 88, ...order.sorted('listen_port'), sorter: (a, b) => a.listen_port - b.listen_port, render: value => <span className="mono">{value}</span> },
        { title: '目标', width: 144, render: (_, row) => <Tooltip title={`${row.target_address}:${row.target_port}`}><span className="mono ports-target">{row.target_address}:{row.target_port}</span></Tooltip> },
        { title: '转发模式', width: 92, render: (_, row) => <Tooltip title={row.chain_id ? `转发链：${chains.find(chain => chain.id === row.chain_id)?.name ?? `引用待确认 · ${row.chain_id}`}` : row.exit_group_id ? `端点组：${exitGroups.find(group => group.id === row.exit_group_id)?.name ?? row.exit_group_id}` : '入口直接连接目标'}><span className="ports-mode">{row.chain_id ? <Button className="ports-chain-link" size="small" type="link" onClick={() => go('chains', row.chain_id)}><ColorTag tone="purple">{ruleModeLabels.chain}</ColorTag></Button> : <ColorTag tone={ruleMode(row) === 'single' ? 'blue' : 'neutral'}>{ruleModeLabels[ruleMode(row)]}</ColorTag>}</span></Tooltip> },
        { title: '状态', width: 98, render: (_, row) => { const tone = configurationStatusTone(row.sync_status); return <span className="ports-status"><StatusDot tone={row.enabled ? 'ok' : 'warn'}>{row.enabled ? '运行中' : '已暂停'}</StatusDot>{tone !== 'ok' && <Tooltip title={row.sync_status || '未同步'}><span className={`ports-sync-alert ${tone}`} role="img" aria-label={`同步状态：${row.sync_status || '未同步'}`}>{tone === 'danger' ? <CloseCircleOutlined /> : <ExclamationCircleOutlined />}</span></Tooltip>}</span>; } },
        { title: '流量', width: 80, ...order.sorted('traffic'), sortDirections: ['descend', 'ascend'], sorter: byMetric<Rule>(row => row.traffic ? row.traffic.upload_bytes + row.traffic.download_bytes : null), render: (_, row) => <Tooltip title={`上行 ${bytes(row.traffic?.upload_bytes ?? 0)}，下行 ${bytes(row.traffic?.download_bytes ?? 0)}`}><span className="num ports-traffic">{bytes((row.traffic?.upload_bytes ?? 0) + (row.traffic?.download_bytes ?? 0))}</span></Tooltip> },
        { title: '连接', width: 64, ...order.sorted('connections'), sortDirections: ['descend', 'ascend'], sorter: byMetric<Rule>(row => row.traffic?.active_connections), render: (_, row) => row.traffic?.active_connections == null ? <span className="muted">缺测</span> : <span className="num">{row.traffic.active_connections.toLocaleString()}</span> },
        { title: '操作', key: 'actions', fixed: 'right', width: 112, align: 'right', render: (_, row) => <div className="ports-row-actions">
          <Button size="small" type="link" disabled={mutating} onClick={() => edit(row)}>编辑</Button>
          <Dropdown trigger={['click']} menu={{ items: [
            { key: 'toggle', label: row.enabled ? '暂停' : '启用', icon: row.enabled ? <PauseCircleOutlined /> : <PlayCircleOutlined />, disabled: mutating },
            { key: 'copy', label: '复制端口定义', disabled: mutating },
            { key: 'sync', label: expandedIDs.includes(row.id) ? '收起完整路径与同步' : '查看完整路径与同步' },
            { type: 'divider' }, { key: 'delete', label: '删除端口', icon: <DeleteOutlined />, danger: true, disabled: mutating },
          ], onClick: ({ key }) => {
            if (key === 'toggle') modal.confirm({ title: row.enabled ? '暂停该端口并断开所有现有连接？' : '启用该端口？', okText: row.enabled ? '确认暂停' : '确认启用', cancelText: '取消', onOk: () => toggle(row) });
            if (key === 'copy') edit(row, true);
            if (key === 'sync') toggleDetails(row.id);
            if (key === 'delete') modal.confirm({ title: `删除端口「${row.name}」？`, content: '会断开该端口的所有现有连接，删除后不可恢复。', okText: '确认删除', okButtonProps: { danger: true }, onOk: () => remove(row.id) });
          } }}><Button size="small" type="text" loading={actingID === row.id} aria-label={`端口 ${row.name} 的更多操作`}>更多 <DownOutlined /></Button></Dropdown>
        </div> },
      ]} />

    <div className="ports-notes">
      <p>使用「更多 → 查看完整路径与同步」，核对完整路径、私网策略、成员服务器的期望版本、实际生效版本与应用错误。离线服务器恢复控制连接后才能应用改动。</p>
      <p>同步异常时，状态旁显示提示图标，悬停查看原因；同步明细按成员确认结果汇总。部分端口应用成功时，服务器的已应用版本仍保留在上一次完整成功的版本；明细中已暂停端口的同步状态为「正常」，表示服务器已确认撤销监听。</p>
      <p>流量仅计入口侧业务字节，合计显示，悬停查看上下行；上行为用户到目标，下行为目标到用户。离线或样本过期时连接数显示「缺测」。清除排序、筛选和关联定位后，悬停行左侧出现拖动手柄，可拖动保存自定义顺序。</p>
    </div>

    <Modal className="ports-editor" title={editing ? '编辑端口转发' : '添加端口转发'} open={editing !== undefined} width={760} onCancel={closeEditor} closable={!saving} keyboard={!saving} forceRender
      footer={<><Button disabled={saving} onClick={closeEditor}>取消</Button><Button type="primary" loading={saving} onClick={() => form.submit()}>保存端口</Button></>}>
      <Form form={form} layout="horizontal" labelCol={{ flex: '120px' }} wrapperCol={{ flex: 1 }} labelWrap colon={false} onFinish={save} disabled={saving}>
        {formError && <Alert title={formError} type="error" showIcon className="message" />}
        <FormSection title="基本信息" icon={<InfoCircleOutlined />}>
          <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: '请输入端口名称' }]}><Input placeholder="请输入端口名称" maxLength={200} /></Form.Item>
          <Form.Item name="line" label="线路" rules={[{ required: true, message: '请选择服务器或服务器组' }]} extra={line?.startsWith('group:') ? '使用服务器组作为入口线路，成员生效情况可通过「更多 → 查看完整路径与同步」核对。' : '客户端连接此服务器的地址；单服务器线路没有入口切换。'}><Select showSearch optionFilterProp="label" placeholder="选择服务器或服务器组" options={lineOptions} /></Form.Item>
          {!editing && <Form.Item name="random_port" label="自动分配端口" valuePropName="checked" extra="保存时从 1024–65535 中分配一个在实际入口服务器配置中均空闲的服务端口，之后固定使用。"><Switch /></Form.Item>}
          {!randomPort && <Form.Item name="listen_port" label="服务端口" preserve={false} rules={[{ required: true, message: '请输入服务端口' }]} extra="客户端连接此 TCP 端口。"><InputNumber min={1} max={65535} precision={0} placeholder="1–65535" /></Form.Item>}
          <Form.Item name="target_address" label="目标地址" rules={[{ required: true, whitespace: true, message: '请输入目标地址' }]} extra="填写单个 IPv4 地址或域名。"><Input placeholder="例如：203.0.113.10 或 target.example.com" maxLength={253} /></Form.Item>
          <Form.Item name="target_port" label="目标端口" rules={[{ required: true, message: '请输入目标端口' }]}><InputNumber min={1} max={65535} precision={0} placeholder="1–65535" /></Form.Item>
        </FormSection>
        <FormSection title="转发配置" icon={<LinkOutlined />}>
          <Form.Item name="mode" label="转发模式" rules={[{ required: true }]} extra="直连由入口连目标；单跳经一个端点组；链式按已保存链逐跳转发，最终目标由末跳解析。"><Segmented options={Object.entries(ruleModeLabels).map(([value, label]) => ({ value, label }))} onChange={() => form.setFieldsValue({ exit_group_id: undefined, chain_id: undefined })} /></Form.Item>
          {mode === 'single' && <Form.Item name="exit_group_id" label="端点组" preserve={false} rules={[{ required: true, message: '请选择端点组' }]} extra="域名目标由转发端点解析。端点组不可用时，新连接失败。"><Select showSearch optionFilterProp="label" placeholder="选择端点组" options={exitGroups.map(group => ({ value: group.id, label: group.name }))} /></Form.Item>}
          {mode === 'chain' && <>
            <Form.Item name="chain_id" label="转发链" preserve={false} rules={[{ required: true, message: '请选择已保存的转发链' }]} extra="链由多个端口共享；编辑链会同时影响全部引用端口，不为本端口复制私有路径。">
              <Select showSearch optionFilterProp="label" placeholder="选择已保存的转发链" options={[
                ...chains.map(chain => ({ value: chain.id, label: `${chain.name}（${chain.rule_count} 个引用端口）` })),
                ...(selectedChain && !chains.some(chain => chain.id === selectedChain) ? [{ value: selectedChain, label: `引用暂时缺失 · ${selectedChain}` }] : []),
              ]} />
            </Form.Item>
            {selectedChain && !chains.some(chain => chain.id === selectedChain) && <Alert type="warning" showIcon title="链引用暂时缺失，已保留原引用。请刷新或重新选择；保存时由服务端验证，模式不会自动变为直连。" />}
            <p><Button type="link" onClick={() => go('chains', selectedChain)}>{chains.length ? '管理 / 编辑共享转发链' : '尚无转发链，前往创建至少两跳的链'}</Button></p>
          </>}
          <Form.Item name="allow_private" label="允许私网目标" valuePropName="checked" extra="域名解析到私网、回环或链路本地地址时，同样需要显式允许。"><Switch /></Form.Item>
          <Form.Item name="enabled" label="启用端口" valuePropName="checked"><Switch /></Form.Item>
        </FormSection>
        {editing && <Alert type="info" showIcon title="修改转发目标或服务端口等配置，会断开此端口已有的连接。" />}
      </Form>
    </Modal>
  </div>;
}
