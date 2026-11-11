export type EndpointGroup = { id: string; name: string; strategy: 'primary_backup'; members: string[] };
export type EndpointNode = { id: string; name: string; connected: boolean };
export type Endpoint = { node_id: string; address: string; listen_port: number; transport: 'wss'; server_name: string; certificate_pem: string };
export type EndpointHealth = { exit_id: string; state: string; consecutive_failures?: number; last_probe?: string; failure_class?: string; recovery_since_unix?: number; checked_at_unix?: number };
export type HealthReport = { fresh: boolean; sampled_at: string | null; exits: EndpointHealth[] };
export type EndpointOption = { id: string; name: string; address: string };

export function transferMembers(current: string[], ids: string[], direction: 'add' | 'remove'): string[] {
  return direction === 'add' ? [...new Set([...current, ...ids])] : current.filter(id => !ids.includes(id));
}

export function moveMembers(current: string[], ids: string[], direction: 'up' | 'down'): string[] {
  const next = [...current];
  const selected = new Set(ids);
  const step = direction === 'up' ? -1 : 1;
  for (let i = step < 0 ? 1 : next.length - 2; i >= 0 && i < next.length; i -= step) {
    const neighbor = i + step;
    if (neighbor >= 0 && neighbor < next.length && selected.has(next[i]) && !selected.has(next[neighbor])) {
      [next[i], next[neighbor]] = [next[neighbor], next[i]];
    }
  }
  return next;
}

export function healthStatus(fresh: boolean, state: string): { tone: 'ok' | 'warn' | 'danger' | 'muted'; label: string } {
  const label = ({ healthy: '健康', unhealthy: '故障', recovering: '恢复观察中', unknown: '待探测' } as Record<string, string>)[state] ?? '未知';
  return { tone: !fresh ? 'muted' : state === 'healthy' ? 'ok' : state === 'unhealthy' ? 'danger' : state === 'recovering' ? 'warn' : 'muted', label: `${fresh ? '' : '历史 · '}${label}` };
}

export function healthIsFresh(report: HealthReport | undefined, now: number): boolean {
  if (!report?.fresh || !report.sampled_at) return false;
  const age = now - Date.parse(report.sampled_at);
  return age >= 0 && age < 10000;
}
