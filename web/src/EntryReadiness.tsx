import { Space, Table, Tag, Typography } from 'antd';

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

export function EntryReadiness({ members }: { members: EntryMemberState[] }) {
  return <Space orientation="vertical" style={{ width: '100%' }}>
    <Typography.Paragraph type="secondary" style={{ margin: 0 }}>每 10 秒检查。本组全部启用规则已应用、监听成功，且每条隧道规则至少一个获授权出口握手成功才就绪。自动切换候选还须控制在线、未维护、未被墙，并有五分钟内的国内可达证据。</Typography.Paragraph>
    <Table<EntryMemberState> size="small" pagination={false} rowKey="node_id" dataSource={members} scroll={{ x: 650 }} columns={[
      { title: '成员节点', dataIndex: 'node_name' },
      { title: '本组就绪', render: (_, row) => <Space orientation="vertical" size={4}><Tag color={row.ready ? 'green' : row.readiness_status === 'unknown' ? 'default' : 'orange'}>{row.ready ? '就绪' : row.readiness_status === 'unknown' ? '未知' : '未就绪'}</Tag>{reasons(row.readiness_reasons)}</Space> },
      { title: '自动切换候选', render: (_, row) => <Space orientation="vertical" size={4}><Tag color={row.candidate ? 'green' : row.candidate_status === 'unknown' ? 'default' : 'orange'}>{row.candidate ? '候选' : row.candidate_status === 'unknown' ? '未知' : '非候选'}</Tag>{reasons(row.candidate_reasons)}</Space> },
      { title: '国内可达证据 / 检查时间', render: (_, row) => <Space orientation="vertical" size={0}>{row.domestic_evidence ? <>
        <span>{row.domestic_evidence.source === 'domestic_checker' ? '国内检测点成功' : '正常自测轮'} · {row.domestic_evidence.public_ipv4}</span>
        <Typography.Text type="secondary">证据：{new Date(row.domestic_evidence.observed_at).toLocaleString()}</Typography.Text>
        <Typography.Text type="secondary">到期：{new Date(row.domestic_evidence.expires_at).toLocaleString()}</Typography.Text>
      </> : <span>无当前 IP 的成功证据</span>}<Typography.Text type="secondary">检查：{row.checked_at ? new Date(row.checked_at).toLocaleString() : '等待检查'}</Typography.Text></Space> },
    ]} />
  </Space>;
}
