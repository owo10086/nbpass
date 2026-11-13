import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Empty, Select } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { FilterField } from './FilterCard';
import { StatusDot } from './StatusDot';
import { TableCard } from './TableCard';
import { endpointMemberOptions, memberKey, healthIsFresh, healthStatus, type Endpoint, type EndpointHealth as Health, type EndpointNode, type HealthReport } from './EndpointData';
import './Endpoints.css';

export function EndpointHealth({ nodes, exits, onSessionExpired }: { nodes: EndpointNode[]; exits: Endpoint[]; onSessionExpired: () => void }) {
  const [entry, setEntry] = useState<string>();
  const [health, setHealth] = useState<HealthReport>();
  const [error, setError] = useState('');
  const [now, setNow] = useState(Date.now);
  const [refreshKey, setRefreshKey] = useState(0);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  useEffect(() => {
    setHealth(undefined); setError('');
    if (!entry) return;
    let active = true;
    let pending = false;
    let timeout: number | undefined;
    async function refresh() {
      if (pending) return;
      pending = true;
      try {
        const report = await Promise.race([
          api<HealthReport>(`/nodes/${entry}/exit-health`),
          new Promise<never>((_, reject) => { timeout = window.setTimeout(() => reject(new Error('健康观测请求超时，请刷新重试')), 10000); }),
        ]);
        if (active) { setHealth(report); setNow(Date.now()); setError(''); }
      } catch (e) {
        if (active) {
          if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
          else setError((e as Error).message);
        }
      } finally { window.clearTimeout(timeout); pending = false; }
    }
    void refresh();
    const timer = window.setInterval(() => void refresh(), 5000);
    const aging = window.setInterval(() => setNow(Date.now()), 1000);
    return () => { active = false; window.clearInterval(timer); window.clearInterval(aging); window.clearTimeout(timeout); };
  }, [entry, refreshKey]);
  const nodeName = (id: string) => nodes.find(node => node.id === id)?.name ?? id;
  const fresh = healthIsFresh(health, now);
  const options = endpointMemberOptions(exits, nodes);
  const identity = (row: Health) => ({ node_id: row.exit_id, address_id: row.address_id || 'public' });
  const notes = <>
    {entry && error && <Alert type="error" title={error} showIcon className="message" />}
    {entry && health && !fresh && <Alert type="warning" title="当前无实时证据，以下为最近观测，不代表当前健康状态。" showIcon className="message" />}
    <p className="endpoint-health-note">选择入口服务器查看它到各端点的观测，每 5 秒刷新。目标不可达不影响端点健康；控制失联后服务器仍自行探测和切换。
      {entry && health?.sampled_at && <> 最近观测：<span className="mono">{new Date(health.sampled_at).toLocaleString()}</span></>}
    </p>
  </>;
  return <section className="endpoint-health" aria-label="端点链路健康">
    <TableCard<Health> title="端点链路健康" actions={<div className="endpoint-health-controls">
      <FilterField label="观测入口" htmlFor="endpoint-health-entry"><Select id="endpoint-health-entry" showSearch optionFilterProp="label" allowClear placeholder="选择入口服务器" value={entry} onChange={setEntry} options={nodes.map(node => ({ value: node.id, label: node.name }))} /></FilterField>
      {entry && <Button icon={<ReloadOutlined />} onClick={() => setRefreshKey(key => key + 1)}>刷新</Button>}
    </div>} rowKey={row => memberKey(identity(row))} dataSource={health?.exits ?? []} loading={!!entry && !health && !error} pagination={false} scroll={{ x: 900 }} footer={() => notes} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="尚无端点探测结果" /> }} columns={[
      { title: '端点', render: (_, row) => <><span className="node-name">{options.find(option => option.id === memberKey(identity(row)))?.name ?? nodeName(row.exit_id)}</span><span className="cell-sub mono">{options.find(option => option.id === memberKey(identity(row)))?.address ?? row.address_id}</span></> },
      { title: '链路状态', render: (_, row) => { const status = healthStatus(fresh, row.state); return <StatusDot tone={status.tone}>{status.label}</StatusDot>; } },
      { title: '连续失败', render: (_, row) => <span className="mono">{row.consecutive_failures ?? 0}</span> },
      { title: '最近探测', render: (_, row) => ({ success: '成功', failure: '失败', incomplete: '未完成' } as Record<string, string>)[row.last_probe ?? ''] ?? '待探测' },
      { title: '故障归属', render: (_, row) => ({ entry_exit: '入口→端点', exit_target: '端点→目标', local_resource: '本地资源', configuration_authorization: '配置/授权' } as Record<string, string>)[row.failure_class ?? ''] ?? '—' },
      { title: '恢复观察起点', render: (_, row) => row.recovery_since_unix ? <span className="mono">{new Date(row.recovery_since_unix * 1000).toLocaleTimeString()}</span> : '—' },
      { title: '探测时间', render: (_, row) => row.checked_at_unix ? <span className="mono">{new Date(row.checked_at_unix * 1000).toLocaleTimeString()}</span> : '—' },
    ]}>
      {!entry ? <div className="endpoint-health-placeholder"><p className="endpoint-health-prompt">选择观测入口后显示它到各端点的链路状态</p>{notes}</div> : undefined}
    </TableCard>
  </section>;
}
