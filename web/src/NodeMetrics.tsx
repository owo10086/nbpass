import type { ReactNode } from 'react';
import { Tooltip } from 'antd';
import { format } from './ProbeDetails';

// 节点列表里的探针实时值；节点离线或采样过期时为 null，缺失的指标直接省略。
export type NodeProbe = Partial<Record<string, number>> | null;

const missing = <span className="muted">—</span>;

export function memoryPercent(probe: NodeProbe) {
  const used = probe?.memory_used_bytes, total = probe?.memory_total_bytes;
  return used != null && total ? used / total * 100 : null;
}

export function networkRate(probe: NodeProbe) {
  const rx = probe?.network_receive_bytes_per_second, tx = probe?.network_transmit_bytes_per_second;
  return rx == null && tx == null ? null : (rx ?? 0) + (tx ?? 0);
}

function percent(used?: number, total?: number) {
  return used != null && total ? ` (${(used / total * 100).toFixed(1)}%)` : '';
}

function count(value?: number) {
  return value == null ? '—' : value.toLocaleString();
}

// 单元格只放一行主值，其余明细在悬停提示里按「名称 值」逐行列出。
function Detail({ rows, children }: { rows: [string, ReactNode][]; children: ReactNode }) {
  return <Tooltip placement="top" mouseEnterDelay={0.1} title={<dl className="metric-tip">
    {rows.map(([label, value]) => <div key={label}><dt>{label}</dt><dd className="num">{value}</dd></div>)}
  </dl>}>
    <span className="metric-cell" tabIndex={0}>{children}</span>
  </Tooltip>;
}

// 与 komari 一致：低于 60% 绿，60%–80% 橙，80% 及以上红。
function UsageBar({ value }: { value: number }) {
  const clamped = Math.min(100, Math.max(0, value));
  const level = clamped >= 80 ? 'danger' : clamped >= 60 ? 'warn' : 'ok';
  return <span className="usage">
    <span className="usage-track"><span className={`usage-fill ${level}`} style={{ width: `${clamped}%` }} /></span>
    <span className="num usage-value">{clamped.toFixed(1)}%</span>
  </span>;
}

export function CpuCell({ probe }: { probe: NodeProbe }) {
  if (probe?.cpu_percent == null) return missing;
  return <Detail rows={[
    ['CPU 使用率', `${probe.cpu_percent.toFixed(1)}%`],
    ['运行时长', probe.uptime_seconds == null ? '—' : uptime(probe.uptime_seconds)],
  ]}><UsageBar value={probe.cpu_percent} /></Detail>;
}

export function MemoryCell({ probe }: { probe: NodeProbe }) {
  const value = memoryPercent(probe);
  if (value == null) return missing;
  const disk = probe!.disk_used_bytes != null && probe!.disk_total_bytes != null;
  return <Detail rows={[
    ['内存', `${format(probe!.memory_used_bytes, 'B')} / ${format(probe!.memory_total_bytes, 'B')}`],
    ['磁盘', disk ? `${format(probe!.disk_used_bytes, 'B')} / ${format(probe!.disk_total_bytes, 'B')}${percent(probe!.disk_used_bytes, probe!.disk_total_bytes)}` : '—'],
  ]}><UsageBar value={value} /></Detail>;
}

export function NetworkCell({ probe }: { probe: NodeProbe }) {
  const total = networkRate(probe);
  if (total == null) return missing;
  return <Detail rows={[
    ['下行', format(probe!.network_receive_bytes_per_second, 'B/s')],
    ['上行', format(probe!.network_transmit_bytes_per_second, 'B/s')],
    ['合计', format(total, 'B/s')],
  ]}>
    <span className="num">
      <span className="metric-line"><span className="muted">↓</span> {format(probe!.network_receive_bytes_per_second, 'B/s')}</span>
      <span className="metric-line"><span className="muted">↑</span> {format(probe!.network_transmit_bytes_per_second, 'B/s')}</span>
    </span>
  </Detail>;
}

export function ConnectionsCell({ probe }: { probe: NodeProbe }) {
  if (probe?.business_connections == null) return missing;
  return <Detail rows={[
    ['业务连接', count(probe.business_connections)],
    ['隧道连接', count(probe.tunnel_connections)],
    ['系统 TCP', count(probe.system_tcp_connections)],
  ]}><span className="num">{count(probe.business_connections)}</span></Detail>;
}

// 节点进程运行时长，列表里只保留最大的两级单位。
export function uptime(seconds: number) {
  const days = Math.floor(seconds / 86400), hours = Math.floor(seconds % 86400 / 3600), minutes = Math.floor(seconds % 3600 / 60);
  if (days) return `${days} 天 ${hours} 小时`;
  if (hours) return `${hours} 小时 ${minutes} 分`;
  return `${minutes} 分钟`;
}
