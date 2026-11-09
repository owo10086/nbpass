import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd';
import { api, APIError } from './api';

type Group = { id: string; name: string; strategy: 'primary_backup'; members: string[] };
type ExitHealth = { exit_id: string; state: string; consecutive_failures?: number; last_probe?: string; failure_class?: string; recovery_since_unix?: number; checked_at_unix?: number };
type HealthReport = { fresh: boolean; sampled_at: string | null; exits: ExitHealth[] };
type Member = { id: string; name: string };
type Exit = { node_id: string; address: string; listen_port: number; transport: 'wss'; server_name: string; certificate_pem: string };

export function ExitGroups({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [rows, setRows] = useState<Group[]>([]);
  const [nodes, setNodes] = useState<Member[]>([]);
  const [exits, setExits] = useState<Exit[]>([]);
  const [healthNode, setHealthNode] = useState<string>();
  const [health, setHealth] = useState<HealthReport>();
  const [healthError, setHealthError] = useState('');
  const [editing, setEditing] = useState<Group | null>();
  const [configuring, setConfiguring] = useState(false);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();
  const [exitForm] = Form.useForm();
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else setError((e as Error).message);
  }
  async function load() {
    setLoading(true);
    try {
      const [groups, members, configured] = await Promise.all([api<{ exit_groups: Group[] }>('/exit-groups'), api<{ nodes: Member[] }>('/nodes'), api<{ exits: Exit[] }>('/tunnel-exits')]);
      setRows(groups.exit_groups); setNodes(members.nodes); setExits(configured.exits); setError('');
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }
  useEffect(() => { void load(); }, []);
  useEffect(() => {
    if (!healthNode) { setHealth(undefined); return; }
    let active = true;
    setHealth(undefined); setHealthError('');
    const refresh = async () => {
      try { const report = await api<HealthReport>(`/nodes/${healthNode}/exit-health`); if (active) { setHealth(report); setHealthError(''); } }
      catch (e) { if (active) { setHealth(undefined); if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current(); else setHealthError((e as Error).message); } }
    };
    void refresh(); const timer = window.setInterval(() => void refresh(), 5000);
    return () => { active = false; window.clearInterval(timer); };
  }, [healthNode]);
  function edit(row: Group | null) {
    setError(''); form.resetFields(); form.setFieldsValue(row ? { name: row.name, members: row.members } : { members: [undefined] }); setEditing(row);
  }
  function configure(row?: Exit) {
    setError(''); exitForm.resetFields(); if (row) exitForm.setFieldsValue(row); setConfiguring(true);
  }
  async function save(values: { name: string; members: string[] }) {
    setSaving(true);
    try {
      await api(editing ? `/exit-groups/${editing.id}` : '/exit-groups', editing ? 'PUT' : 'POST', { name: values.name, members: values.members, strategy: 'primary_backup' });
      setEditing(undefined); form.resetFields(); await load();
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }
  async function saveExit(values: { node_id: string; address: string; listen_port: number }) {
    setSaving(true);
    try {
      await api(`/nodes/${values.node_id}/tunnel`, 'PUT', { address: values.address, listen_port: values.listen_port, transport: 'wss' });
      setConfiguring(false); exitForm.resetFields(); await load();
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }
  async function remove(id: string) {
    try { await api(`/exit-groups/${id}`, 'DELETE'); await load(); } catch (e) { failed(e); }
  }
  const nodeName = (id: string) => nodes.find(n => n.id === id)?.name ?? id;
  return <>
    <div className="nodes-heading"><div><Typography.Title level={2}>出口组</Typography.Title><Typography.Paragraph type="secondary">先配置出口的 WSS 地址和监听端口，再将节点加入出口组供隧道规则使用。</Typography.Paragraph></div><Space wrap><Button onClick={() => void load()}>刷新</Button><Button onClick={() => configure()}>配置出口</Button><Button type="primary" onClick={() => edit(null)}>创建出口组</Button></Space></div>
    {error && <Alert title={error} type="error" showIcon className="message" />}
    <Card styles={{ body: { padding: 0 } }}><Table<Group> rowKey="id" dataSource={rows} loading={loading} scroll={{ x: 680 }} locale={{ emptyText: '暂无出口组，请先配置一个出口节点。' }} columns={[
      { title: '出口组', dataIndex: 'name' },
      { title: '出口节点', render: (_, row) => row.members.map((id, i) => `${i + 1}. ${nodeName(id)}`).join(' → ') },
      { title: '出口策略', render: () => <Tag>主备 · 按顺序</Tag> },
      { title: '操作', render: (_, row) => <Space><Button size="small" onClick={() => edit(row)}>编辑</Button><Popconfirm title="删除出口组？" description="须先删除引用该组的转发规则。" onConfirm={() => remove(row.id)}><Button danger size="small">删除</Button></Popconfirm></Space> },
    ]} /></Card>
    <Typography.Title level={3}>出口配置</Typography.Title>
    <Card styles={{ body: { padding: 0 } }}><Table<Exit> rowKey="node_id" dataSource={exits} loading={loading} scroll={{ x: 800 }} locale={{ emptyText: '暂无已配置出口' }} columns={[
      { title: '出口节点', render: (_, row) => nodeName(row.node_id) },
      { title: 'WSS 端点', render: (_, row) => `${row.address}:${row.listen_port}` },
      { title: '证书身份', dataIndex: 'server_name', render: value => <Typography.Text copyable style={{ wordBreak: 'break-all' }}>{value}</Typography.Text> },
      { title: '操作', render: (_, row) => <Button size="small" onClick={() => configure(row)}>编辑地址与端口</Button> },
    ]} /></Card>
    <Typography.Title level={3}>出口链路健康</Typography.Title>
    <Typography.Paragraph type="secondary">选择入口查看它到各出口的观测。目标不可达不影响出口健康；控制失联后节点仍自行探测和切换。</Typography.Paragraph>
    <Select allowClear placeholder="选择观测入口" value={healthNode} onChange={setHealthNode} style={{ minWidth: 240, marginBottom: 16 }} options={nodes.map(n => ({ value: n.id, label: n.name }))} />
    {healthError && <Alert type="error" title={healthError} showIcon />}
    {health && !health.fresh && <Alert type="warning" title="当前无实时证据，以下为最近观测，不代表当前健康状态。" showIcon style={{ marginBottom: 12 }} />}
    {healthNode && <Card styles={{ body: { padding: 0 } }}><Table<ExitHealth> rowKey="exit_id" dataSource={health?.exits ?? []} scroll={{ x: 760 }} locale={{ emptyText: '尚无出口探测结果' }} columns={[
      { title: '出口', render: (_, row) => nodeName(row.exit_id) },
      { title: '链路状态', render: (_, row) => <Tag color={!health?.fresh ? 'default' : row.state === 'healthy' ? 'green' : row.state === 'unhealthy' ? 'red' : 'orange'}>{!health?.fresh && '历史 · '}{({ healthy: '健康', unhealthy: '故障', recovering: '恢复观察中', unknown: '待探测' } as Record<string, string>)[row.state] ?? '未知'}</Tag> },
      { title: '连续失败', render: (_, row) => row.consecutive_failures ?? 0 },
      { title: '最近探测', render: (_, row) => ({ success: '成功', failure: '失败', incomplete: '未完成' } as Record<string, string>)[row.last_probe ?? ''] ?? '待探测' },
      { title: '故障归属', render: (_, row) => ({ entry_exit: '入口→出口', exit_target: '出口→目标', local_resource: '本地资源', configuration_authorization: '配置/授权' } as Record<string, string>)[row.failure_class ?? ''] ?? '—' },
      { title: '恢复观察起点', render: (_, row) => row.recovery_since_unix ? new Date(row.recovery_since_unix * 1000).toLocaleTimeString() : '—' },
      { title: '探测时间', render: (_, row) => row.checked_at_unix ? new Date(row.checked_at_unix * 1000).toLocaleTimeString() : '—' },
    ]} /></Card>}
    <Typography.Paragraph type="secondary" style={{ marginTop: 16 }}>面板自动生成有效期 5 年的出口证书，更换地址会保留证书身份。保存后请在节点页面核对配置同步结果，并放行出口监听端口。</Typography.Paragraph>
    <Modal title={editing ? '编辑出口组' : '创建出口组'} open={editing !== undefined} onCancel={() => { setEditing(undefined); form.resetFields(); }} footer={null} forceRender>
      <Form form={form} layout="vertical" onFinish={save} clearOnDestroy>
        {error && <Alert title={error} type="error" showIcon className="message" />}
        <Form.Item name="name" label="出口组名称" rules={[{ required: true, whitespace: true, message: '请输入出口组名称' }]}><Input maxLength={200} /></Form.Item>
        <Typography.Paragraph type="secondary">第一项为主出口，其余按顺序备用。可用箭头调整顺序；故障恢复后连续探测成功 60 秒才重新接新连接。</Typography.Paragraph>
        <Form.List name="members" rules={[{ validator: async (_, members: string[]) => { if (!members?.length) throw new Error('至少选择一个出口'); if (new Set(members).size !== members.length) throw new Error('出口不能重复'); } }]}>
          {(fields, { add, remove, move }, { errors }) => <>
            {fields.map((field, index) => <Space key={field.key} wrap align="baseline" style={{ display: 'flex' }}>
              <Form.Item name={field.name} label={index === 0 ? '主出口' : `备出口 ${index}`} rules={[{ required: true, message: '请选择出口' }]}><Select style={{ minWidth: 200 }} options={exits.map(e => ({ value: e.node_id, label: nodeName(e.node_id) }))} /></Form.Item>
              <Button disabled={index === 0} onClick={() => move(index, index - 1)} aria-label="上移出口">↑</Button>
              <Button disabled={index === fields.length - 1} onClick={() => move(index, index + 1)} aria-label="下移出口">↓</Button>
              <Button danger disabled={fields.length === 1} onClick={() => remove(field.name)}>移除</Button>
            </Space>)}
            <Form.ErrorList errors={errors} />
            <Button onClick={() => add()} style={{ marginBottom: 16 }}>添加备出口</Button>
          </>}
        </Form.List>
        <Button type="primary" htmlType="submit" block loading={saving}>保存出口组</Button>
      </Form>
    </Modal>
    <Modal title="配置出口" open={configuring} onCancel={() => { setConfiguring(false); exitForm.resetFields(); }} footer={null} forceRender>
      <Form form={exitForm} layout="vertical" onFinish={saveExit} clearOnDestroy>
        {error && <Alert title={error} type="error" showIcon className="message" />}
        <Form.Item name="node_id" label="出口节点" rules={[{ required: true, message: '请选择出口节点' }]}><Select options={nodes.map(n => ({ value: n.id, label: n.name }))} onChange={id => { const e = exits.find(e => e.node_id === id); exitForm.setFieldsValue({ address: e?.address, listen_port: e?.listen_port }); }} /></Form.Item>
        <Form.Item name="address" label="可直接入站的 IPv4" extra="填写入口可以访问的出口地址；出站公网 IP 观测值不能证明可入站。" rules={[{ required: true, whitespace: true, message: '请输入出口 IPv4' }]}><Input maxLength={15} placeholder="例如：203.0.113.20" /></Form.Item>
        <Form.Item name="listen_port" label="WSS 监听端口" rules={[{ required: true, message: '请输入监听端口' }]}><InputNumber min={1} max={65535} precision={0} style={{ width: '100%' }} /></Form.Item>
        <Button type="primary" htmlType="submit" block loading={saving}>保存出口配置</Button>
      </Form>
    </Modal>
  </>;
}
