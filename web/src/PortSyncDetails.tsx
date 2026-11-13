import { useEffect, useState } from 'react';
import { Button, Space, Table, Typography } from 'antd';
import { ChainSyncDetails, ConfigurationBlockers, RuleSyncDetails } from './ConfigSync';
import { configurationStatusTone, type Rule, type PathNode } from './RulesData';
import type { ForwardChain } from './ChainsData';
import { entryStatus, rulePathSteps, type EntryState } from './RulePathData';
import { StatusDot } from './StatusDot';
import { go } from './route';

export function PortSyncStatus({ status }: { status: string }) {
  return <StatusDot tone={configurationStatusTone(status)}>{status || '未同步'}</StatusDot>;
}

export function PortSyncDetails({ rule, entryName, chains, nodes }: { rule: Rule; entryName: string; chains: ForwardChain[]; nodes: PathNode[] }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => { const timer = window.setInterval(() => setNow(Date.now()), 1000); return () => window.clearInterval(timer); }, []);
  const names = new Map([...(rule.path?.entries ?? []), ...(rule.path?.hops ?? []).flatMap(hop => hop.members)].map(member => [member.node_id, member.node_name]));
  return <div className="ports-member-details">
    <p>完整定义路径（各组按配置顺序列出全部候选；链式按主备顺序，不代表当前连接所走的唯一服务器）</p>
    <ol className="ports-path">{rulePathSteps(rule, entryName, chains).map((step, i) => <li key={i}>{step}</li>)}</ol>
    <p>私网策略：{rule.allow_private ? '允许私网目标' : '仅公网目标'}。</p>
    {rule.chain_id && <p><Button type="link" onClick={() => go('chains', rule.chain_id)}>编辑共享转发链</Button>修改后影响所有引用端口。</p>}
    <p><PortSyncStatus status={rule.sync_status} /> · 备用升级后须重新持久应用并确认，才能重新加入候选。</p>
    <ConfigurationBlockers blockers={rule.configuration_blockers} />
    <p>入口业务监听与生效版本（仅实际入口）</p>
    <RuleSyncDetails members={rule.sync_members} />
    <p>逐入口检查（服务器组的 A1 / A2 分开核对）</p>
    <Table<EntryState> size="small" pagination={false} rowKey="node_id" dataSource={rule.entry_states ?? []} scroll={{ x: 650 }} locale={{ emptyText: '待观测，没有入口检查证据' }} columns={[
      { title: '入口服务器', render: (_, row) => <>{row.node_name || row.node_id}<span className="cell-sub mono">{row.node_id}</span></> },
      { title: '本入口就绪', render: (_, row) => { const status = entryStatus(row, now); return <StatusDot tone={status.tone}>{status.label}</StatusDot>; } },
      { title: '入口候选 / 原因', render: (_, row) => <Space orientation="vertical" size={2}><span>{entryStatus(row, now).tone === 'muted' ? '历史或未知候选，须重新检查' : row.candidate ? '入口候选' : '非入口候选'}</span>{[...(row.readiness_reasons ?? []), ...(row.candidate_reasons ?? [])].map((reason, i) => <span key={i}>{reason.code}：{reason.message}{reason.rule_id && `（端口 ${reason.rule_id}）`}</span>)}</Space> },
      { title: '检查时间', render: (_, row) => row.checked_at ? new Date(row.checked_at).toLocaleString() : '未采样' },
    ]} />
    {rule.chain_id && <><p>各跳服务器整体配置（版本与错误仅属于该服务器；本端口授权另见下方 grant）</p>
      <Table<PathNode> size="small" pagination={false} rowKey="id" dataSource={nodes.filter(node => rule.path?.hops.some(hop => hop.members.some(member => member.node_id === node.id)))} scroll={{ x: 700 }} columns={[
        { title: '服务器', render: (_, node) => <Button type="link" onClick={() => go('servers', node.id)}>{node.name} · {node.id}</Button> },
        { title: '控制 / 整体配置', render: (_, node) => <>{node.connected ? '控制已连接' : '控制失联，缓存业务待确认'}<span className="cell-sub">{node.config_status}</span></> },
        { title: '期望 / 实际版本', render: (_, node) => <span className="num">{node.desired_revision} / {node.applied_revision}</span> },
        { title: '服务器应用错误', render: (_, node) => node.apply_error ? <Typography.Text type="danger" className="ports-apply-error">{node.apply_error}</Typography.Text> : '—' },
      ]} />
    </>}
    {rule.chain_id && <><p>逐边 / grant 应用与确认（中转只接收授权，不创建入口业务监听）</p><ChainSyncDetails edges={rule.path?.edges ?? []} hops={rule.path?.hops} nodeName={id => names.get(id) || id} /></>}
    <p><Button type="link" onClick={() => go('incidents')}>查看故障与历史原因</Button>控制失联或证据过期表示待确认；缓存业务可能继续，不能据此认定专线中断。</p>
  </div>;
}
