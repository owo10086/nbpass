import { Space, Typography } from 'antd';
import { TableCard } from './TableCard';
import { StatusDot } from './StatusDot';

type Reason = { code: string; message: string; rule_id?: string };
export type EntryMemberState = {
  node_id: string; node_name: string; checked_at: string | null;
  ready: boolean; readiness_status: string; readiness_reasons: Reason[];
  candidate: boolean; candidate_status: string; candidate_reasons: Reason[];
  domestic_evidence: { public_ipv4: string; observed_at: string; expires_at: string; source: string } | null;
};

function reasons(values: Reason[]) {
  return values.length ? <Space orientation="vertical" size={0}>{values.map((reason, i) => <Typography.Text key={`${reason.code}-${reason.rule_id ?? i}`} style={{ overflowWrap: 'anywhere', whiteSpace: 'pre-wrap' }}>{reason.message}</Typography.Text>)}</Space> : '条件满足';
}

export function EntryReadiness({ members, connections = [] }: { members: EntryMemberState[]; connections?: { node_id: string; connected: boolean }[] }) {
  return <Space orientation="vertical" style={{ width: '100%' }}>
    <Typography.Paragraph type="secondary" style={{ margin: 0 }}>每 10 秒检查。本组全部启用端口的配置已应用、监听成功，且每条单跳转发至少有一个获授权的转发端点握手成功才就绪。自动切换候选还须控制在线、未维护、未被墙，并有五分钟内的国内可达证据。</Typography.Paragraph>
    <TableCard<EntryMemberState> size="small" pagination={false} rowKey="node_id" dataSource={members} scroll={{ x: 850 }} columns={[
      { title: '服务器', dataIndex: 'node_name' },
      ...(connections.length ? [{ title: '控制连接', key: 'connection', render: (_: unknown, row: EntryMemberState) => {
        const connection = connections.find(c => c.node_id === row.node_id);
        return <StatusDot tone={connection?.connected ? 'ok' : 'warn'}>{connection?.connected ? '在线' : '离线 / 待确认'}</StatusDot>;
      } }] : []),
      { title: '本组就绪', render: (_, row) => <Space orientation="vertical" size={4}><StatusDot tone={row.ready ? 'ok' : row.readiness_status === 'unknown' ? 'muted' : 'warn'}>{row.ready ? '就绪' : row.readiness_status === 'unknown' ? '未知' : '未就绪'}</StatusDot>{reasons(row.readiness_reasons)}</Space> },
      { title: '自动切换候选', render: (_, row) => <Space orientation="vertical" size={4}><StatusDot tone={row.candidate ? 'ok' : row.candidate_status === 'unknown' ? 'muted' : 'warn'}>{row.candidate ? '候选' : row.candidate_status === 'unknown' ? '未知' : '非候选'}</StatusDot>{reasons(row.candidate_reasons)}</Space> },
      { title: '国内可达证据 / 检查时间', render: (_, row) => <Space orientation="vertical" size={0}>{row.domestic_evidence ? <>
        <span>{row.domestic_evidence.source === 'domestic_checker' ? '国内检测点成功' : '正常自测轮'} · {row.domestic_evidence.public_ipv4}</span>
        <Typography.Text type="secondary">证据：{new Date(row.domestic_evidence.observed_at).toLocaleString()}</Typography.Text>
        <Typography.Text type="secondary">到期：{new Date(row.domestic_evidence.expires_at).toLocaleString()}</Typography.Text>
      </> : <span>无当前 IP 的成功证据</span>}<Typography.Text type="secondary">检查：{row.checked_at ? new Date(row.checked_at).toLocaleString() : '等待检查'}</Typography.Text></Space> },
    ]} />
  </Space>;
}
