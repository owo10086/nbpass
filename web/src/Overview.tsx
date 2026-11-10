import { Alert, Button, Empty, Skeleton, Space, Table, Tag } from 'antd';
import { go } from './route';
import { PageHeader } from './PageHeader';
import { operationLabels, type OperationRule, type OperationsState } from './Operations';

export function Overview({ state }: { state: OperationsState }) {
  const { data, error } = state;
  const counts = data?.counts;
  const name = (id: string) => data?.nodes.find(n => n.id === id)?.name || id || '待确认';
  return <>
    <PageHeader title="概览" description="根据当前证据查看规则影响、冗余与自动处理进度。">
      <span className="muted">每 10 秒刷新 · {data ? `更新于 ${new Date(data.generated_at).toLocaleString()}` : '等待数据'}</span>
    </PageHeader>
    {error && <Alert title={`读取失败：${error}`} description="保留上次数据；显示值可能已过期。" type="error" showIcon className="message" />}
    {data?.incident_error && <Alert title={data.incident_error} description={data.incident_sync ? `最近整理：${new Date(data.incident_sync).toLocaleString()}` : '故障记录尚未完成首次整理'} type="warning" showIcon className="message" />}
    <div className="stats">{[
      ['检测到转发异常的规则', counts?.abnormal_rules], ['影响待确认的规则', counts?.unknown_rules],
      ['冗余下降的入口组', counts?.degraded_groups], ['需要人工介入的故障', counts?.manual_incidents ?? '未采集'],
    ].map(([label, value]) => <div className="stat" key={label}><div className="stat-label">{label}</div><div className="stat-value">{value ?? '—'}</div></div>)}</div>
    <p className="muted">节点 {counts?.nodes ?? '—'} · 启用规则 {counts?.enabled_rules ?? '—'} / {counts?.rules ?? '—'}。控制离线不等于业务中断；未发现异常不代表已验证目标业务。</p>
    {!data ? <Skeleton active /> : <>
      <section className="panel">
        <div className="panel-head"><h2>规则影响与证据</h2></div>
        <Table<OperationRule> rowKey="id" dataSource={data.rules} pagination={{ pageSize: 20, showSizeChanger: false }} scroll={{ x: 700 }} locale={{ emptyText: <Empty description="暂无规则，请先添加节点并创建转发规则" /> }} columns={[
          { title: '规则', render: (_, r) => <><strong>{r.name}</strong><div className="muted">{r.group_id ? data.groups.find(g => g.id === r.group_id)?.name : name(r.node_id || '')}</div></> },
          { title: '当前判断', render: (_, r) => <Tag color={r.status === 'abnormal' ? 'red' : r.status === 'unknown' ? 'orange' : undefined}>{operationLabels[r.status] || r.status}</Tag> },
          { title: '原因 / 证据', render: (_, r) => <Space orientation="vertical" size={2}>{r.reasons.map((reason, i) => <span key={i}>{reason.node_id && `${name(reason.node_id)}：`}{reason.message}<span className="cell-sub">{reason.source} · {reason.observed_at ? new Date(reason.observed_at).toLocaleString() : '时间待确认'}</span></span>)}{r.reasons.length === 0 && <span className="muted">{r.enabled ? '当前规则应用及所需隧道证据未显示异常' : '停用规则不计入异常统计'}</span>}</Space> },
          { title: '', render: (_, r) => <Button size="small" onClick={() => go(r.group_id ? 'entry-groups' : 'nodes', r.group_id || r.node_id)}>查看入口</Button> },
        ]} />
      </section>
      <section className="panel" style={{ marginTop: 20 }}><div className="panel-head"><h2>入口组发布与冗余</h2></div>
        <Table rowKey="id" dataSource={data.groups} pagination={false} scroll={{ x: 700 }} columns={[
          { title: '入口组', render: (_, g) => <Button type="link" onClick={() => go('entry-groups', g.id)}>{g.name}</Button> },
          { title: '期望', render: (_, g) => <>{name(g.desired_node)}<div className="mono">{g.expected_ip || '未发布'} · 版本 {g.desired_version}</div></> },
          { title: '服务商最后确认', render: (_, g) => <>{g.confirmed_ip || '待确认'}<span className="cell-sub">{g.confirmed_node ? name(g.confirmed_node) : '对应成员待确认'} · {g.confirmed_at ? new Date(g.confirmed_at).toLocaleString() : '确认时间未知'}</span></> },
          { title: '发布 / 冗余', render: (_, g) => <>{g.stage} · {g.propagation || '传播状态待确认'}<span className="cell-sub">{g.redundancy}{g.reasons.map(r => `；${r.message}`).join('')}</span></> },
        ]} />
      </section>
    </>}
  </>;
}
