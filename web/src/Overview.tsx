import { Alert, Button, Empty, Skeleton, Space } from 'antd';
import { ExclamationCircleOutlined, QuestionCircleOutlined, ClusterOutlined, ToolOutlined } from '@ant-design/icons';
import { go } from './route';
import { displaySystemText, handlingLabels, impactLabels, type Incident } from './Incidents';
import { PageHeader } from './PageHeader';
import { FilterCard } from './FilterCard';
import { TableCard } from './TableCard';
import { StatusDot } from './StatusDot';
import { operationLabels, type OperationRule, type OperationsState } from './Operations';
import './OtherPages.css';

export function Overview({ state }: { state: OperationsState }) {
  const { data, error } = state;
  const counts = data?.counts;
  const name = (id: string) => data?.nodes.find(n => n.id === id)?.name || id || '待确认';
  return <div className="zf-other-page">
    <PageHeader title="概览" description="根据当前证据查看端口影响、冗余与自动处理进度。">
      <span className="muted">每 10 秒刷新 · {data ? `更新于 ${new Date(data.generated_at).toLocaleString()}` : '等待数据'}</span>
    </PageHeader>
    {error && <Alert title={`读取失败：${error}`} description="保留上次数据；显示值可能已过期。" type="error" showIcon className="message" />}
    {data?.incident_error && <Alert title={data.incident_error} description={data.incident_sync ? `最近整理：${new Date(data.incident_sync).toLocaleString()}` : '故障记录尚未完成首次整理'} type="warning" showIcon className="message" />}
    <div className="zf-overview-stats">{[
      { label: '检测到转发异常的端口', value: counts?.abnormal_rules, icon: <ExclamationCircleOutlined /> },
      { label: '影响待确认的端口', value: counts?.unknown_rules, icon: <QuestionCircleOutlined /> },
      { label: '冗余下降的服务器组', value: counts?.degraded_groups, icon: <ClusterOutlined /> },
      { label: '需要人工介入的故障', value: counts?.manual_incidents ?? '未采集', icon: <ToolOutlined /> },
    ].map(({ label, value, icon }) => <div className="zf-overview-stat" key={label}><div className="zf-overview-stat-label">{icon}{label}</div><div className="zf-overview-stat-value">{value ?? '—'}</div></div>)}</div>
    <FilterCard><p className="zf-other-note">服务器 <span className="num">{counts?.nodes ?? '—'}</span> · 启用端口 <span className="num">{counts?.enabled_rules ?? '—'} / {counts?.rules ?? '—'}</span>。控制离线不等于业务中断；未发现异常不代表已验证目标业务。</p></FilterCard>
    {!data ? <Skeleton active /> : <>
      <div className="zf-overview-incidents">
      {[
        { title: '当前异常与自动处理', rows: data.current_incidents }, { title: '最近结束的故障', rows: data.recent_incidents },
      ].map(({title, rows}) => <TableCard<Incident> title={title} actions={<Button size="small" onClick={() => go('incidents')}>全部故障与筛选</Button>} key={title}
        rowKey="id" dataSource={rows} pagination={false} scroll={{ x: 500 }} locale={{ emptyText: data.incident_error ? '整理状态待确认，请稍后刷新' : '当前没有记录' }} columns={[
          { title: '故障', render: (_, r) => <Button type="link" onClick={() => go('incidents', r.id)}>{r.title}</Button> },
          { title: '影响 / 处理', render: (_, r) => <><StatusDot tone={r.impact === 'abnormal' ? 'danger' : r.impact === 'unknown' ? 'warn' : 'muted'}>{impactLabels[r.impact] || r.impact}</StatusDot><span className="cell-sub">{handlingLabels[r.handling]}{r.closure && ` · ${r.closure}`}</span></> },
          { title: '关联服务器组', render: (_, r) => r.refs.filter(ref => ref.kind === 'group').map(ref => ref.name).join('、') || '—' },
          { title: '最近更新', render: (_, r) => <span className="num">{new Date(r.updated_at).toLocaleString()}</span> },
        ]} />)}
      </div>
      <TableCard<OperationRule> title="端口影响与证据" rowKey="id" dataSource={data.rules} pagination={{ pageSize: 20, showSizeChanger: false }} scroll={{ x: 900 }} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无端口，请先添加服务器并创建端口" /> }} columns={[
          { title: '端口', render: (_, r) => <><strong>{r.name}</strong><div className="muted">{r.group_id ? data.groups.find(g => g.id === r.group_id)?.name : name(r.node_id || '')}</div></> },
          { title: '当前判断', render: (_, r) => <StatusDot tone={r.status === 'abnormal' ? 'danger' : r.status === 'unknown' ? 'warn' : 'muted'}>{operationLabels[r.status] || r.status}</StatusDot> },
          { title: '原因 / 证据', render: (_, r) => <Space orientation="vertical" size={2}>{r.reasons.map((reason, i) => <span key={i}>{reason.node_id && `${name(reason.node_id)}：`}{displaySystemText(reason.message)}<span className="cell-sub">{displaySystemText(reason.source)} · {reason.observed_at ? new Date(reason.observed_at).toLocaleString() : '时间待确认'}</span></span>)}{r.reasons.length === 0 && <span className="muted">{r.enabled ? '当前端口应用及所需隧道证据未显示异常' : '停用端口不计入异常统计'}</span>}</Space> },
          { title: '操作', render: (_, r) => <Space wrap><Button size="small" type="link" onClick={() => go('ports', r.id)}>查看端口</Button>{r.incident_id && <Button size="small" type="link" onClick={() => go('incidents', r.incident_id)}>追查故障</Button>}</Space> },
        ]} />
        <TableCard title="服务器组发布与冗余" rowKey="id" dataSource={data.groups} pagination={{ pageSize: 20, showSizeChanger: false }} scroll={{ x: 900 }} columns={[
          { title: '服务器组', render: (_, g) => <Button type="link" onClick={() => go('server-groups', g.id)}>{g.name}</Button> },
          { title: '期望', render: (_, g) => <>{name(g.desired_node)}<div className="mono">{g.expected_ip || '未发布'} · 版本 {g.desired_version}</div></> },
          { title: '服务商最后确认', render: (_, g) => <>{g.confirmed_ip || '待确认'}<span className="cell-sub">{g.confirmed_node ? name(g.confirmed_node) : '对应成员待确认'} · {g.confirmed_at ? new Date(g.confirmed_at).toLocaleString() : '确认时间未知'}</span></> },
          { title: '发布 / 冗余', render: (_, g) => <>{g.stage} · {g.propagation || '传播状态待确认'}<span className="cell-sub">{displaySystemText(g.redundancy)}{g.reasons.map(r => `；${displaySystemText(r.message, r.node_id ? `${name(r.node_id)}：` : '')}`).join('')}</span></> },
        ]} />
    </>}
  </div>;
}
