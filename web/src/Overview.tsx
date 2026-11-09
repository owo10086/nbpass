import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Skeleton } from 'antd';
import { CheckCircleOutlined, RightOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { eventLabels } from './Events';
import type { EntryMemberState } from './EntryReadiness';
import { go, type Page } from './route';
import { PageHeader } from './PageHeader';

export type OverviewNode = {
  id: string; name: string; status: string; status_label: string; connected: boolean;
  last_seen_at: string | null; maintenance_until: string | null; apply_error: string;
  version_mismatch: boolean; panel_version: string; upgrade?: { state: string; error?: string; manifest: { version: string } };
};
export type OverviewEntryGroup = { id: string; name: string; member_states: EntryMemberState[] | null };
type Rule = { id: string; enabled: boolean };
type EventRow = { sequence: number; node_id: string; node_name: string; type: string; occurred_at: string; message: string };

export type Tone = 'danger' | 'warn' | 'muted';
export type Attention = { key: string; tone: Tone; title: string; subject: string; detail: string[]; page: Page; focus?: string };

const time = (value: string) => new Date(value).toLocaleString();

// 只整理 API 已经给出的状态与原因，不在前端推算新的判定。
export function collectAttention(nodes: OverviewNode[], groups: OverviewEntryGroup[]): Attention[] {
  const items: Attention[] = [];
  for (const node of nodes) {
    const at = { subject: node.name, page: 'nodes' as const, focus: node.id };
    if (node.status === 'offline') items.push({ ...at, key: `offline:${node.id}`, tone: 'danger', title: node.status_label, detail: [node.last_seen_at ? `最近活性 ${time(node.last_seen_at)}` : '从未上报活性'] });
    if (node.status === 'online' && !node.connected) items.push({ ...at, key: `link:${node.id}`, tone: 'danger', title: '控制连接已中断', detail: node.last_seen_at ? [`最近活性 ${time(node.last_seen_at)}`] : [] });
    if (node.apply_error) items.push({ ...at, key: `apply:${node.id}`, tone: 'danger', title: '配置应用失败', detail: [node.apply_error] });
    if (node.upgrade && (node.upgrade.state === 'failed' || node.upgrade.state === 'rolled_back')) items.push({ ...at, key: `upgrade:${node.id}`, tone: 'warn', title: node.upgrade.state === 'failed' ? `升级至 ${node.upgrade.manifest.version} 失败` : `升级至 ${node.upgrade.manifest.version} 已回滚`, detail: node.upgrade.error ? [node.upgrade.error] : [] });
    else if (node.version_mismatch) items.push({ ...at, key: `version:${node.id}`, tone: 'warn', title: `与面板 ${node.panel_version} 版本不一致`, detail: [] });
    if (node.status === 'maintenance') items.push({ ...at, key: `maintenance:${node.id}`, tone: 'muted', title: node.status_label, detail: node.maintenance_until ? [`至 ${time(node.maintenance_until)}`] : [] });
  }
  // 待接入通常是批量添加后尚未安装，合并成一条，避免挤占真正的异常。
  const pending = nodes.filter(node => node.status === 'pending');
  if (pending.length) items.push({ key: 'pending', tone: 'muted', title: `${pending.length} 个节点待接入`, subject: pending.map(node => node.name).join('、'), detail: ['复制安装命令到服务器执行后上线'], page: 'nodes', focus: pending.length === 1 ? pending[0].id : undefined });
  for (const group of groups) {
    const waiting = (group.member_states ?? []).filter(member => member.readiness_status === 'not_ready');
    if (!waiting.length) continue;
    items.push({
      key: `entry:${group.id}`, tone: 'warn', title: `${waiting.length} 个成员未就绪`, subject: `入口组 ${group.name}`, page: 'entry-groups', focus: group.id,
      detail: waiting.flatMap(member => member.readiness_reasons.slice(0, 2).map(reason => `${member.node_name}：${reason.message}`)),
    });
  }
  const order: Record<Tone, number> = { danger: 0, warn: 1, muted: 2 };
  return items.sort((a, b) => order[a.tone] - order[b.tone]);
}

export function Overview({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [nodes, setNodes] = useState<OverviewNode[]>();
  const [groups, setGroups] = useState<OverviewEntryGroup[]>([]);
  const [rules, setRules] = useState<Rule[]>([]);
  const [events, setEvents] = useState<EventRow[]>([]);
  const [updated, setUpdated] = useState<Date>();
  const [error, setError] = useState('');
  const expired = useRef(onSessionExpired);
  expired.current = onSessionExpired;

  async function load() {
    try {
      const [n, g, r, e] = await Promise.all([
        api<{ nodes: OverviewNode[] }>('/nodes'),
        api<{ entry_groups: OverviewEntryGroup[] }>('/entry-groups'),
        api<{ rules: Rule[] }>('/rules'),
        api<{ events: EventRow[] }>('/events?limit=12'),
      ]);
      setNodes(n.nodes); setGroups(g.entry_groups); setRules(r.rules); setEvents(e.events); setUpdated(new Date()); setError('');
    } catch (e) {
      if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
      else setError((e as Error).message);
    }
  }

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 10_000);
    return () => window.clearInterval(timer);
  }, []);

  const attention = nodes ? collectAttention(nodes, groups) : [];
  const urgent = attention.filter(item => item.tone !== 'muted').length;
  const online = nodes?.filter(node => node.status === 'online').length ?? 0;
  const readyGroups = groups.filter(group => (group.member_states ?? []).length > 0 && group.member_states!.every(member => member.ready)).length;
  const enabledRules = rules.filter(rule => rule.enabled).length;

  return <>
    <PageHeader title="概览" description="哪里异常、自动处理做了什么、结果如何。">
      <span className="muted">{updated ? <>每 10 秒刷新 · 更新于 <span className="num">{updated.toLocaleTimeString()}</span></> : '正在读取'}</span>
    </PageHeader>
    {error && <Alert title={error} type="error" showIcon className="message" action={<Button size="small" onClick={() => void load()}>重试</Button>} />}
    <div className="stats">
      <button type="button" className="stat" onClick={() => go('nodes')}><div className="stat-label"><span className={`dot ${nodes && online === nodes.length && nodes.length ? 'ok' : ''}`} />节点在线</div><div className="stat-value">{nodes ? <>{online}<small>/ {nodes.length}</small></> : '—'}</div></button>
      <button type="button" className="stat" onClick={() => document.getElementById('attention')?.scrollIntoView({ behavior: 'smooth' })}><div className="stat-label"><span className={`dot ${!nodes ? '' : urgent ? 'danger' : 'ok'}`} />需要处理</div><div className="stat-value">{nodes ? urgent : '—'}</div></button>
      <button type="button" className="stat" onClick={() => go('entry-groups')}><div className="stat-label">入口组就绪</div><div className="stat-value">{nodes ? <>{readyGroups}<small>/ {groups.length}</small></> : '—'}</div></button>
      <button type="button" className="stat" onClick={() => go('rules')}><div className="stat-label">转发规则启用</div><div className="stat-value">{nodes ? <>{enabledRules}<small>/ {rules.length}</small></> : '—'}</div></button>
    </div>
    <div className="overview-grid">
      <section className="panel" id="attention">
        <div className="panel-head"><h2>需要关注</h2><span className="muted num">{attention.length ? `${attention.length} 项` : ''}</span></div>
        {!nodes ? <div style={{ padding: 16 }}><Skeleton active paragraph={{ rows: 4 }} title={false} /></div>
          : attention.length === 0 ? <div className="all-clear"><CheckCircleOutlined style={{ color: 'var(--ok)', fontSize: 16 }} />所有节点在线，入口组成员均已就绪。</div>
          : <ul className="attention">{attention.map(item => <li key={item.key}>
            <button type="button" className="attention-item" onClick={() => go(item.page, item.focus)}>
              <span className={`dot ${item.tone}`} />
              <span><span className="attention-title">{item.title}</span> <span className="attention-subject">· {item.subject}</span>
                {item.detail.length > 0 && <ul className="attention-detail">{item.detail.map((line, i) => <li key={i}>{line}</li>)}</ul>}</span>
              <span className="attention-go">{item.page === 'nodes' ? '查看节点' : '查看入口组'} <RightOutlined style={{ fontSize: 10 }} /></span>
            </button>
          </li>)}</ul>}
      </section>
      <section className="panel">
        <div className="panel-head"><h2>最近事件</h2><Button type="link" size="small" onClick={() => go('events')}>全部事件</Button></div>
        {!nodes ? <div style={{ padding: 16 }}><Skeleton active paragraph={{ rows: 5 }} title={false} /></div>
          : events.length === 0 ? <div className="all-clear">暂无事件。节点上线、被墙判定、换 IP 等动作会记录在这里。</div>
          : <ul className="events-list">{events.map(event => <li key={event.sequence}>
            <time dateTime={event.occurred_at} title={time(event.occurred_at)}>{new Date(event.occurred_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</time>
            <div><span className="what">{eventLabels[event.type] ?? event.type}</span> <span className="where">· {event.node_name || '面板'}</span><div className="where">{event.message}</div></div>
          </li>)}</ul>}
      </section>
    </div>
  </>;
}
