import { useEffect, useRef, useState } from 'react';
import { api, APIError } from './api';
import type { Incident } from './Incidents';

export type OperationReason = { code: string; message: string; source: string; node_id?: string; observed_at: string | null };
export type OperationRule = { id: string; incident_id?: string; name: string; group_id?: string; node_id?: string; enabled: boolean; revision: number; status: string; reasons: OperationReason[]; members: string[] };
export type OperationGroup = { id: string; name: string; members: string[]; desired_node: string; desired_version: number; expected_ip: string; confirmed_ip: string; confirmed_node: string; confirmed_at: string | null; stage: string; propagation: string; watch_id: string; redundancy: string; reasons: OperationReason[]; bound: boolean };
export type OperationNode = { id: string; name: string; status: string; connected: boolean; last_seen: string | null; blocked: string; revision: number };
export type OperationsView = {
  current_incidents: Incident[]; recent_incidents: Incident[];
  generated_at: string; complete: boolean; nodes: OperationNode[]; groups: OperationGroup[]; rules: OperationRule[];
  counts: { nodes: number; entry: number; exit: number; rules: number; enabled_rules: number; abnormal_rules: number; unknown_rules: number; degraded_groups: number; manual_incidents: number | null; alerts: number };
  incident_sync: string | null; incident_error: string;
};
export type OperationsState = { data?: OperationsView; error: string };

// App 持有单份数据，侧栏与概览不分别推导告警。失败保留数据与原生成时间。
export function useOperations(enabled: boolean, onSessionExpired: () => void): OperationsState {
  const [state, setState] = useState<OperationsState>({ error: '' });
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  useEffect(() => {
    if (!enabled) { setState({ error: '' }); return; }
    let active = true;
    let loading = false;
    async function load() {
      if (loading) return;
      loading = true;
      try {
        const data = await api<OperationsView>('/overview');
        if (active) setState({ data, error: '' });
      } catch (e) {
        if (active) {
          if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
          setState(previous => ({ ...previous, error: (e as Error).message }));
        }
      } finally { loading = false; }
    }
    void load();
    const timer = window.setInterval(() => void load(), 10_000);
    return () => { active = false; window.clearInterval(timer); };
  }, [enabled]);
  return state;
}

export const operationLabels: Record<string, string> = { abnormal: '检测到转发异常', unknown: '影响待确认', no_known_issue: '未发现转发异常', disabled: '未启用' };
