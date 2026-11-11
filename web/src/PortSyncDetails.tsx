import { Table, Typography } from 'antd';
import type { SyncMember } from './ConfigSync';
import { StatusDot, type StatusTone } from './StatusDot';

export function PortSyncStatus({ status }: { status: string }) {
  const tones: Record<string, StatusTone> = { 正常: 'ok', 同步失败: 'danger', 待确认: 'warn', 未同步: 'muted' };
  return <StatusDot tone={tones[status] ?? 'muted'}>{status || '未同步'}</StatusDot>;
}

// 共用版本详情仍用于其他页面；这里保留相同字段，使用端口页的新名称。
export function PortSyncDetails({ members }: { members: SyncMember[] }) {
  return <div className="ports-member-details">
    <p>成员服务器生效版本</p>
    <Table<SyncMember> size="small" pagination={false} rowKey="node_id" dataSource={members} scroll={{ x: 700 }} locale={{ emptyText: '暂无成员生效信息' }} columns={[
      { title: '成员服务器', render: (_, row) => <><span>{row.node_name}</span><span className="cell-sub">{row.connected ? '控制已连接' : '控制失联，实际状态待确认'}</span></> },
      { title: '服务端口', dataIndex: 'listen_port', render: value => <span className="mono">{value}</span> },
      { title: '期望 / 实际生效版本', render: (_, row) => <span className="num">{row.desired_revision} / {row.applied_revision}</span> },
      { title: '同步状态', render: (_, row) => <PortSyncStatus status={row.sync_status} /> },
      { title: '应用错误', render: (_, row) => row.apply_error ? <Typography.Text type="danger" className="ports-apply-error">{row.apply_error}</Typography.Text> : '—' },
    ]} />
  </div>;
}
