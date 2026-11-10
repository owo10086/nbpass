import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Switch, Table, Tag } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { RuleSyncDetails, SyncHelp, SyncTag, type SyncMember } from './ConfigSync';
import { RulesTransfer } from './RulesTransfer';
import { PageHeader } from './PageHeader';

type RuleInput = { name: string; entry_group_id?: string; entry_node_id?: string; kind: 'direct' | 'tunnel'; exit_group_id?: string; listen_port: number; target_address: string; target_family: 'ipv4'; target_port: number; allow_private: boolean; enabled: boolean };
type Rule = RuleInput & { id: string; sync_status: string; sync_members: SyncMember[]; traffic?: { upload_bytes: number; download_bytes: number; active_connections: number | null } };
type Group = { id: string; name: string };

function bytes(value: number) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let size = value, unit = 0;
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++; }
  return `${unit ? size.toFixed(size < 10 ? 1 : 0) : size} ${units[unit]}`;
}

function ruleInput(row: RuleInput): RuleInput {
  const { name, entry_group_id, entry_node_id, kind, exit_group_id, listen_port, target_address, target_family, target_port, allow_private, enabled } = row;
  return { name, entry_group_id, entry_node_id, kind, exit_group_id, listen_port, target_address, target_family, target_port, allow_private, enabled };
}

