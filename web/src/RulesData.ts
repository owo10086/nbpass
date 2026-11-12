import type { SyncMember } from './ConfigSync';
import type { RulePath, EntryState } from './RulePathData';

export type ConfigurationBlocker = { node_id: string; node_name: string; address_id: string; capability: string; reason: string };

export function configurationBlockerText(blocker: ConfigurationBlocker): string {
  return `${blocker.node_name || blocker.node_id}${blocker.address_id ? ` · ${blocker.address_id}` : ''}：${blocker.capability}；${blocker.reason}`;
}

export function configurationStatusTone(status: string): 'ok' | 'danger' | 'warn' | 'muted' {
  if (status === '正常') return 'ok';
  if (status === '同步失败' || status === '整条阻塞（需升级服务器）') return 'danger';
  if (status === '待确认' || status.includes('备用成员能力不足')) return 'warn';
  return 'muted';
}

export type RuleInput = {
  name: string; entry_group_id?: string; entry_node_id?: string; kind: 'direct' | 'tunnel';
  exit_group_id?: string; chain_id?: string; listen_port: number; target_address: string; target_family: 'ipv4';
  target_port: number; allow_private: boolean; enabled: boolean;
};
export type Rule = RuleInput & {
  configuration_blockers?: ConfigurationBlocker[];
  path?: RulePath; entry_states?: EntryState[];
  id: string; sync_status: string; sync_members: SyncMember[];
  traffic?: { upload_bytes: number; download_bytes: number; active_connections: number | null };
};
export type Group = { id: string; name: string };
export type PathNode = Group & { connected: boolean; config_status: string; desired_revision: number; applied_revision: number; apply_error: string };
export type RuleMode = 'direct' | 'single' | 'chain';
export const ruleModeLabels: Record<RuleMode, string> = { direct: '直连', single: '单跳转发', chain: '链式转发' };
export type RuleFilters = { search?: string; line?: string; kind?: RuleInput['kind']; mode?: RuleMode; enabled?: boolean };
export type RuleDetailSort = 'id-ascend' | 'id-descend' | 'sync-ascend' | 'sync-descend';
export type RuleFormValues = Omit<RuleInput, 'entry_group_id' | 'entry_node_id' | 'target_family' | 'listen_port' | 'kind'> & {
  line: string; mode?: RuleMode; kind?: RuleInput['kind']; listen_port?: number; random_port?: boolean;
};
type FilterableRule = Pick<Rule, 'id' | 'name' | 'entry_node_id' | 'entry_group_id' | 'kind' | 'chain_id' | 'enabled' | 'target_address' | 'target_port'>;

export function ruleLine(row: Pick<RuleInput, 'entry_node_id' | 'entry_group_id'>) {
  return row.entry_node_id ? `node:${row.entry_node_id}` : `group:${row.entry_group_id}`;
}

export function filterRules<T extends FilterableRule>(rows: T[], filters: RuleFilters, focus?: string): T[] {
  const query = filters.search?.trim().toLocaleLowerCase() ?? '';
  return rows.filter(row => (!focus || row.id === focus)
    && (!query || row.name.toLocaleLowerCase().includes(query) || `${row.target_address}:${row.target_port}`.toLocaleLowerCase().includes(query))
    && (!filters.line || ruleLine(row) === filters.line)
    && (!filters.kind || row.kind === filters.kind)
    && (!filters.mode || ruleMode(row) === filters.mode)
    && (filters.enabled === undefined || row.enabled === filters.enabled));
}

export function sortRuleDetails<T extends Pick<Rule, 'id' | 'sync_status'>>(rows: T[], sort?: RuleDetailSort): T[] {
  if (!sort) return rows;
  const key = sort.startsWith('id-') ? 'id' : 'sync_status';
  const direction = sort.endsWith('ascend') ? 1 : -1;
  return [...rows].sort((a, b) => direction * a[key].localeCompare(b[key], 'zh-CN', { numeric: true }));
}

export function ruleMode(row: Pick<RuleInput, 'kind' | 'chain_id'>): RuleMode {
  return row.chain_id ? 'chain' : row.kind === 'tunnel' ? 'single' : 'direct';
}

// Editing, copying and enable/disable all use this same definition-only path.
export function ruleFormValues(row: RuleInput): RuleFormValues {
  return { name: row.name, line: ruleLine(row), mode: ruleMode(row),
    exit_group_id: row.exit_group_id, chain_id: row.chain_id, listen_port: row.listen_port,
    random_port: false, target_address: row.target_address, target_port: row.target_port,
    allow_private: row.allow_private, enabled: row.enabled };
}

export function buildRuleInput(values: RuleFormValues): RuleInput & { random_port?: boolean } {
  const mode = values.mode ?? ruleMode({ kind: values.kind ?? 'direct', chain_id: values.chain_id });
  return {
    name: values.name,
    entry_node_id: values.line.startsWith('node:') ? values.line.slice(5) : undefined,
    entry_group_id: values.line.startsWith('group:') ? values.line.slice(6) : undefined,
    kind: mode === 'direct' ? 'direct' : 'tunnel',
    exit_group_id: mode === 'single' ? values.exit_group_id : undefined,
    chain_id: mode === 'chain' ? values.chain_id : undefined,
    listen_port: values.random_port ? 0 : values.listen_port ?? 0,
    random_port: values.random_port || undefined,
    target_address: values.target_address, target_family: 'ipv4', target_port: values.target_port,
    allow_private: values.allow_private, enabled: values.enabled,
  };
}

// Polling never supersedes an in-flight read. Explicit reloads after mutations
// coalesce into a trailing read; their callers wait until that read completes.
export function createRuleReloader<T>(read: () => Promise<T>, callbacks: {
  apply: (value: T) => void; failed: (error: unknown) => void; loading: (value: boolean) => void;
}) {
  let disposed = false;
  let pending = false;
  let running: Promise<void> | undefined;
  function refresh(): Promise<void> {
    if (disposed) return Promise.resolve();
    if (running) { pending = true; return running; }
    callbacks.loading(true);
    running = Promise.resolve().then(async () => {
      try {
        if (disposed) return;
        do {
          pending = false;
          try {
            const value = await read();
            if (!disposed && !pending) callbacks.apply(value);
          } catch (error) {
            // In particular, a slow 401/428 still reaches session handling even
            // when a mutation requested another read in the meantime.
            if (!disposed) callbacks.failed(error);
          }
        } while (pending && !disposed);
      } finally {
        running = undefined;
        if (!disposed) callbacks.loading(false);
      }
    });
    return running;
  }
  return {
    refresh,
    poll: () => { if (!disposed && !running) void refresh(); },
    dispose: () => { disposed = true; pending = false; },
  };
}
