import { useEffect, useState } from 'react';
import { Alert, Button, Descriptions, Drawer, Form, Input, InputNumber, Modal, Space, Switch, Typography } from 'antd';
import { GlobalOutlined, SettingOutlined } from '@ant-design/icons';
import { api } from './api';
import { FormSection } from './FormSection';
import { StatusDot, type StatusTone } from './StatusDot';
import { TableCard } from './TableCard';
import './servers.css';

type Settings = {
  sites: string[]; interval_seconds: number; jitter_seconds: number; connect_timeout_seconds: number;
  resolve_timeout_seconds: number; resolve_attempts: number; round_timeout_seconds: number;
};
type Connection = { address: string; connected: boolean; error?: string; duration_ms: number };
type Site = { host: string; addresses: string[]; resolve_attempts: number; resolve_error?: string; result: string; connections: Connection[] };
type Arbitration = { target_ip: string; target_port: number; config_revision: number; deadline: string; status: string; checkers: { id: string; checker_id: string; result: { control: Connection; target?: Connection; execution_error?: string; started_at: string; finished_at: string } | null }[] };
type Round = { id: string; sequence: number; started_at: string; finished_at: string; result: string; source: string; final_result: string; evidence_source: string; config_revision: number; sites: Site[]; arbitration?: Arbitration };
type View = { enabled: boolean; state: string; consecutive_abnormal: number; rounds: Round[]; next_before: number | null };
const labels: Record<string, string> = { normal: '正常', abnormal: '异常', unknown: '未知', suspected: '疑似被墙', blocked: '被墙', failed: '连接失败' };
const tones: Record<string, StatusTone> = { normal: 'ok', abnormal: 'warn', suspected: 'warn', blocked: 'danger' };
const date = (value: string) => new Date(value).toLocaleString();
function mergeLatest(previous: View | undefined, next: View): View {
  if (!previous) return next;
  const rounds = Array.from(new Map([...previous.rounds, ...next.rounds].map(round => [round.id, round])).values()).sort((a, b) => b.sequence - a.sequence);
  const previousOldest = previous.rounds[previous.rounds.length - 1]?.sequence ?? Infinity;
  const nextOldest = next.rounds[next.rounds.length - 1]?.sequence ?? Infinity;
  return { ...next, rounds, next_before: previousOldest < nextOldest ? previous.next_before : next.next_before };
}

