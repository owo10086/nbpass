import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Modal, Space, Switch, Table, Tabs, Typography } from 'antd';
import { CloudSyncOutlined, EyeOutlined, ReloadOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { AllPublishDNS } from './AllPublishDNS';
import { ManagedDNS } from './ManagedDNS';
import { EntryReadiness } from './EntryReadiness';
import type { EntryGroup } from './EntryGroups';
import type { GroupSyncMember, SyncMember } from './ConfigSync';
import { ColorTag } from './ColorTag';
import { FormSection } from './FormSection';
import { StatusDot } from './StatusDot';
import { TableCard } from './TableCard';
import './EntryGroups.css';

type Node = { id: string; name: string };

export function EntryDNSStatus({ group, nodes, onClose, onSessionExpired, onRefresh, onObserve, loading, observing }: {
  group: EntryGroup; nodes: Node[]; onClose: () => void; onSessionExpired: () => void;
  onRefresh: () => Promise<void>; onObserve: (enabled: boolean) => Promise<void>; loading: boolean; observing: boolean;
}) {
  const [refreshToken, setRefreshToken] = useState(0);
  const [error, setError] = useState('');
  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) onSessionExpired();
    else setError((e as Error).message);
  }
  return <Modal open className="sg-modal" title="服务器组 DNS 状态" width={1080} onCancel={onClose} footer={<Button onClick={onClose}>关闭</Button>}>
    <div className="sg-dialog-heading"><strong>{group.name}</strong><ColorTag tone={group.publication_mode === 'all' ? 'blue' : 'purple'}>{group.publication_mode === 'all' ? '全部发布' : '主备'}</ColorTag>
      <Button icon={<ReloadOutlined />} loading={loading} onClick={() => { setError(''); setRefreshToken(value => value + 1); void onRefresh(); }}>刷新状态</Button>
    </div>
    {error && <Alert title={error} type="error" showIcon className="message" />}
    <div className="sg-sunken sg-toggle"><div><strong>仅观测</strong><p>开启后，后续自动动作只记录并通知。手动操作仍会执行，已下发的换 IP 会继续核对和同步地址。</p></div>
      <Switch checked={group.observation_enabled} loading={observing} checkedChildren="开" unCheckedChildren="关" aria-label={`${group.name}仅观测`} onChange={enabled => void onObserve(enabled)} />
    </div>
    <Tabs items={[
      { key: 'health', label: '健康与观测', children: <>
        <FormSection title="成员就绪与自动切换候选" icon={<SafetyCertificateOutlined />}>
          <EntryReadiness members={group.member_states} connections={group.sync_members} />
        </FormSection>
        <FormSection title="观测动作" icon={<EyeOutlined />}>
          <ObservationDetails group={group.id} enabled={group.observation_enabled} refreshToken={refreshToken} onFailure={failed} />
        </FormSection>
      </> },
      { key: 'dns', label: '受管 DNS 与发布', children: group.publication_mode === 'all'
        ? <AllPublishDNS embedded refreshToken={refreshToken} group={group} nodes={nodes} onClose={onClose} onSessionExpired={onSessionExpired} />
        : <ManagedDNS embedded refreshToken={refreshToken} group={group} nodes={nodes} onClose={onClose} onSessionExpired={onSessionExpired} /> },
      { key: 'sync', label: '端口配置同步', children: <FormSection title="成员同步详情" icon={<CloudSyncOutlined />}><ServerGroupSync members={group.sync_members} /></FormSection> },
    ]} />
  </Modal>;
}

type ObservationAction = { id: string; node_id: string; kind: string; message: string; created_at: string };

function ObservationDetails({ group, enabled, refreshToken, onFailure }: { group: string; enabled: boolean; refreshToken: number; onFailure: (e: unknown) => void }) {
  const [actions, setActions] = useState<ObservationAction[]>([]);
  const [loading, setLoading] = useState(true);
  const failed = useRef(onFailure); failed.current = onFailure;
  useEffect(() => {
    let active = true;
    async function load() {
      try {
        const view = await api<{ actions: ObservationAction[] }>(`/entry-groups/${group}/observation`);
        if (active) setActions(view.actions);
      } catch (e) { if (active) failed.current(e); }
      finally { if (active) setLoading(false); }
    }
    void load();
    const timer = window.setInterval(() => void load(), 10_000);
    return () => { active = false; window.clearInterval(timer); };
  }, [group, enabled, refreshToken]);
  return <>
    <Alert type={enabled ? 'info' : 'warning'} showIcon className="message" title={enabled ? '仅观测已开启：以下记录本来会执行的自动动作' : '仅观测已关闭：后续自动动作会按实际健康条件执行'} description="关闭观测后按当前状态重新判断，历史记录仍保留。" />
    <TableCard<ObservationAction> size="small" rowKey="id" dataSource={actions} loading={loading} pagination={{ pageSize: 5, hideOnSinglePage: true }} locale={{ emptyText: '暂无观测动作' }} columns={[
      { title: '时间', dataIndex: 'created_at', width: 180, render: value => <span className="num">{new Date(value).toLocaleString()}</span> },
      { title: '本来会执行的动作', dataIndex: 'message' },
    ]} />
  </>;
}

function ServerGroupSync({ members }: { members: GroupSyncMember[] }) {
  const status = (value: string) => <StatusDot tone={value === '正常' ? 'ok' : value === '同步失败' ? 'danger' : value === '待确认' ? 'warn' : 'muted'}>{value}</StatusDot>;
  return <TableCard<GroupSyncMember> size="small" pagination={false} rowKey="node_id" dataSource={members} scroll={{ x: 600 }} columns={[
    { title: '服务器', dataIndex: 'node_name' },
    { title: '控制连接', render: (_, row) => <StatusDot tone={row.connected ? 'ok' : 'warn'}>{row.connected ? '已连接' : '失联，待确认'}</StatusDot> },
    { title: '配置同步', render: (_, row) => status(row.sync_status) },
  ]} expandable={{ expandedRowRender: member => member.rules.length ? <Space orientation="vertical" style={{ width: '100%' }}>
    {member.rules.map(rule => <div key={rule.rule_id}><Typography.Text strong>{rule.rule_name}</Typography.Text>
      <Table<SyncMember> size="small" pagination={false} rowKey="node_id" dataSource={[rule]} scroll={{ x: 650 }} columns={[
        { title: '监听端口', dataIndex: 'listen_port' },
        { title: '期望 / 生效版本', render: (_, row) => `${row.desired_revision} / ${row.applied_revision}` },
        { title: '配置同步', render: (_, row) => status(row.sync_status) },
        { title: '应用错误', render: (_, row) => row.apply_error ? <Typography.Text type="danger">{row.apply_error}</Typography.Text> : '—' },
      ]} />
    </div>)}
  </Space> : <Typography.Text type="secondary">本组暂无端口。</Typography.Text> }} />;
}
