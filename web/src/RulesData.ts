import type { SyncMember } from './ConfigSync';

export type RuleInput = {
  name: string; entry_group_id?: string; entry_node_id?: string; kind: 'direct' | 'tunnel';
  exit_group_id?: string; listen_port: number; target_address: string; target_family: 'ipv4';
  target_port: number; allow_private: boolean; enabled: boolean;
};
export type Rule = RuleInput & {
  id: string; sync_status: string; sync_members: SyncMember[];
  traffic?: { upload_bytes: number; download_bytes: number; active_connections: number | null };
};
export type Group = { id: string; name: string };
export type RuleFilters = { search?: string; line?: string; kind?: RuleInput['kind']; enabled?: boolean };
export type RuleFormValues = Omit<RuleInput, 'entry_group_id' | 'entry_node_id' | 'target_family' | 'listen_port'> & {
  line: string; listen_port?: number; random_port?: boolean;
};
type FilterableRule = Pick<Rule, 'id' | 'name' | 'entry_node_id' | 'entry_group_id' | 'kind' | 'enabled' | 'target_address' | 'target_port'>;

export function ruleLine(row: Pick<RuleInput, 'entry_node_id' | 'entry_group_id'>) {
  return row.entry_node_id ? `node:${row.entry_node_id}` : `group:${row.entry_group_id}`;
}

export function filterRules<T extends FilterableRule>(rows: T[], filters: RuleFilters, focus?: string): T[] {
  const query = filters.search?.trim().toLocaleLowerCase() ?? '';
  return rows.filter(row => (!focus || row.id === focus)
    && (!query || row.name.toLocaleLowerCase().includes(query) || `${row.target_address}:${row.target_port}`.toLocaleLowerCase().includes(query))
    && (!filters.line || ruleLine(row) === filters.line)
    && (!filters.kind || row.kind === filters.kind)
    && (filters.enabled === undefined || row.enabled === filters.enabled));
}

export function buildRuleInput(values: RuleFormValues): RuleInput & { random_port?: boolean } {
  return {
    name: values.name,
    entry_node_id: values.line.startsWith('node:') ? values.line.slice(5) : undefined,
    entry_group_id: values.line.startsWith('group:') ? values.line.slice(6) : undefined,
    kind: values.kind,
    exit_group_id: values.kind === 'tunnel' ? values.exit_group_id : undefined,
    listen_port: values.random_port ? 0 : values.listen_port ?? 0,
    random_port: values.random_port || undefined,
    target_address: values.target_address, target_family: 'ipv4', target_port: values.target_port,
    allow_private: values.allow_private, enabled: values.enabled,
  };
}