export function NodeSelfCheck({ node, onClose, onError }: { node: { id: string; name: string }; onClose: () => void; onError: (error: unknown) => void }) {
  const [view, setView] = useState<View>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [settings, setSettings] = useState(false);
  const path = `/nodes/${node.id}/self-check`;
  useEffect(() => {
    let active = true;
    const load = async () => {
      try { const next = await api<View>(path); if (active) { setView(previous => mergeLatest(previous, next)); setError(''); } }
      catch (e) { if (active) { setError((e as Error).message); onError(e); } }
    };
    void load(); const timer = window.setInterval(() => void load(), 5000);
    return () => { active = false; window.clearInterval(timer); };
  }, [path]);
  async function toggle(enabled: boolean) {
    setBusy(true);
    try { await api(path, 'PUT', { enabled }); const next = await api<View>(path); setView(previous => mergeLatest(previous, next)); setError(''); }
    catch (e) { setError((e as Error).message); onError(e); }
    finally { setBusy(false); }
  }
  async function more() {
    if (!view?.next_before) return;
    setBusy(true);
    try { const next = await api<View>(`${path}?before=${view.next_before}`); setView(previous => ({ ...(previous ?? next), rounds: Array.from(new Map([...(previous?.rounds ?? []), ...next.rounds].map(round => [round.id, round])).values()).sort((a, b) => b.sequence - a.sequence), next_before: next.next_before })); }
    catch (e) { onError(e); }
    finally { setBusy(false); }
  }
  return <Drawer open title={`${node.name} · 自检与被墙认定`} onClose={onClose} size={1100} className="servers-drawer">
    {error && <Alert type="error" showIcon title={error} />}
    <Space wrap style={{ marginBottom: 16 }}>
      <Typography.Text>开启本服务器自检</Typography.Text><Switch checked={view?.enabled ?? false} disabled={!view} loading={busy} onChange={enabled => void toggle(enabled)} />
      <StatusDot tone={tones[view?.state ?? 'unknown']}>{labels[view?.state ?? 'unknown']}</StatusDot>
      <Typography.Text>连续异常 {view?.consecutive_abnormal ?? 0} 轮</Typography.Text>
      <Button onClick={() => setSettings(true)}>自检站点与参数</Button>
    </Space>
    <Alert type="info" showIcon title="依据服务器出站自检推定被墙" description="默认约每 2 分钟连接四个国内站点的 TCP 443 端口，至少三个站点连接失败为异常。自检未知时，由国内检测点在 60 秒内补判：任一有效点连通为正常，全部有效点连接失败为异常，缺报、执行错误或无有效结果则保持未知。连续三轮最终结果异常认定被墙，未知轮中断计数，一轮正常即可解除。自检不能证明所有国内网络的入站可达性；关闭后保留历史状态与证据。" style={{ marginBottom: 16 }} />
    <TableCard<Round> title="自检记录" rowKey="id" loading={!view && !error} dataSource={view?.rounds} pagination={false} scroll={{ x: 850 }} locale={{ emptyText: '尚无自检结果；离线期间的结果将在重连后补报。' }} columns={[
      { title: '轮次', dataIndex: 'sequence' },
      { title: '开始 / 结束', render: (_, row) => <Space orientation="vertical" size={0}><span>{date(row.started_at)}</span><Typography.Text type="secondary">{date(row.finished_at)}</Typography.Text></Space> },
      { title: '原始结果', render: (_, row) => <StatusDot tone={tones[row.result]}>{labels[row.result]}</StatusDot> },
      { title: '最终结果', render: (_, row) => labels[row.final_result] },
      { title: '证据来源', render: (_, row) => row.evidence_source === 'domestic_checker' ? '检测点补判' : row.arbitration?.status === 'pending' ? '等待检测点补判' : '服务器自检' },
      { title: '配置版本', dataIndex: 'config_revision' },
    ]} expandable={{ expandedRowRender: row => <Space orientation="vertical" style={{ width: '100%' }}>{row.arbitration && <CheckerEvidence value={row.arbitration} />}{row.sites.map(site => <Descriptions key={site.host} title={`${site.host}:443 · ${labels[site.result]}`} size="small" column={1} items={[
      { key: 'dns', label: '解析', children: `${site.resolve_attempts} 次；${site.addresses.join('、') || '无 IPv4'}${site.resolve_error ? `；${site.resolve_error}` : ''}` },
      { key: 'tcp', label: 'TCP 原始结果', children: site.connections.length ? site.connections.map((connection, i) => <div key={i}>{connection.address} · {connection.connected ? '成功' : connection.error || '失败'} · {connection.duration_ms} 毫秒</div>) : '未完成 TCP 连接检测' },
    ]} />)}</Space> }} />
    {view?.next_before && <Button loading={busy} onClick={() => void more()} style={{ marginTop: 16 }}>加载更早轮次</Button>}
    {settings && <SelfCheckSettings onClose={() => setSettings(false)} onError={onError} />}
  </Drawer>;
}

