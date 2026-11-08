import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Typography } from 'antd';
import { api, APIError } from './api';

type Rule = { id: string; name: string; entry_group_id: string; kind: 'direct' | 'tunnel'; exit_group_id?: string; listen_port: number; target_address: string; target_family: 'ipv4'; target_port: number; allow_private: boolean; enabled: boolean };
type Group = { id: string; name: string };

export function Rules({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [rows, setRows] = useState<Rule[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [exitGroups, setExitGroups] = useState<Group[]>([]);
  const [editing, setEditing] = useState<Rule | null>();
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();
  const kind = Form.useWatch('kind', form);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else setError((e as Error).message);
  }
  async function load() {
    setLoading(true);
    try {
      const [rules, entryGroups, exits] = await Promise.all([api<{ rules: Rule[] }>('/rules'), api<{ entry_groups: Group[] }>('/entry-groups'), api<{ exit_groups: Group[] }>('/exit-groups')]);
      setRows(rules.rules); setGroups(entryGroups.entry_groups); setExitGroups(exits.exit_groups); setError('');
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }
  useEffect(() => { void load(); }, []);
  function edit(row: Rule | null) {
    setError(''); form.resetFields(); form.setFieldsValue(row ?? { kind: 'direct', enabled: true, allow_private: false }); setEditing(row);
  }
  async function save(values: Omit<Rule, 'id'>) {
    setSaving(true);
    try {
      await api(editing ? `/rules/${editing.id}` : '/rules', editing ? 'PUT' : 'POST', { ...values, exit_group_id: values.kind === 'tunnel' ? values.exit_group_id : undefined, target_family: 'ipv4' });
      setEditing(undefined); form.resetFields(); await load();
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }
  async function toggle(row: Rule) {
    try { await api(`/rules/${row.id}`, 'PUT', { ...row, enabled: !row.enabled }); await load(); } catch (e) { failed(e); }
  }
  async function remove(id: string) {
    try { await api(`/rules/${id}`, 'DELETE'); await load(); } catch (e) { failed(e); }
  }
  return <>
    <div className="nodes-heading"><div><Typography.Title level={2}>转发规则</Typography.Title><Typography.Paragraph type="secondary">将入口组的 TCP 监听端口通过直连或出口组转发到一个 IPv4 或域名目标。</Typography.Paragraph></div><Space><Button onClick={() => void load()}>刷新</Button><Button type="primary" onClick={() => edit(null)}>创建转发规则</Button></Space></div>
    {error && <Alert title={error} type="error" showIcon className="message" />}
    <Card styles={{ body: { padding: 0 } }}><Table<Rule> rowKey="id" dataSource={rows} loading={loading} scroll={{ x: 1000 }} locale={{ emptyText: '暂无转发规则，请先创建入口组。' }} columns={[
      { title: '规则', dataIndex: 'name' },
      { title: '入口组', render: (_, row) => groups.find(g => g.id === row.entry_group_id)?.name ?? row.entry_group_id },
      { title: '链路', render: (_, row) => row.kind === 'tunnel' ? <Space orientation="vertical" size={0}><Tag color="blue">隧道</Tag><span>{exitGroups.find(g => g.id === row.exit_group_id)?.name ?? row.exit_group_id}</span></Space> : <Tag>直连</Tag> },
      { title: '监听端口', dataIndex: 'listen_port' },
      { title: '目标', render: (_, row) => <Space orientation="vertical" size={0}><span>{row.target_address}:{row.target_port}</span><Typography.Text type="secondary">IPv4 · {row.allow_private ? '允许私网目标' : '仅公网目标'}</Typography.Text></Space> },
      { title: '状态', render: (_, row) => <Tag color={row.enabled ? 'green' : 'default'}>{row.enabled ? '启用' : '停用'}</Tag> },
      { title: '操作', render: (_, row) => <Space><Button size="small" onClick={() => edit(row)}>编辑</Button><Popconfirm title={row.enabled ? '停用并断开该规则的所有连接？' : '启用该规则？'} onConfirm={() => toggle(row)}><Button size="small">{row.enabled ? '停用' : '启用'}</Button></Popconfirm><Popconfirm title="删除规则并断开所有连接？" onConfirm={() => remove(row.id)}><Button danger size="small">删除</Button></Popconfirm></Space> },
    ]} /></Card>
    <Typography.Paragraph type="secondary" style={{ marginTop: 16 }}>保存后在节点页面核对期望与已应用配置版本。离线节点须恢复控制连接后才能应用改动。</Typography.Paragraph>
    <Modal title={editing ? '编辑转发规则' : '创建转发规则'} open={editing !== undefined} onCancel={() => { setEditing(undefined); form.resetFields(); }} footer={null} forceRender>
      <Form form={form} layout="vertical" onFinish={save} clearOnDestroy>
        {error && <Alert title={error} type="error" showIcon className="message" />}
        <Form.Item name="name" label="规则名称" rules={[{ required: true, whitespace: true, message: '请输入规则名称' }]}><Input maxLength={200} autoFocus /></Form.Item>
        <Form.Item name="entry_group_id" label="入口组" rules={[{ required: true, message: '请选择入口组' }]}><Select options={groups.map(g => ({ value: g.id, label: g.name }))} /></Form.Item>
        <Form.Item name="kind" label="链路形态" rules={[{ required: true }]}><Select options={[{ value: 'direct', label: '直连' }, { value: 'tunnel', label: '隧道' }]} /></Form.Item>
        {kind === 'tunnel' && <Form.Item name="exit_group_id" label="出口组" preserve={false} rules={[{ required: true, message: '请选择出口组' }]} extra="域名目标由出口解析。出口不可用时，新连接失败。"><Select options={exitGroups.map(g => ({ value: g.id, label: g.name }))} /></Form.Item>}
        <Form.Item name="listen_port" label="TCP 监听端口" rules={[{ required: true, message: '请输入监听端口' }]}><InputNumber min={1} max={65535} precision={0} style={{ width: '100%' }} /></Form.Item>
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
