import { healthIsFresh, healthStatus } from './EndpointData.ts';

export type ChainMember = { node_id: string; node_name: string; address_id: string; address: string; port: number };
export type ChainObservation = {
  from_node_id: string; to_node_id: string; address_id: string; endpoint_config_digest: string;
  chain_fingerprint: string; rule_id: string; generation: number; state: string; fresh: boolean; sampled_at: string;
};
export type ChainHop = { position: number; exit_group_id: string; name: string; members: ChainMember[]; observations: ChainObservation[] };
export type ForwardChain = { id: string; name: string; exit_group_ids: string[]; rule_count: number; hops: ChainHop[] };
export type ChainValues = { name: string; exit_group_ids: string[] };

export function addChainGroup(ids: string[], id: string): string[] {
  if (ids.includes(id)) throw new Error('同一端点组不能重复添加');
  return [...ids, id];
}

export function moveChainGroup(ids: string[], id: string, direction: 'up' | 'down'): string[] {
  const next = [...ids];
  const index = next.indexOf(id);
  const target = index + (direction === 'up' ? -1 : 1);
  if (index >= 0 && target >= 0 && target < next.length) [next[index], next[target]] = [next[target], next[index]];
  return next;
}

export function removeChainGroup(ids: string[], id: string): string[] {
  return ids.filter(value => value !== id);
}

export function chainFormValues(chain?: ChainValues): ChainValues {
  return { name: chain?.name ?? '', exit_group_ids: [...(chain?.exit_group_ids ?? [])] };
}

export function chainPayload(values: ChainValues): ChainValues {
  const name = values.name.trim();
  if (!name || new TextEncoder().encode(name).length > 200) throw new Error('转发链名称须为 1–200 字节');
  if (values.exit_group_ids.length < 2) throw new Error('转发链至少需要两个端点组');
  if (new Set(values.exit_group_ids).size !== values.exit_group_ids.length) throw new Error('同一端点组不能重复添加');
  return { name, exit_group_ids: [...values.exit_group_ids] };
}

export function chainSaveRequest(values: ChainValues, id?: string) {
  return { path: id ? `/forward-chains/${encodeURIComponent(id)}` : '/forward-chains', method: id ? 'PUT' : 'POST', body: chainPayload(values) };
}

export function chainPathLabel(chain: ForwardChain): string {
  return chain.exit_group_ids.map((id, index) => `${index + 1}. ${chain.hops.find(hop => hop.exit_group_id === id)?.name ?? id}`).join(' → ');
}

export function chainMatchesQuery(chain: ForwardChain, query: string): boolean {
  const text = [chain.id, chain.name, chainPathLabel(chain), ...chain.exit_group_ids,
    ...chain.hops.flatMap(hop => hop.members.map(member => `${member.node_id} ${member.node_name} ${member.address_id} ${member.address}:${member.port}`)),
  ].join(' ').toLocaleLowerCase();
  return text.includes(query.trim().toLocaleLowerCase());
}

// Preserve each rule/edge/configuration separately; a local handshake is never
// evidence that the suffix or the final target is reachable.
export function chainObservationStatus(observation: ChainObservation | undefined, now: number, hop?: ChainHop): ReturnType<typeof healthStatus> {
  if (!observation || (observation.state === 'unknown' && !observation.sampled_at)) return { tone: 'muted', label: '待观测' };
  if (hop && !observationMember(hop, observation)) return { tone: 'muted', label: '历史 · 未匹配当前候选地址' };
  const states: Record<string, { health: string; label: string }> = {
    ready: { health: 'healthy', label: '本边及后续路径就绪（未探测目标）' },
    edge_unhealthy: { health: 'unhealthy', label: '本边未就绪' },
    suffix_unknown: { health: 'recovering', label: '本边可达 · 后续路径未就绪' },
    grant_failed: { health: 'unhealthy', label: '授权应用失败' },
  };
  const state = states[observation.state] ?? { health: 'unknown', label: '状态未知' };
  const fresh = observation.state !== 'historical' && healthIsFresh({ fresh: observation.fresh, sampled_at: observation.sampled_at, exits: [] }, now);
  return { tone: healthStatus(fresh, state.health).tone, label: `${fresh ? '' : '历史 · '}${state.label}` };
}

export function observationMember(hop: ChainHop, observation: ChainObservation): ChainMember | undefined {
  return hop.members.find(member => member.node_id === observation.to_node_id && member.address_id === observation.address_id);
}
