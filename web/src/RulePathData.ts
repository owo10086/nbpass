import type { ChainHop, ForwardChain } from './ChainsData';
import { chainObservationStatus } from './ChainsData.ts';
import type { RuleInput } from './RulesData';

export type ChainEdge = {
  chain_id: string; chain_fingerprint: string; rule_id: string; from_node_id: string; to_node_id: string;
  address_id: string; endpoint_config_digest: string; hop_index: number; rule_generation: number; grant_generation: number;
  desired_revision: number; applied_revision: number; apply_error?: string;
  state: string; fresh: boolean; local_ready: boolean; suffix_ready: boolean; observed_at: string | null;
};
export type ChainFailure = Pick<ChainEdge, 'from_node_id' | 'hop_index' | 'rule_generation' | 'grant_generation' | 'chain_fingerprint'> & { to_node_id?: string; address_id?: string; endpoint_config_digest?: string };
export type RulePath = { entries: { node_id: string; node_name: string }[]; hops: ChainHop[]; edges: ChainEdge[] | null; target: { address: string; port: number } };
export type EntryReason = { code: string; message: string; rule_id?: string };
export type EntryState = {
  node_id: string; node_name: string; checked_at: string | null; ready: boolean; readiness_status: string;
  readiness_reasons: EntryReason[]; candidate: boolean; candidate_status: string; candidate_reasons: EntryReason[];
  desired_revision: number; applied_revision: number;
};

export function rulePathSteps(row: Pick<RuleInput, 'kind' | 'chain_id' | 'exit_group_id' | 'target_address' | 'target_port'> & { path?: RulePath }, entryName: string, chains: ForwardChain[] = []): string[] {
  const entries = row.path?.entries.map(entry => entry.node_name || entry.node_id) ?? [];
  const steps = [`入口 · ${entryName}${entries.length ? `（${entries.join(' / ')}）` : ''}`];
  const hops = row.path?.hops ?? chains.find(chain => chain.id === row.chain_id)?.hops ?? [];
  if (row.kind === 'tunnel' || row.chain_id) {
    if (!hops.length) steps.push(row.chain_id ? `链引用待确认 · ${row.chain_id}` : `端点组引用待确认 · ${row.exit_group_id || '未选择'}`);
    for (const hop of hops) steps.push(`第 ${hop.position} 跳 · ${hop.name || hop.exit_group_id}（${hop.members.map((member, i) => `${row.chain_id ? (i ? `备${i}` : '主') : `候选${i + 1}`} · ${member.node_name || member.node_id} · ${member.address_id} · ${member.address}:${member.port}`).join(' / ') || '成员待确认'}）`);
  }
  steps.push(`最终目标 · ${row.path?.target.address ?? row.target_address}:${row.path?.target.port ?? row.target_port}`);
  return steps;
}

export function edgeStatus(edge: ChainEdge | undefined, now: number, hop?: ChainHop) {
  return chainObservationStatus(edge ? { ...edge, generation: edge.rule_generation, sampled_at: edge.observed_at ?? '' } : undefined, now, hop);
}

// The API does not expose an independent confirmation boolean. Only the ready
// states prove the persisted grant was checked; revisions alone do not.
export function grantStatus(edge: ChainEdge): string {
  if (edge.apply_error) return `授权应用失败：${edge.apply_error}`;
  if (!edge.grant_generation || edge.grant_generation !== edge.rule_generation) return '待重新应用并确认（升级后也须重新确认）';
  if (edge.state === 'ready' || edge.state === 'suffix_unknown') return '授权已持久应用并确认（按本次服务端证据）';
  return '已有应用报告，待确认当前授权';
}

export function entryStatus(entry: EntryState | undefined, now: number): { tone: 'ok' | 'warn' | 'muted'; label: string } {
  if (!entry?.checked_at) return { tone: 'muted', label: '待观测' };
  const age = now - Date.parse(entry.checked_at);
  if (!Number.isFinite(age) || age < 0 || age > 10_000) return { tone: 'muted', label: '历史 · 入口检查已过期，业务状态待确认' };
  if (entry.readiness_status === 'unknown') return { tone: 'muted', label: '就绪待确认' };
  return entry.ready ? { tone: 'ok', label: '本入口就绪（未探测目标）' } : { tone: 'warn', label: '本入口未就绪' };
}

export function affectedOperations<T extends { members: string[]; path_nodes?: string[] }>(rules: T[], nodeID: string): T[] {
  return rules.filter(rule => (rule.path_nodes?.length ? rule.path_nodes : rule.members).includes(nodeID));
}
