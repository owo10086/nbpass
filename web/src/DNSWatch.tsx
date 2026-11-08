import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Drawer, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd';
import { api, APIError } from './api';

type Resolver = { kind: 'dns' | 'doh'; address: string };
type Path = { path_id: string; node_id: string; kind: string; resolver: string; ips: string[]; status: string; expected_version: number; consecutive_mismatches: number; consecutive_matches: number; sampled_at: string | null };
type History = { sequence: number; path_id: string; sampled_at: string; old_ips: string[]; ips: string[]; status: string; changed: boolean };
type Watch = { id: string; domain: string; expected_ips: string[]; expected_version: number; status: string; coverage_label: string; change_count: number; different_ip_count: number; next_sample_at: string; paths: Path[]; history?: History[]; next_before?: number };
const date = (value?: string | null) => value ? new Date(value).toLocaleString() : '尚未采样';
const ips = (values: string[]) => values.length ? values.join('、') : '无 A 记录';
const color = (status: string) => status === '疑似污染' || status === '期望不符' ? 'red' : status === '采样失败' ? 'orange' : status === '正常' || status === '符合期望' ? 'green' : 'default';

export function DNSWatch({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [rows, setRows] = useState<Watch[]>([]);
  const [nodes, setNodes] = useState<Record<string, string>>({});
  const [selected, setSelected] = useState<string>();
  const [detail, setDetail] = useState<Watch>();
  const [editing, setEditing] = useState<Watch | 'new'>();
  const [settings, setSettings] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [form] = Form.useForm();
  const [resolverForm] = Form.useForm<{ resolvers: Resolver[] }>();
  const current = useRef(onSessionExpired); current.current = onSessionExpired;
  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) current.current();
    else setError((e as Error).message);
  }
  async function load() {
    const [result, nodeList] = await Promise.all([api<{ watches: Watch[] }>('/dns-watches'), api<{ nodes: { id: string; name: string }[] }>('/nodes')]);
    setRows(result.watches); setNodes(Object.fromEntries(nodeList.nodes.map(n => [n.id, n.name])));
  }
  useEffect(() => {
    let active = true; let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      try {
        const [result, nodeList] = await Promise.all([api<{ watches: Watch[] }>('/dns-watches'), api<{ nodes: { id: string; name: string }[] }>('/nodes')]);
        if (active) { setRows(result.watches); setNodes(Object.fromEntries(nodeList.nodes.map(n => [n.id, n.name]))); }
      } catch (e) { if (active) failed(e); }
      finally { if (active) timer = setTimeout(() => void poll(), 5000); }
    }
    void poll(); return () => { active = false; clearTimeout(timer); };
  }, []);
  useEffect(() => {
    setDetail(undefined);
    if (!selected) return;
    let active = true; let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      try { const result = await api<Watch>(`/dns-watches/${selected}`); if (active) setDetail(result); }
      catch (e) { if (active) failed(e); }
      finally { if (active) timer = setTimeout(() => void poll(), 5000); }
    }
    void poll(); return () => { active = false; clearTimeout(timer); };
  }, [selected]);
  function edit(row?: Watch) {
    setError(''); setEditing(row ?? 'new'); form.setFieldsValue({ domain: row?.domain ?? '', expected: row?.expected_ips.join('\n') ?? '' });
  }
  async function save(values: { domain: string; expected?: string }) {
    setBusy(true); setError('');
    try {
      const expected_ips = (values.expected ?? '').split(/[\s,，]+/).filter(Boolean);
      if (editing === 'new') await api('/dns-watches', 'POST', { domain: values.domain, expected_ips });
      else if (editing) await api(`/dns-watches/${editing.id}`, 'PUT', { expected_ips });
      setEditing(undefined); form.resetFields(); await load();
    } catch (e) { failed(e); } finally { setBusy(false); }
  }
  async function remove(id: string) {
    setBusy(true); setError('');
    try { await api(`/dns-watches/${id}`, 'DELETE'); if (selected === id) setSelected(undefined); await load(); }
    catch (e) { failed(e); } finally { setBusy(false); }
  }
  async function openSettings() {
    setError('');
    try { resolverForm.setFieldsValue(await api<{ resolvers: Resolver[] }>('/settings/dns-watch')); setSettings(true); }
    catch (e) { failed(e); }
  }
  async function saveSettings(values: { resolvers: Resolver[] }) {
    setBusy(true); setError('');
    try { await api('/settings/dns-watch', 'PUT', values); setSettings(false); await load(); }
    catch (e) { failed(e); } finally { setBusy(false); }
  }
  const pathName = (path: { node_id: string; resolver: string }) => `${path.node_id ? nodes[path.node_id] ?? '国内检测点' : '面板本机 DoH'} · ${path.resolver}`;
  return <>
    <Typography.Title level={2}>解析监控</Typography.Title>
    <Typography.Paragraph type="secondary">每 5 分钟查询 A 记录。期望 IP 可选；同一路径对同一期望连续 3 次不符才判异常，其他路径正常时提示疑似污染。解析异常只记录和通知。</Typography.Paragraph>
    {error && <Alert className="message" title={error} type="error" showIcon />}
    <Space wrap style={{ marginBottom: 16 }}><Button type="primary" onClick={() => edit()}>添加域名</Button><Button onClick={() => void openSettings()}>解析器设置</Button><Button onClick={() => void load().catch(failed)}>刷新</Button></Space>
    <Card styles={{ body: { padding: 0 } }}><Table rowKey="id" dataSource={rows} scroll={{ x: 1000 }} columns={[
      { title: '域名', dataIndex: 'domain', render: (_, row) => <Button type="link" onClick={() => setSelected(row.id)}>{row.domain}</Button> },
      { title: '期望 IP', render: (_, row) => row.expected_ips.length ? ips(row.expected_ips) : '仅观察' },
      { title: '状态', render: (_, row) => <Space orientation="vertical"><Tag color={color(row.status)}>{row.status}</Tag><Typography.Text type="secondary">{row.coverage_label}</Typography.Text></Space> },
      { title: '变化次数', dataIndex: 'change_count' }, { title: '不同 IP 数', dataIndex: 'different_ip_count' },
      { title: '下次查询', dataIndex: 'next_sample_at', render: date },
      { title: '操作', render: (_, row) => <Space><Button onClick={() => edit(row)}>修改期望</Button><Popconfirm title={`删除 ${row.domain} 的解析监控及历史？`} onConfirm={() => remove(row.id)}><Button danger loading={busy}>删除</Button></Popconfirm></Space> },
    ]} /></Card>
    <Modal title={editing === 'new' ? '添加解析监控' : '修改期望 IP'} open={!!editing} onCancel={() => setEditing(undefined)} onOk={() => form.submit()} confirmLoading={busy} destroyOnHidden>
      {error && <Alert title={error} type="error" showIcon />}
      <Form form={form} layout="vertical" onFinish={save}><Form.Item name="domain" label="域名" rules={[{ required: true, message: '请输入纯域名' }]}><Input disabled={editing !== 'new'} placeholder="example.com" /></Form.Item><Form.Item name="expected" label="期望 IPv4（可选，每行一个）"><Input.TextArea rows={3} placeholder="留空只记录变化" /></Form.Item></Form>
    </Modal>
    <Modal title="解析器设置" open={settings} onCancel={() => setSettings(false)} onOk={() => resolverForm.submit()} confirmLoading={busy} width={720} destroyOnHidden>
      <Typography.Paragraph type="secondary">国内 DNS 使用公共 IPv4 的 53 端口，例如 223.5.5.5:53；DoH 使用 HTTPS 地址，例如 https://cloudflare-dns.com/dns-query。最多 8 个，至少保留一个 DoH 对照；更改后重新采样。</Typography.Paragraph>
      {error && <Alert title={error} type="error" showIcon />}
      <Form form={resolverForm} onFinish={saveSettings} layout="vertical"><Form.List name="resolvers">{(fields, { add, remove }) => <>{fields.map(field => <Space key={field.key} align="baseline" wrap><Form.Item name={[field.name, 'kind']} rules={[{ required: true }]}><Select style={{ width: 120 }} options={[{ value: 'dns', label: '国内 DNS' }, { value: 'doh', label: '面板 DoH' }]} /></Form.Item><Form.Item name={[field.name, 'address']} rules={[{ required: true, message: '请输入解析器地址' }]}><Input style={{ width: 330, maxWidth: '100%' }} /></Form.Item><Button danger onClick={() => remove(field.name)}>移除</Button></Space>)}<Button disabled={fields.length >= 8} onClick={() => add({ kind: 'dns', address: '' })}>增加解析器</Button></>}</Form.List></Form>
    </Modal>
    <Drawer title={detail?.domain ?? '解析详情'} open={!!selected} onClose={() => setSelected(undefined)} width={1050} destroyOnHidden loading={!detail}>
      {detail && <Space orientation="vertical" size="large" style={{ width: '100%' }}>
        <Space wrap><Tag color={color(detail.status)}>{detail.status}</Tag><span>{detail.coverage_label}</span><span>期望版本 {detail.expected_version} · 变化 {detail.change_count} 次 · 不同 IP {detail.different_ip_count} 个</span></Space>
        <Typography.Text>期望：{detail.expected_ips.length ? ips(detail.expected_ips) : '仅观察'}；下次查询：{date(detail.next_sample_at)}</Typography.Text>
        <Table rowKey="path_id" size="small" pagination={false} scroll={{ x: 850 }} dataSource={detail.paths} columns={[
          { title: '解析路径', render: (_, p) => pathName(p), width: 320 }, { title: '观察值', dataIndex: 'ips', render: ips },
          { title: '状态', dataIndex: 'status', render: value => <Tag color={color(value)}>{value}</Tag> },
          { title: '连续不符', dataIndex: 'consecutive_mismatches' }, { title: '最近采样', dataIndex: 'sampled_at', render: date },
        ]} />
        <Typography.Title level={4}>解析变化与失败历史</Typography.Title>
        <Typography.Text type="secondary">首次成功建立基线；地址集合变化才增加变化次数，解析失败保留上次成功基线。不同 IP 数为所有路径成功观察值的去重总数。</Typography.Text>
        <Table rowKey="sequence" size="small" scroll={{ x: 850 }} dataSource={detail.history ?? []} columns={[
          { title: '时间', dataIndex: 'sampled_at', render: date, width: 180 },
          { title: '路径', dataIndex: 'path_id', render: value => { const p = detail.paths.find(p => p.path_id === value); return p ? pathName(p) : value; } },
          { title: '上次成功', dataIndex: 'old_ips', render: ips }, { title: '本次结果', dataIndex: 'ips', render: ips }, { title: '状态', dataIndex: 'status' },
        ]} />
        {detail.next_before && <Button onClick={async () => { try { const result = await api<Watch>(`/dns-watches/${detail.id}?before=${detail.next_before}`); setDetail({ ...result, history: [...(detail.history ?? []), ...(result.history ?? [])] }); } catch (e) { failed(e); } }}>加载更早历史（自动刷新会返回最新记录）</Button>}
      </Space>}
    </Drawer>
  </>;
}
