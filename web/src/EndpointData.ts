export type MemberAddress = { node_id: string; address_id: string };
export type EndpointGroup = { id: string; name: string; strategy: 'primary_backup'; members: string[]; member_addresses: MemberAddress[] };
export type EndpointNode = { id: string; name: string; connected: boolean };
export type EndpointValues = { node_id: string; address: string; listen_port: number; public_port?: number | null };
export type Endpoint = { public_port: number | null; node_id: string; address: string; listen_port: number; transport: 'wss'; server_name: string; certificate_pem: string };
export type EndpointHealth = { exit_id: string; address_id?: string; endpoint_config_digest?: string; state: string; consecutive_failures?: number; last_probe?: string; failure_class?: string; recovery_since_unix?: number; checked_at_unix?: number };
export type HealthReport = { fresh: boolean; sampled_at: string | null; exits: EndpointHealth[] };
export type EndpointOption = MemberAddress & { id: string; name: string; address: string };

export function memberKey(member: MemberAddress): string {
  return JSON.stringify([member.node_id, member.address_id]);
}

export function endpointMemberOptions(exits: Endpoint[], nodes: EndpointNode[]): EndpointOption[] {
  return exits.map(exit => {
    const member = { node_id: exit.node_id, address_id: 'public' };
    return { ...member, id: memberKey(member), name: nodes.find(node => node.id === exit.node_id)?.name ?? exit.node_id, address: `${exit.address}:${effectiveEndpointPort(exit)}` };
  });
}

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

export function effectiveEndpointPort(endpoint: { public_port?: number | null; listen_port: number }): number {
  return endpoint.public_port ?? endpoint.listen_port;
}

export function endpointAddressDetails(endpoint: EndpointValues): { label: string; value: string }[] {
  return [
    { label: '连接地址', value: `${endpoint.address}:${effectiveEndpointPort(endpoint)}` },
    { label: '本机监听端口', value: String(endpoint.listen_port) },
  ];
}

export function endpointFormValues(endpoint?: EndpointValues) {
  return endpoint ? { ...endpoint, public_port: endpoint.public_port ?? null }
    : { address: '', listen_port: undefined, public_port: null };
}

export function endpointPayload(values: EndpointValues) {
  return {
    address: values.address.trim().replace(/\.$/, '').toLowerCase(),
    listen_port: values.listen_port, transport: 'wss', public_port: values.public_port ?? null,
  };
}
