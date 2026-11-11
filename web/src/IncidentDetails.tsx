import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Descriptions, Space } from 'antd';
import { ArrowLeftOutlined, FileSearchOutlined, ReloadOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { PageHeader } from './PageHeader';
import { go, type Page } from './route';
import { displayRotationNotice, displaySystemText, handlingLabels, impactLabels, type Incident } from './Incidents';
import { TableCard } from './TableCard';
import { StatusDot } from './StatusDot';
import { ColorTag } from './ColorTag';
import { FormSection } from './FormSection';
import './OtherPages.css';

type Entry = { sequence: number; stage: string; message: string; source: string; version: number; occurred_at: string | null; observed_at: string };
type Timeline = { entries: Entry[]; next_before: number | null };
const routes: Record<string, Page> = { node: 'servers', group: 'server-groups', rule: 'ports', watch: 'dns-watch' };
const kinds: Record<string, string> = { node: '服务器', group: '服务器组', rule: '端口', watch: '解析监控' };

export function IncidentDetails({ id, onSessionExpired }: { id: string; onSessionExpired: () => void }) {
  const [incident, setIncident] = useState<Incident>();
  const [timeline, setTimeline] = useState<Timeline>();
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const request = useRef(0);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  async function load(before?: number) {
    const version = ++request.current; setLoading(true);
    try {
      const [item, entries] = await Promise.all([api<Incident>(`/incidents/${encodeURIComponent(id)}`), api<Timeline>(`/incidents/${encodeURIComponent(id)}/timeline?limit=50${before ? `&before=${before}` : ''}`)]);
      if (version === request.current) {
        setIncident(item);
        setTimeline(old => before && old ? { ...entries, entries: [...old.entries, ...entries.entries] } : entries);
        setError('');
      }
    } catch (e) {
      if (version === request.current) { setError((e as Error).message); if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current(); }
    } finally { if (version === request.current) setLoading(false); }
  }
  useEffect(() => { setIncident(undefined); setTimeline(undefined); void load(); return () => { request.current++; }; }, [id]);
  return <div className="zf-other-page">
    <PageHeader title={incident?.title || '故障详情'} description="时间线按记录顺序展示，分别保留事实发生时间与面板观察时间；历史缺口不会被补造。"><Button icon={<ArrowLeftOutlined />} onClick={() => go('incidents')}>返回故障列表</Button><Button icon={<ReloadOutlined />} loading={loading} onClick={() => void load()}>刷新</Button></PageHeader>
    {error && <Alert title={error} description="读取未成功，已显示的信息可能过期。" type="error" showIcon className="message" />}
    {incident?.sync_error && <Alert title={incident.sync_error} description={incident.synced_at ? `最近整理：${new Date(incident.synced_at).toLocaleString()}` : '等待整理'} type="warning" showIcon className="message" />}
    {incident && <>
      <section className="zf-other-card zf-other-detail">
        <FormSection title="故障信息" icon={<FileSearchOutlined />}>
        <Descriptions column={{ xs: 1, sm: 2 }} items={[
          { key: 'impact', label: '异常状态', children: <StatusDot tone={incident.impact === 'abnormal' ? 'danger' : incident.impact === 'unknown' ? 'warn' : 'muted'}>{impactLabels[incident.impact] || incident.impact}</StatusDot> },
          { key: 'handling', label: '处理状态', children: <StatusDot tone={incident.handling === 'manual' ? 'warn' : incident.handling === 'processing' ? 'info' : 'muted'}>{handlingLabels[incident.handling] || incident.handling}</StatusDot> },
          { key: 'start', label: '首次观察', children: new Date(incident.started_at).toLocaleString() },
          { key: 'updated', label: '最近更新', children: new Date(incident.updated_at).toLocaleString() },
          { key: 'closure', label: '结束方式', children: incident.closure || '未结束' },
          { key: 'duration', label: '持续时间', children: `${Math.max(0, Math.floor((new Date(incident.ended_at || incident.updated_at).getTime() - new Date(incident.started_at).getTime()) / 1000))} 秒（截至最近观察）` },
        ]} />
        {incident.reasons.map((reason, i) => <p className="zf-other-note" key={i}>{displaySystemText(reason)}</p>)}
        <Space wrap>{incident.refs.map(ref => <Button key={`${ref.kind}:${ref.id}`} disabled={!ref.exists || !routes[ref.kind]} onClick={() => go(routes[ref.kind], ref.id)}>{kinds[ref.kind] || ref.kind}：{ref.name}{!ref.exists && '（已删除）'}</Button>)}</Space>
        </FormSection>
      </section>
        <TableCard<Entry> title="处理过程" actions={<ColorTag>最新记录在前</ColorTag>} rowKey="sequence" dataSource={timeline?.entries} loading={loading} pagination={{ pageSize: 50, showSizeChanger: false }} scroll={{ x: 900 }} footer={() => <div className="zf-other-table-footer"><span className="muted num">已加载 {timeline?.entries.length ?? 0} 条</span><Button disabled={!timeline?.next_before} loading={loading} onClick={() => void load(timeline?.next_before || undefined)}>加载更早过程</Button></div>} columns={[
          { title: '发生 / 观察时间', render: (_, e) => <span className="num">{e.occurred_at ? new Date(e.occurred_at).toLocaleString() : '准确发生时间未知'}<span className="cell-sub">观察：{new Date(e.observed_at).toLocaleString()}</span></span> },
          { title: '阶段', dataIndex: 'stage', render: value => <ColorTag>{displaySystemText(value)}</ColorTag> }, { title: '说明', render: (_, e) => e.source.startsWith('换 IP 事件 ') ? displayRotationNotice(e.message) : displaySystemText(e.message, '', ['入口组策略决策', '入口动作已提交'].includes(e.stage)) },
          { title: '来源 / 版本', render: (_, e) => <>{displaySystemText(e.source)}<span className="cell-sub">{e.version ? `版本 ${e.version}` : '—'}</span></> },
        ]} />
    </>}
  </div>;
}
