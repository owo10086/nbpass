import { Space, Table, Tag, Typography } from 'antd';

export type SyncMember = {
  node_id: string; node_name: string; connected: boolean; listen_port: number;
  desired_revision: number; applied_revision: number; sync_status: string; apply_error: string;
};
export type GroupSyncMember = {
  node_id: string; node_name: string; connected: boolean; sync_status: string;
  rules: (SyncMember & { rule_id: string; rule_name: string })[];
};

export function SyncTag({ status }: { status: string }) {
  const colors: Record<string, string> = { '正常': 'green', '同步失败': 'red', '待确认': 'orange', '未同步': 'default' };
  return <Tag color={colors[status]}>{status}</Tag>;
}

export function RuleSyncDetails({ members }: { members: SyncMember[] }) {
  return <Table<SyncMember> size="small" pagination={false} rowKey="node_id" dataSource={members} scroll={{ x: 700 }} columns={[
    { title: '成员服务器', render: (_, row) => <Space orientation="vertical" size={0}><span>{row.node_name}</span><Typography.Text type="secondary">{row.connected ? '控制已连接' : '控制失联，实际状态待确认'}</Typography.Text></Space> },
    { title: '监听端口', dataIndex: 'listen_port' },
    { title: '期望 / 实际生效版本', render: (_, row) => `${row.desired_revision} / ${row.applied_revision}` },
    { title: '同步状态', render: (_, row) => <SyncTag status={row.sync_status} /> },
    { title: '应用错误', render: (_, row) => row.apply_error ? <Typography.Text type="danger" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{row.apply_error}</Typography.Text> : '—' },
  ]} />;
}

export function GroupSyncDetails({ members }: { members: GroupSyncMember[] }) {
  return <Table<GroupSyncMember> size="small" pagination={false} rowKey="node_id" dataSource={members} columns={[
    { title: '成员服务器', dataIndex: 'node_name' },
    { title: '控制连接', render: (_, row) => row.connected ? '已连接' : '失联，待确认' },
    { title: '同步状态', render: (_, row) => <SyncTag status={row.sync_status} /> },
  ]} expandable={{ expandedRowRender: member => <Space orientation="vertical" style={{ width: '100%' }}>
    {member.rules.length === 0 && <Typography.Text type="secondary">本服务器组暂无转发端口。</Typography.Text>}
    {member.rules.map(rule => <div key={rule.rule_id} style={{ width: '100%' }}><Typography.Text strong>{rule.rule_name}</Typography.Text><RuleSyncDetails members={[rule]} /></div>)}
  </Space> }} />;
}

export function SyncHelp() {
  return <p className="footnote">展开行查看每个成员的实际生效版本与错误。失联成员显示待确认；部分端口成功时，服务器的已应用版本仍保留在上一次完整成功的版本。停用端口显示正常，表示服务器已确认撤销监听。</p>;
}
