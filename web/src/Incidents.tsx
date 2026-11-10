import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Form, Input, Select, Space, Table, Tag } from 'antd';
import { api, APIError } from './api';
import { PageHeader } from './PageHeader';
import { go } from './route';
import type { OperationsView } from './Operations';
import { IncidentDetails } from './IncidentDetails';

export type Incident = { synced_at?: string; sync_error?: string; id: string; kind: string; title: string; impact: string; handling: string; started_at: string; updated_at: string; ended_at: string | null; closure: string; refs: { kind: string; id: string; name: string; exists: boolean }[]; reasons: string[] };
export const incidentKinds: Record<string, string> = { offline: '控制离线', blocked: '被墙', rule: '配置、监听或隧道', dns: 'DNS' };
export const handlingLabels: Record<string, string> = { waiting: '等待条件', processing: '处理中', manual: '需要人工', ended: '已结束' };
export const impactLabels: Record<string, string> = { abnormal: '检测到转发异常', unknown: '影响待确认', cleared: '异常证据已解除', no_known_issue: '未发现转发异常' };
type Page = { incidents: Incident[]; next_before: string | null; synced_at: string | null; sync_error: string };

export function Incidents({ focus, operations, onSessionExpired }: { focus?: string; operations?: OperationsView; onSessionExpired: () => void }) {
  const [page, setPage] = useState<Page>();
  const [filters, setFilters] = useState<Record<string, string>>({});
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const request = useRef(0);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  async function load(before?: string) {
    const version = ++request.current; setLoading(true);
    try {
      const query = new URLSearchParams({ limit: '30', ...filters }); if (before) query.set('before', before);
      const result = await api<Page>(`/incidents?${query}`);
      if (version === request.current) { setPage(old => ({ ...result, incidents: before && old ? [...old.incidents, ...result.incidents] : result.incidents })); setError(''); }
    } catch (e) {
      if (version === request.current) { setError((e as Error).message); if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current(); }
    } finally { if (version === request.current) setLoading(false); }
  }
  useEffect(() => { if (!focus) void load(); return () => { request.current++; }; }, [filters, focus]);
  if (focus) return <IncidentDetails id={focus} onSessionExpired={onSessionExpired} />;
  return <>
    <PageHeader title="故障" description="按故障追查证据与处理分支；控制离线和 DNS 状态不能单独证明目标业务是否中断。"><Button loading={loading} onClick={() => void load()}>刷新</Button></PageHeader>
    <Form layout="inline" style={{ gap: 8, marginBottom: 16 }} onFinish={values => {
      const next: Record<string, string> = {};
      for (const [key, value] of Object.entries(values)) if (value) next[key] = key === 'from' || key === 'to' ? new Date(String(value)).toISOString() : String(value);
      setFilters(next);
    }}>
      <Form.Item name="status" label="状态"><Select allowClear style={{ width: 130 }} options={[{ value: 'open', label: '未结束' }, { value: 'manual', label: '需要人工' }, { value: 'waiting', label: '等待条件' }, { value: 'processing', label: '处理中' }, { value: 'ended', label: '已结束' }]} /></Form.Item>
      <Form.Item name="kind" label="类型"><Select allowClear style={{ width: 160 }} options={Object.entries(incidentKinds).map(([value, label]) => ({ value, label }))} /></Form.Item>
      <Form.Item name="node_id" label="节点"><Select allowClear showSearch optionFilterProp="label" style={{ width: 150 }} options={operations?.nodes.map(n => ({ value: n.id, label: n.name }))} /></Form.Item>
      <Form.Item name="group_id" label="入口组"><Select allowClear showSearch optionFilterProp="label" style={{ width: 150 }} options={operations?.groups.map(g => ({ value: g.id, label: g.name }))} /></Form.Item>
      <Form.Item name="rule_id" label="规则"><Select allowClear showSearch optionFilterProp="label" style={{ width: 150 }} options={operations?.rules.map(r => ({ value: r.id, label: r.name }))} /></Form.Item>
      <Form.Item name="watch_id" label="解析监控 ID"><Input allowClear /></Form.Item>
      <Form.Item name="from" label="开始"><Input type="datetime-local" /></Form.Item><Form.Item name="to" label="结束"><Input type="datetime-local" /></Form.Item>
      <Form.Item><Button type="primary" htmlType="submit">筛选</Button></Form.Item>
    </Form>
    {error && <Alert title={error} description="保留上次列表；筛选后的数据尚未成功读取时，请勿将旧列表当作新结果。" type="error" showIcon className="message" />}
    {page?.sync_error && <Alert title={page.sync_error} description={page.synced_at ? `最近整理：${new Date(page.synced_at).toLocaleString()}` : '等待首次整理'} type="warning" showIcon className="message" />}
    <div className="panel"><Table<Incident> rowKey="id" dataSource={page?.incidents} loading={loading} pagination={false} scroll={{ x: 800 }} columns={[
      { title: '故障', render: (_, r) => <Button type="link" onClick={() => go('incidents', r.id)}>{r.title}</Button> },
      { title: '类型', render: (_, r) => incidentKinds[r.kind] || r.kind },
      { title: '当前判断', render: (_, r) => impactLabels[r.impact] || r.impact },
      { title: '处理状态', render: (_, r) => <Tag color={r.handling === 'manual' ? 'orange' : undefined}>{handlingLabels[r.handling] || r.handling}</Tag> },
      { title: '首次观察 / 最近更新', render: (_, r) => <>{new Date(r.started_at).toLocaleString()}<span className="cell-sub">{new Date(r.updated_at).toLocaleString()}</span></> },
      { title: '结束方式', render: (_, r) => r.closure || '—' },
    ]} /><div className="panel-foot"><Space><Button disabled={!page?.next_before} loading={loading} onClick={() => void load(page?.next_before || undefined)}>加载更早故障</Button><span className="muted">{page?.incidents.length ?? 0} 条</span></Space></div></div>
  </>;
}