export function Rules({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [rows, setRows] = useState<Rule[]>([]);
  const [nodes, setNodes] = useState<Group[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [exitGroups, setExitGroups] = useState<Group[]>([]);
  const [editing, setEditing] = useState<Rule | null>();
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [selectedIDs, setSelectedIDs] = useState<string[]>([]);
  const [form] = Form.useForm();
  const entryType = Form.useWatch('entry_type', form);
  const kind = Form.useWatch('kind', form);
  const randomPort = Form.useWatch('random_port', form);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else setError((e as Error).message);
  }
  async function load() {
    setLoading(true);
    try {
      const [rules, entryGroups, exits, entries] = await Promise.all([api<{ rules: Rule[] }>('/rules'), api<{ entry_groups: Group[] }>('/entry-groups'), api<{ exit_groups: Group[] }>('/exit-groups'), api<{ nodes: Group[] }>('/nodes')]);
      setNodes(entries.nodes); setRows(rules.rules); setSelectedIDs(ids => ids.filter(id => rules.rules.some(row => row.id === id))); setGroups(entryGroups.entry_groups); setExitGroups(exits.exit_groups); setError(''); setLoaded(true);
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }
  useEffect(() => { void load(); const timer = window.setInterval(() => void load(), 5000); return () => window.clearInterval(timer); }, []);
  function edit(row: Rule | null) {
    setError(''); form.resetFields(); form.setFieldsValue(row ? { ...row, entry_type: row.entry_node_id ? 'node' : 'group' } : { entry_type: 'node', kind: 'direct', enabled: true, allow_private: false }); setEditing(row);
  }
  async function save(values: RuleInput & { entry_type: 'node' | 'group'; random_port?: boolean }) {
    setSaving(true);
    try {
      const { entry_type, ...input } = values;
      await api(editing ? `/rules/${editing.id}` : '/rules', editing ? 'PUT' : 'POST', { ...input, entry_node_id: entry_type === 'node' ? values.entry_node_id : undefined, entry_group_id: entry_type === 'group' ? values.entry_group_id : undefined, listen_port: values.random_port ? 0 : values.listen_port, exit_group_id: values.kind === 'tunnel' ? values.exit_group_id : undefined, target_family: 'ipv4' });
      setEditing(undefined); form.resetFields(); await load();
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }
  async function toggle(row: Rule) {
    try { await api(`/rules/${row.id}`, 'PUT', { ...ruleInput(row), enabled: !row.enabled }); await load(); } catch (e) { failed(e); }
  }
  async function remove(id: string) {
    try { await api(`/rules/${id}`, 'DELETE'); await load(); } catch (e) { failed(e); }
  }
  return <>
    <PageHeader title="转发规则" description="将入口节点或入口组的 TCP 监听端口通过直连或出口组转发到一个 IPv4 或域名目标。每 5 秒刷新。">
      <RulesTransfer selectedIDs={selectedIDs} onChanged={load} onSessionExpired={onSessionExpired} />
      <Button icon={<ReloadOutlined spin={loading} />} onClick={() => void load()} aria-label="刷新" />
      <Button type="primary" icon={<PlusOutlined />} onClick={() => edit(null)}>创建转发规则</Button>
    </PageHeader>
    {error && <Alert title={error} type="error" showIcon className="message" />}
    <div className="panel"><Table<Rule> rowKey="id" dataSource={rows} rowSelection={{ selectedRowKeys: selectedIDs, onChange: keys => setSelectedIDs(keys.map(String)) }} loading={!loaded && !error} pagination={rows.length > 100 ? { pageSize: 100, showSizeChanger: false } : false} scroll={{ x: 1130 }} locale={{ emptyText: '暂无转发规则，请先接入入口节点。' }} expandable={{ expandedRowRender: row => <RuleSyncDetails members={row.sync_members} /> }} columns={[
      { title: '规则', dataIndex: 'name', width: 130, render: value => <span className="node-name">{value}</span> },
      { title: '入口', width: 130, render: (_, row) => <>{row.entry_node_id ? nodes.find(n => n.id === row.entry_node_id)?.name ?? row.entry_node_id : groups.find(g => g.id === row.entry_group_id)?.name ?? row.entry_group_id}<span className="cell-sub">{row.entry_node_id ? '入口节点' : '入口组'}</span></> },
      { title: '链路', width: 90, render: (_, row) => row.kind === 'tunnel' ? <>隧道<span className="cell-sub">经 {exitGroups.find(g => g.id === row.exit_group_id)?.name ?? row.exit_group_id}</span></> : <>直连</> },
      { title: '监听端口', dataIndex: 'listen_port', width: 80, render: value => <span className="mono">{value}</span> },
      { title: '目标', width: 170, render: (_, row) => <><span className="mono">{row.target_address}:{row.target_port}</span><span className="cell-sub">IPv4 · {row.allow_private ? '允许私网目标' : '仅公网目标'}</span></> },
      { title: '状态', width: 70, render: (_, row) => <Tag color={row.enabled ? 'green' : 'default'}>{row.enabled ? '启用' : '停用'}</Tag> },
      { title: '同步状态', width: 90, render: (_, row) => <SyncTag status={row.sync_status} /> },
      { title: '累计上行 / 下行', width: 120, render: (_, row) => <span className="num" title={`上行 ${(row.traffic?.upload_bytes ?? 0).toLocaleString()} B，下行 ${(row.traffic?.download_bytes ?? 0).toLocaleString()} B`}>↑ {bytes(row.traffic?.upload_bytes ?? 0)}<span className="cell-sub">↓ {bytes(row.traffic?.download_bytes ?? 0)}</span></span> },
      { title: '连接数', width: 80, render: (_, row) => row.traffic?.active_connections == null ? <span className="muted">缺测</span> : <span className="num">{row.traffic.active_connections.toLocaleString()}</span> },
      { title: '', key: 'actions', fixed: 'right', width: 140, align: 'right', render: (_, row) => <div className="row-actions"><Button size="small" type="text" onClick={() => edit(row)}>编辑</Button><Popconfirm title={row.enabled ? '停用并断开该规则的所有连接？' : '启用该规则？'} onConfirm={() => toggle(row)}><Button size="small" type="text">{row.enabled ? '停用' : '启用'}</Button></Popconfirm><Popconfirm title="删除规则并断开所有连接？" onConfirm={() => remove(row.id)}><Button danger size="small" type="text">删除</Button></Popconfirm></div> },
    ]} /></div>
    <p className="footnote">保存后在节点页面核对期望与已应用配置版本。离线节点须恢复控制连接后才能应用改动。</p>
    <SyncHelp />
    <p className="footnote">流量仅计入口侧业务字节；上行为用户到目标，下行为目标到用户。离线或样本过期时连接数显示缺测。</p>
    <Modal title={editing ? '编辑转发规则' : '创建转发规则'} open={editing !== undefined} onCancel={() => { setEditing(undefined); form.resetFields(); }} footer={null} forceRender>
      <Form form={form} layout="vertical" onFinish={save} clearOnDestroy>
        {error && <Alert title={error} type="error" showIcon className="message" />}
        <Form.Item name="name" label="规则名称" rules={[{ required: true, whitespace: true, message: '请输入规则名称' }]}><Input maxLength={200} autoFocus /></Form.Item>
        <Form.Item name="entry_type" label="入口类型" rules={[{ required: true }]}><Select onChange={() => form.setFieldsValue({ entry_node_id: undefined, entry_group_id: undefined })} options={[{ value: 'node', label: '入口节点' }, { value: 'group', label: '入口组' }]} /></Form.Item>
        {entryType === 'node' ? <Form.Item name="entry_node_id" label="入口节点" preserve={false} rules={[{ required: true, message: '请选择入口节点' }]} extra="客户端连接节点地址；单节点入口没有入口切换。"><Select showSearch optionFilterProp="label" options={nodes.map(n => ({ value: n.id, label: n.name }))} /></Form.Item> : <Form.Item name="entry_group_id" label="入口组" preserve={false} rules={[{ required: true, message: '请选择入口组' }]}><Select options={groups.map(g => ({ value: g.id, label: g.name }))} /></Form.Item>}
        <Form.Item name="kind" label="链路形态" rules={[{ required: true }]}><Select options={[{ value: 'direct', label: '直连' }, { value: 'tunnel', label: '隧道' }]} /></Form.Item>
        {kind === 'tunnel' && <Form.Item name="exit_group_id" label="出口组" preserve={false} rules={[{ required: true, message: '请选择出口组' }]} extra="域名目标由出口解析。出口不可用时，新连接失败。"><Select options={exitGroups.map(g => ({ value: g.id, label: g.name }))} /></Form.Item>}
        {!editing && <Form.Item name="random_port" label="随机监听端口" valuePropName="checked" extra="保存时从 1024–65535 中选一个在实际入口节点配置中均空闲的端口，保存后固定使用。"><Switch /></Form.Item>}
        {!randomPort && <Form.Item name="listen_port" label="TCP 监听端口" preserve={false} rules={[{ required: true, message: '请输入监听端口' }]}><InputNumber min={1} max={65535} precision={0} style={{ width: '100%' }} /></Form.Item>}
        <Form.Item name="target_address" label="目标 IPv4 或域名" rules={[{ required: true, whitespace: true, message: '请输入目标地址' }]}><Input placeholder="例如：203.0.113.10 或 target.example.com" maxLength={253} /></Form.Item>
        <Form.Item name="target_port" label="目标端口" rules={[{ required: true, message: '请输入目标端口' }]}><InputNumber min={1} max={65535} precision={0} style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="allow_private" label="允许私网目标" valuePropName="checked" extra="域名解析到私网、回环或链路本地地址时，同样需要显式允许。"><Switch /></Form.Item>
        <Form.Item name="enabled" label="启用规则" valuePropName="checked"><Switch /></Form.Item>
        {editing && <Alert type="info" title="修改转发目标或监听配置会断开该规则已有的连接。" style={{ marginBottom: 16 }} />}
        <Button type="primary" htmlType="submit" block loading={saving}>保存规则</Button>
      </Form>
    </Modal>
  </>;
}
