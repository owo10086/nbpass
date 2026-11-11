import { useEffect, useRef, useState } from 'react';
import { Alert, Card, Descriptions, Drawer, Select, Space, Spin, Typography } from 'antd';
import { api, APIError } from './api';
import { StatusDot } from './StatusDot';
import './probe.css';

type Values = Record<string, number | null>;
type Minute = { minute: string; samples: number; average: Values | null; maximum: Values | null; counts?: Record<string, number> };
type ProbeView = { fresh: boolean; sampled_at: string | null; realtime: Values | null; minutes: Minute[] };
type Node = { id: string; name: string; status: string; status_label: string; connected: boolean };
const metrics = [
  ['cpu_percent', 'CPU', '%'], ['memory_used_bytes', '内存已用', 'B'], ['disk_used_bytes', '根文件系统已用', 'B'],
  ['network_receive_bytes_per_second', '网卡接收速率', 'B/s'], ['network_transmit_bytes_per_second', '网卡发送速率', 'B/s'],
  ['business_connections', '业务连接数', ''], ['tunnel_connections', '隧道连接数', ''], ['system_tcp_connections', '系统 TCP 连接数', ''],
  ['uptime_seconds', '服务器进程运行时长', 's'],
] as const;

export function format(value: number | null | undefined, unit: string) {
  if (value == null) return '—';
  if (unit === 'B' || unit === 'B/s') {
    const index = Math.min(4, Math.max(0, Math.floor(Math.log2(Math.max(1, value)) / 10)));
    return `${(value / 1024 ** index).toFixed(index ? 1 : 0)} ${['B', 'KiB', 'MiB', 'GiB', 'TiB'][index]}${unit === 'B/s' ? '/s' : ''}`;
  }
  if (unit === 's') {
    const seconds = Math.floor(value);
    return `${Math.floor(seconds / 86400)} 天 ${Math.floor(seconds % 86400 / 3600)} 小时 ${Math.floor(seconds % 3600 / 60)} 分 ${seconds % 60} 秒`;
  }
  return `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })}${unit}`;
}

function MinuteChart({ minutes, metric }: { minutes: Minute[]; metric: typeof metrics[number] }) {
  const [selected, setSelected] = useState(1439);
  const [key, label, unit] = metric;
  const ceiling = key === 'cpu_percent' ? 100 : Math.max(1, ...minutes.map(p => p.maximum?.[key] ?? 0));
  const x = (i: number) => 56 + i / Math.max(1, minutes.length - 1) * 828;
  const y = (value: number) => 210 - value / ceiling * 180;
  function line(which: 'average' | 'maximum') {
    let interrupted = true;
    return minutes.map((point, i) => {
      const value = point[which]?.[key];
      if (value == null) { interrupted = true; return ''; }
      const command = `${interrupted ? 'M' : 'L'}${x(i)},${y(value)}`;
      interrupted = false;
      return command;
    }).join(' ');
  }
  const point = minutes[selected];
  return <>
    <div className="probe-chart-legend"><span className="probe-average">● 分钟平均值</span><span className="probe-maximum">● 分钟最大值</span><Typography.Text type="secondary">缺测保留空白</Typography.Text></div>
    <svg className="probe-chart" viewBox="0 0 920 245" role="img" aria-label={`${label}最近 24 小时分钟曲线，橙色为平均值，蓝色为最大值，缺测不连线`} onPointerMove={e => {
      const bounds = e.currentTarget.getBoundingClientRect();
      setSelected(Math.max(0, Math.min(minutes.length - 1, Math.round(((e.clientX - bounds.left) / bounds.width * 920 - 56) / 828 * (minutes.length - 1)))));
    }}>
      {[0, .5, 1].map(f => <g key={f}><line x1="56" x2="884" y1={y(ceiling * f)} y2={y(ceiling * f)} stroke="var(--line)" /><text x="50" y={y(ceiling * f) + 4} textAnchor="end">{unit === 's' ? `${Math.round(ceiling * f / 3600)}h` : format(ceiling * f, unit)}</text></g>)}
      <path d={line('maximum')} fill="none" stroke="var(--info)" strokeWidth="1.7" />
      <path d={line('average')} fill="none" stroke="var(--accent)" strokeWidth="1.7" />
      {minutes.map((p, i) => <g key={p.minute}>{p.maximum?.[key] != null && <circle cx={x(i)} cy={y(p.maximum[key]!)} r="1.3" fill="var(--info)" />}{p.average?.[key] != null && <circle cx={x(i)} cy={y(p.average[key]!)} r="1.3" fill="var(--accent)" />}</g>)}
      {point && <line x1={x(selected)} x2={x(selected)} y1="25" y2="210" stroke="var(--muted)" strokeDasharray="3 3" />}
      <text x="56" y="237">{minutes[0] ? new Date(minutes[0].minute).toLocaleString() : ''}</text>
      <text x="884" y="237" textAnchor="end">{minutes.at(-1) ? new Date(minutes.at(-1)!.minute).toLocaleString() : ''}</text>
    </svg>
    <input className="probe-minute-slider" type="range" min={0} max={Math.max(0, minutes.length - 1)} value={selected} onChange={e => setSelected(Number(e.target.value))} aria-label="选择曲线分钟" />
    <div className="probe-point">{point && <Space wrap><Typography.Text>{new Date(point.minute).toLocaleString()}</Typography.Text><span>平均值 {format(point.average?.[key], unit)}</span><span>最大值 {format(point.maximum?.[key], unit)}</span><Typography.Text type="secondary">{point.counts?.[key] ?? 0} 个有效样本{point.samples === 0 ? ' · 缺测' : ''}</Typography.Text></Space>}</div>
  </>;
}

