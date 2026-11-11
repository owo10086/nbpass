import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Form, Input, Select, Space, Typography } from 'antd';
import { PageHeader } from './PageHeader';
import { api, APIError } from './api';
import { FilterCard } from './FilterCard';
import { TableCard } from './TableCard';
import { StatusDot } from './StatusDot';
import { displayRotationNotice, displaySystemText } from './Incidents';
import './OtherPages.css';

type EventRow = {
  sequence: number; node_id: string; node_name: string; event_id: string; type: string;
  source: string; occurred_at: string; received_at: string; config_revision: number; message: string;
  gap?: { count: number; from: string; to: string; reason: string };
};
type EventPage = { events: EventRow[]; next_before: number | null };
type Filters = { node_id?: string; type?: string; from?: string; to?: string };
const labels: Record<string, string> = { ip_rotation_authorized: '换 IP 已授权', ip_rotation_started: '换 IP 已开始', ip_rotation_observing: '换 IP 观察中', ip_rotation_changed: 'IP 已变化', ip_rotation_recovered: '新 IP 已恢复', ip_rotation_no_improvement: '换 IP 无改善', ip_rotation_pending: '新 IP 待确认', ip_rotation_paused: '无改善暂停', ip_rotation_resumed: '无改善暂停已解除', ip_rotation_unconfirmed: '换 IP 结果未确认', ip_rotation_cancelled: '换 IP 已取消', ip_rotation_released: '换 IP 冻结已解除', node_blocked: '被墙', node_unblocked: '解除疑似被墙或被墙', node_online: '服务器上线', node_offline: '服务器离线', node_recovered: '服务器恢复上线', node_started: '服务器启动', sync_failed: '同步失败', dns_watch_failed: '解析采样失败', dns_watch_mismatch: '解析期望不符', dns_watch_poisoned: '解析疑似污染', event_gap: '事件缺口' };
const date = (value: string) => new Date(value).toLocaleString();
Object.assign(labels, { entry_blocked_waiting: '被墙入口等待候选', entry_blocked_switch: '被墙入口已切换', ip_rotation_entry_waiting: '策略③等待恢复' });

function displayEventMessage(row: { type: string; message: string }): string {
  if (row.type.startsWith('ip_rotation_')) return displayRotationNotice(row.message);
  // 自测消息包含历史服务器名，按固定系统后缀识别，避免改写名称。
  if (row.type === 'node_blocked' || row.type === 'node_unblocked') {
    const named = row.message.match(/^([\s\S]*：)(节点(?:连续三轮自测异常，认定被墙（自测推定）|自测正常，已解除疑似被墙或被墙|连续三轮最终结果异常，认定被墙（含检测点补判）|最终结果正常，已解除疑似被墙或被墙（检测点补判）))$/);
    return named ? named[1] + displaySystemText(named[2]) : row.message;
  }
  const known = row.type.startsWith('node_') || row.type.startsWith('entry_') || ['sync_failed', 'event_gap'].includes(row.type);
  return known ? displaySystemText(row.message, '', row.type.startsWith('entry_')) : row.message;
}

export function Events({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [rows, setRows] = useState<EventRow[]>([]);
  const [nodes, setNodes] = useState<{ id: string; name: string }[]>([]);
  const [filters, setFilters] = useState<Filters>({});
  const [next, setNext] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const request = useRef(0);
  const expired = useRef(onSessionExpired);
  expired.current = onSessionExpired;

  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else setError((e as Error).message);
  }

  async function load(before?: number) {
    const version = ++request.current;
    setLoading(true);
    const query = new URLSearchParams({ limit: '100' });
    Object.entries(filters).forEach(([key, value]) => { if (value) query.set(key, value); });
    if (before) query.set('before', String(before));
    try {
      const result = await api<EventPage>(`/events?${query}`);
      if (version !== request.current) return;
      setRows(current => before ? [...current, ...result.events] : result.events);
      setNext(result.next_before); setError('');
    } catch (e) { if (version === request.current) failed(e); }
    finally { if (version === request.current) setLoading(false); }
  }

  useEffect(() => {
    let active = true;
    void api<{ nodes: { id: string; name: string }[] }>('/nodes').then(result => { if (active) setNodes(result.nodes); }).catch(e => { if (active) failed(e); });
    return () => { active = false; };
  }, []);
  useEffect(() => { void load(); return () => { request.current++; }; }, [filters]);

  function search(values: Filters) {
    const result = { ...values };
    for (const key of ['from', 'to'] as const) {
      if (result[key]) result[key] = new Date(result[key]!).toISOString();
    }
    setFilters(result);
  }

  return <div className="zf-other-page">
    <PageHeader title="事件日志" description="按发生时间追查服务器事件；离线补报保留原始发生时间，缺失记录会显示缺口。" />
      <Form className="zf-other-filters" layout="inline" colon={false} onFinish={search}>
      <FilterCard onRefresh={() => void load()} loading={loading}>
        <Form.Item name="node_id" label="服务器"><Select allowClear showSearch optionFilterProp="label" placeholder="全部服务器" options={nodes.map(node => ({ value: node.id, label: node.name }))} /></Form.Item>
        <Form.Item name="type" label="类型"><Select allowClear showSearch optionFilterProp="label" placeholder="全部类型" options={Object.entries(labels).map(([value, label]) => ({ value, label }))} /></Form.Item>
        <Form.Item name="from" label="开始"><Input type="datetime-local" step="1" /></Form.Item>
        <Form.Item name="to" label="结束"><Input type="datetime-local" step="1" /></Form.Item>
        <Form.Item><Button type="primary" htmlType="submit" loading={loading}>筛选</Button></Form.Item>
      </FilterCard>
      </Form>
    {error && <Alert title={error} type="error" showIcon className="message" />}
      <TableCard<EventRow> title="事件记录" rowKey="sequence" dataSource={rows} loading={loading} pagination={{ pageSize: 100, showSizeChanger: false }} scroll={{ x: 1080 }} locale={{ emptyText: '暂无符合条件的事件' }} footer={() => <div className="zf-other-table-footer"><span className="muted num">已加载 {rows.length} 条</span><Button disabled={next === null} loading={loading} onClick={() => next !== null && void load(next)}>{next === null ? '已显示全部事件' : '加载更早的记录'}</Button></div>} columns={[
        { title: '发生时间', dataIndex: 'occurred_at', render: value => <span className="num">{date(value)}</span>, width: 185 },
        { title: '服务器', key: 'node', render: (_, row) => row.node_name || row.node_id || '面板' },
        { title: '类型', dataIndex: 'type', render: (type: string) => <StatusDot tone={type === 'event_gap' ? 'warn' : type === 'sync_failed' ? 'danger' : 'muted'}>{labels[type] || type}</StatusDot> },
        { title: '详情', key: 'message', render: (_, row) => <Space orientation="vertical" size={2}><span>{displayEventMessage(row)}</span>{row.gap && <Typography.Text type="warning">{row.gap.reason === 'overflow' ? '队列容量不足' : '超过补报期限'}，缺失 {row.gap.count} 条（{date(row.gap.from)} 至 {date(row.gap.to)}）</Typography.Text>}</Space> },
        { title: '配置版本', dataIndex: 'config_revision', width: 100, render: value => <span className="mono">{value ?? '—'}</span> },
        { title: '来源 / 收到时间', key: 'received', width: 185, render: (_, row) => <>{row.source === 'panel' ? '面板' : '服务器上报'}<span className="cell-sub num">{date(row.received_at)}</span></> },
      ]} />
  </div>;
}

export { labels as eventLabels };
