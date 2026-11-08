import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Form, Input, Select, Space, Table, Tag, Typography } from 'antd';
import { api, APIError } from './api';

type EventRow = {
  sequence: number; node_id: string; node_name: string; event_id: string; type: string;
  source: string; occurred_at: string; received_at: string; config_revision: number; message: string;
  gap?: { count: number; from: string; to: string; reason: string };
};
type EventPage = { events: EventRow[]; next_before: number | null };
type Filters = { node_id?: string; type?: string; from?: string; to?: string };
const labels: Record<string, string> = { node_online: '节点上线', node_started: '节点启动', sync_failed: '同步失败', event_gap: '事件缺口' };
const date = (value: string) => new Date(value).toLocaleString();

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

  return <>
    <div className="nodes-heading"><div><Typography.Title level={2}>事件日志</Typography.Title><Typography.Paragraph type="secondary">按发生时间追查节点事件；离线补报保留原始发生时间，缺失记录会显示缺口。</Typography.Paragraph></div><Button loading={loading} onClick={() => void load()}>刷新</Button></div>
    <Card className="message">
      <Form layout="inline" onFinish={search} style={{ gap: 12 }}>
        <Form.Item name="node_id" label="节点"><Select allowClear showSearch optionFilterProp="label" placeholder="全部节点" style={{ minWidth: 170 }} options={nodes.map(node => ({ value: node.id, label: node.name }))} /></Form.Item>
        <Form.Item name="type" label="类型"><Select allowClear showSearch placeholder="全部类型" style={{ minWidth: 160 }} options={Object.entries(labels).map(([value, label]) => ({ value, label }))} /></Form.Item>
        <Form.Item name="from" label="开始"><Input type="datetime-local" step="1" /></Form.Item>
        <Form.Item name="to" label="结束"><Input type="datetime-local" step="1" /></Form.Item>
        <Form.Item><Button type="primary" htmlType="submit" loading={loading}>筛选</Button></Form.Item>
      </Form>
    </Card>
    {error && <Alert title={error} type="error" showIcon className="message" />}
    <Card styles={{ body: { padding: 0 } }}>
      <Table<EventRow> rowKey="sequence" dataSource={rows} loading={loading} pagination={false} scroll={{ x: 1100 }} locale={{ emptyText: '暂无符合条件的事件' }} columns={[
        { title: '发生时间', dataIndex: 'occurred_at', render: date, width: 185 },
        { title: '节点', key: 'node', render: (_, row) => row.node_name || row.node_id || '面板' },
        { title: '类型', dataIndex: 'type', render: (type: string) => <Tag color={type === 'event_gap' ? 'orange' : type === 'sync_failed' ? 'red' : 'blue'}>{labels[type] || type}</Tag> },
        { title: '详情', key: 'message', render: (_, row) => <Space orientation="vertical" size={2}><span>{row.message}</span>{row.gap && <Typography.Text type="warning">{row.gap.reason === 'overflow' ? '队列容量不足' : '超过补报期限'}，缺失 {row.gap.count} 条（{date(row.gap.from)} 至 {date(row.gap.to)}）</Typography.Text>}</Space> },
        { title: '配置版本', dataIndex: 'config_revision', width: 100 },
        { title: '来源 / 收到时间', key: 'received', width: 185, render: (_, row) => <Space orientation="vertical" size={0}><span>{row.source === 'panel' ? '面板' : '节点上报'}</span><Typography.Text type="secondary">{date(row.received_at)}</Typography.Text></Space> },
      ]} />
      <div style={{ padding: 16, textAlign: 'center' }}><Button disabled={next === null} loading={loading} onClick={() => next !== null && void load(next)}>{next === null ? '已显示全部事件' : '加载更早的记录'}</Button></div>
    </Card>
  </>;
}
