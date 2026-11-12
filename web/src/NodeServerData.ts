import type { NodeRegionFields } from './NodeRegion';
import type { NodeUpgradeFields } from './NodeUpgrade';
import type { NodeRecord } from './NodeManagedRecord';
import type { NodeProbe } from './NodeMetrics';

export type ServerRow = NodeRegionFields & NodeUpgradeFields & {
  outbound_ipv4: string; id: string; name: string; status: string; status_label: string; connected: boolean;
  last_seen_at: string | null; maintenance_until: string | null;
  desired_revision: number; applied_revision: number; config_status: string; software_version: string; apply_error: string;
  probe: NodeProbe; confirmed_ipv4: string; managed_record: NodeRecord | null; record_status: string;
};
export type ServerGroup = { id: string; name: string; members: string[] };
export type ServerPort = { entry_node_id?: string; entry_group_id?: string; listen_port: number; enabled: boolean };
export type ServerFilter = { query: string; status: string; groupID: string };

export function serverGroupsFor(id: string, groups: ServerGroup[]): ServerGroup[] {
  return groups.filter(group => group.members.includes(id));
}

// 统计期望配置中的启用业务监听端口，不把隧道/检测预留端口算成业务端口。
export function serverPortCount(id: string, groups: ServerGroup[], ports: ServerPort[]): number {
  const groupIDs = new Set(serverGroupsFor(id, groups).map(group => group.id));
  return new Set(ports.filter(port => port.enabled && (port.entry_node_id === id || (!!port.entry_group_id && groupIDs.has(port.entry_group_id)))).map(port => port.listen_port)).size;
}

export function matchesServer(server: Pick<ServerRow, 'id' | 'name' | 'public_ipv4' | 'confirmed_ipv4' | 'managed_record' | 'status'>, groups: ServerGroup[], filter: ServerFilter): boolean {
  if (filter.status !== 'all' && server.status !== filter.status) return false;
  const memberships = serverGroupsFor(server.id, groups);
  if (filter.groupID === 'ungrouped' ? memberships.length > 0 : filter.groupID !== 'all' && !memberships.some(group => group.id === filter.groupID)) return false;
  const needle = filter.query.trim().toLowerCase();
  return !needle || [server.name, server.public_ipv4, server.confirmed_ipv4, server.managed_record?.hostname].some(value => value?.toLowerCase().includes(needle));
}

export function serverNamePayload(values: { name: string }): { name: string } { return { name: values.name }; }
export function serverOutboundPayload(value: string): { outbound_ipv4: string } { return { outbound_ipv4: value.trim() }; }