export function ProbeDetails({ node, onClose, onSessionExpired }: { node: Node; onClose: () => void; onSessionExpired: () => void }) {
  const [view, setView] = useState<ProbeView>();
  const [error, setError] = useState('');
  const [metric, setMetric] = useState<string>('cpu_percent');
  const expired = useRef(onSessionExpired);
  expired.current = onSessionExpired;
  useEffect(() => {
    let active = true;
    let running = false;
    setView(undefined); setError('');
    async function load() {
      if (running) return;
      running = true;
      try {
        const result = await api<ProbeView>(`/nodes/${node.id}/probe`);
        if (active) { setView(result); setError(''); }
      } catch (e) {
        if (active) {
          setView(undefined);
          if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
          else setError((e as Error).message);
        }
      } finally { running = false; }
    }
    void load();
    const timer = window.setInterval(() => void load(), 5000);
    return () => { active = false; window.clearInterval(timer); };
  }, [node.id]);
  const realtime = view?.realtime;
  return <Drawer open title={`${node.name} · 探针指标详情`} onClose={onClose} size={1000} className="servers-drawer">
    <Space wrap className="probe-status"><StatusDot tone={node.status === 'online' ? node.connected ? 'ok' : 'danger' : node.status === 'maintenance' ? 'warn' : node.status === 'offline' ? 'danger' : 'muted'}>{node.status_label}</StatusDot><Typography.Text type="secondary">{node.connected ? '控制连接正常' : '控制连接已中断'}</Typography.Text><StatusDot tone={view?.fresh ? 'info' : 'muted'}>{view?.fresh ? '实时采样' : '暂无实时数据'}</StatusDot></Space>
    {error && <Alert type="error" showIcon title={error} className="message" />}
    {!view && !error && <Spin description="正在读取探针数据" />}
    {view && <>
      <Typography.Paragraph type="secondary">每 5 秒采样；最近样本：{view.sampled_at ? new Date(view.sampled_at).toLocaleString() : '尚未采样'}。运行时长从服务器进程启动起算。</Typography.Paragraph>
      <Card title="实时资源" className="probe-card">
        <Descriptions column={{ xs: 1, sm: 2 }} items={metrics.map(([key, label, unit]) => ({ key, label, children: <span>{format(realtime?.[key], unit)}{key === 'memory_used_bytes' && <> / {format(realtime?.memory_total_bytes, 'B')}</>}{key === 'disk_used_bytes' && <> / {format(realtime?.disk_total_bytes, 'B')}</>}</span> }))} />
        <Typography.Text type="secondary">业务连接与隧道连接分别计数；系统 TCP 显示已建立连接总数（含控制连接）。</Typography.Text>
      </Card>
      <Card title="最近 24 小时" className="probe-card" extra={<Select aria-label="曲线指标" value={metric} onChange={setMetric} options={metrics.map(([value, label]) => ({ value, label }))} className="probe-metric-select" />}>
        <MinuteChart minutes={view.minutes} metric={metrics.find(m => m[0] === metric)!} />
        <Typography.Paragraph type="secondary">当前分钟持续更新；平均值只使用有效样本。离线和采集失败处留空，恢复后继续记录。</Typography.Paragraph>
      </Card>
    </>}
  </Drawer>;
}
