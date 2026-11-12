import { useEffect, useState } from 'react';
import { StatusDot } from './StatusDot';
import { edgeStatus, grantStatus, type ChainEdge } from './RulePathData';
import type { ChainHop } from './ChainsData';
import { Space, Table, Tag, Typography } from 'antd';
import { configurationBlockerText, configurationStatusTone, type ConfigurationBlocker } from './RulesData';

export type SyncMember = {
  configuration_blockers?: ConfigurationBlocker[];
  node_id: string; node_name: string; connected: boolean; listen_port: number;
  desired_revision: number; applied_revision: number; sync_status: string; apply_error: string;
};
export type GroupSyncMember = {
  node_id: string; node_name: string; connected: boolean; sync_status: string;
  rules: (SyncMember & { rule_id: string; rule_name: string })[];
};

export function SyncTag({ status }: { status: string }) {
  const colors = { ok: 'green', danger: 'red', warn: 'orange', muted: 'default' };
  return <Tag color={colors[configurationStatusTone(status)]}>{status}</Tag>;
}

export function ConfigurationBlockers({ blockers }: { blockers?: ConfigurationBlocker[] }) {
  if (!blockers?.length) return <>—</>;
  return <Space orientation="vertical" size={2}>{blockers.map((blocker, index) => <Typography.Text key={index} type="warning" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{configurationBlockerText(blocker)}</Typography.Text>)}</Space>;
}

export function RuleSyncDetails({ members }: { members: SyncMember[] }) {
  return <Table<SyncMember> size="small" pagination={false} rowKey="node_id" dataSource={members} scroll={{ x: 700 }} columns={[
    { title: '成员服务器', render: (_, row) => <Space orientation="vertical" size={0}><span>{row.node_name}</span><Typography.Text type="secondary">{row.connected ? '控制已连接' : '控制失联，实际状态待确认'}</Typography.Text></Space> },
    { title: '监听端口', dataIndex: 'listen_port' },
    { title: '期望 / 实际生效版本', render: (_, row) => `${row.desired_revision} / ${row.applied_revision}` },
    { title: '同步状态', render: (_, row) => <SyncTag status={row.sync_status} /> },
    { title: '需升级服务器', render: (_, row) => <ConfigurationBlockers blockers={row.configuration_blockers} /> },
    { title: '应用错误', render: (_, row) => row.apply_error ? <Typography.Text type="danger" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{row.apply_error}</Typography.Text> : '—' },
  ]} />;
}

export function GroupSyncDetails({ members }: { members: GroupSyncMember[] }) {
  return <Table<GroupSyncMember> size="small" pagination={false} rowKey="node_id" dataSource={members} columns={[
    { title: '成员服务器', dataIndex: 'node_name' },
    { title: '控制连接', render: (_, row) => row.connected ? '已连接' : '失联，待确认' },
    { title: '同步状态', render: (_, row) => <SyncTag status={row.sync_status} /> },
  ]} expandable={{ expandedRowRender: member => <Space orientation="vertical" style={{ width: '100%' }}>
    {member.rules.length === 0 && <Typography.Text type="secondary">本服务器组暂无转发端口。</Typography.Text>}
    {member.rules.map(rule => <div key={rule.rule_id} style={{ width: '100%' }}><Typography.Text strong>{rule.rule_name}</Typography.Text><RuleSyncDetails members={[rule]} /></div>)}
  </Space> }} />;
}

export function SyncHelp() {
  return <p className="footnote">展开行查看每个成员的实际生效版本与错误。失联成员显示待确认；部分端口成功时，服务器的已应用版本仍保留在上一次完整成功的版本。停用端口显示正常，表示服务器已确认撤销监听。</p>;
}

export function ChainSyncDetails({ edges, hops, nodeName = id => id }: { edges: ChainEdge[]; hops?: ChainHop[]; nodeName?: (id: string) => string }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => { const timer = window.setInterval(() => setNow(Date.now()), 1000); return () => window.clearInterval(timer); }, []);
  return <Table<ChainEdge> size="small" pagination={false} rowKey={edge => `${edge.rule_id}:${edge.hop_index}:${edge.from_node_id}:${edge.to_node_id}:${edge.address_id}:${edge.endpoint_config_digest}`}
    dataSource={edges} scroll={{ x: 950 }} locale={{ emptyText: '待观测，没有逐边 / grant 证据' }} columns={[
      { title: '实际边 / 所选地址', render: (_, edge) => <><span>第 {edge.hop_index} 跳 · {nodeName(edge.from_node_id)} → {nodeName(edge.to_node_id)}</span><span className="cell-sub mono">{edge.from_node_id} → {edge.to_node_id} · {edge.address_id}</span><span className="cell-sub mono">{hops?.find(hop => hop.position === edge.hop_index)?.members.filter(member => member.node_id === edge.to_node_id && member.address_id === edge.address_id).map(member => `${member.address}:${member.port}`).join('') || '地址见路径定义'}</span></> },
      { title: '本边 / 后续路径', render: (_, edge) => { const status = edgeStatus(edge, now, hops?.find(hop => hop.position === edge.hop_index)); return <><StatusDot tone={status.tone}>{status.label}</StatusDot><span className="cell-sub">{edge.observed_at ? new Date(edge.observed_at).toLocaleString() : '未采样'}</span></>; } },
      { title: '接收服务器的 grant 应用', render: (_, edge) => <><Typography.Text type={edge.apply_error ? 'danger' : undefined}>{edgeStatus(edge, now).tone === 'muted' && '历史 / 待确认报告 · '}{grantStatus(edge)}</Typography.Text><span className="cell-sub num">期望 / 实际版本 {edge.desired_revision} / {edge.applied_revision}</span><span className="cell-sub num">端口 / grant 代次 {edge.rule_generation} / {edge.grant_generation}</span></> },
      { title: '证据身份', render: (_, edge) => <div style={{ overflowWrap: 'anywhere' }}><span className="cell-sub mono">端口 {edge.rule_id} · 链 {edge.chain_id}</span><span className="cell-sub mono">端点摘要 {edge.endpoint_config_digest || '待确认'}</span><span className="cell-sub mono">链指纹 {edge.chain_fingerprint || '待确认'}</span></div> },
    ]} />;
}