function CheckerEvidence({ value }: { value: Arbitration }) {
  const status: Record<string, string> = { pending: '等待补判', completed: '已结束', invalidated: '目标 IP、端口或配置已变化，旧任务作废' };
  const connection = (c?: Connection) => c ? `${c.address || '未取得地址'} · ${c.connected ? '成功' : c.error || '失败'} · ${c.duration_ms} 毫秒` : '未执行';
  return <Descriptions title="国内检测点补判 · 本轮固定任务" column={1} size="small" items={[
    { key: 'target', label: '被测地址', children: `${value.target_ip || '未知 IP'}:${value.target_port}；配置版本 ${value.config_revision}` },
    { key: 'deadline', label: '截止时间 / 状态', children: `${date(value.deadline)} · ${status[value.status] ?? value.status}` },
    { key: 'checkers', label: '检测点与原始证据', children: value.checkers.length ? value.checkers.map(c => <div key={c.id} style={{ marginBottom: 8 }}><Typography.Text code>{c.checker_id}</Typography.Text>{c.result ? <><div>对照：{connection(c.result.control)}</div><div>目标：{connection(c.result.target)}</div><div>{c.result.execution_error || (!c.result.control.connected ? '本轮对照未成功，结果无效' : '')}</div><Typography.Text type="secondary">{date(c.result.started_at)} — {date(c.result.finished_at)}</Typography.Text></> : <div>{value.status === 'pending' ? '尚未上报' : '无采纳结果'}</div>}</div>) : '国内覆盖缺失或目标 IP 未知，本轮保持未知' },
  ]} />;
}

export function SelfCheckSettings({ onClose, onError }: { onClose: () => void; onError: (error: unknown) => void }) {
  const [form] = Form.useForm<Settings>();
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    let active = true;
    void api<Settings>('/settings/self-check').then(value => { if (active) { form.setFieldsValue(value); setReady(true); } }).catch(e => { if (active) { setError((e as Error).message); onError(e); } });
    return () => { active = false; };
  }, [form]);
  async function save(value: Settings) {
    setBusy(true);
    try { await api('/settings/self-check', 'PUT', value); onClose(); }
    catch (e) { setError((e as Error).message); onError(e); }
    finally { setBusy(false); }
  }
  const fields: { name: keyof Omit<Settings, 'sites'>; label: string; min: number; max: number }[] = [
    { name: 'interval_seconds', label: '每轮间隔（秒）', min: 30, max: 3600 },
    { name: 'jitter_seconds', label: '间隔随机浮动（±秒）', min: 0, max: 30 },
    { name: 'connect_timeout_seconds', label: '单次 TCP 连接期限（秒）', min: 1, max: 5 },
    { name: 'resolve_timeout_seconds', label: '每次解析期限（秒）', min: 1, max: 10 },
    { name: 'resolve_attempts', label: '最多解析次数', min: 1, max: 2 },
    { name: 'round_timeout_seconds', label: '单轮总期限（秒）', min: 1, max: 60 },
  ];
  return <Modal open title="自检站点与参数（所有服务器）" onCancel={onClose} footer={null} width={680} className="servers-modal" destroyOnHidden>
    <Typography.Paragraph type="secondary">设置应用到已配置自检的服务器；离线服务器将在重连后同步。请填写四个不同的国内站点域名，不带协议、路径和端口。检测固定使用 TCP 443。</Typography.Paragraph>
    {error && <Alert title={error} type="error" showIcon />}
    <Form form={form} layout="vertical" onFinish={save} disabled={!ready || busy} clearOnDestroy>
      <FormSection title="国内站点" icon={<GlobalOutlined />}><div className="servers-settings-grid">
      {[0, 1, 2, 3].map(index => <Form.Item key={index} name={['sites', index]} label={`国内站点 ${index + 1}`} rules={[{ required: true, whitespace: true, message: '请输入站点域名' }]}><Input maxLength={253} /></Form.Item>)}
      </div></FormSection>
      <FormSection title="检测参数" icon={<SettingOutlined />}><div className="servers-settings-grid">
      {fields.map(field => <Form.Item key={field.name} name={field.name} label={field.label} rules={[{ required: true, message: '请输入参数' }]}><InputNumber min={field.min} max={field.max} precision={0} style={{ width: '100%' }} /></Form.Item>)}
      </div></FormSection>
      <Button type="primary" htmlType="submit" loading={busy} disabled={!ready}>保存自检设置</Button>
    </Form>
  </Modal>;
}
