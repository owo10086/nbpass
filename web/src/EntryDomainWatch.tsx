import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Descriptions, Space, Table, Tag, Typography } from 'antd';
import { api, APIError } from './api';

type Path = { path_id: string; node_id: string; resolver: string; ips: string[]; status: string; consecutive_matches: number; sampled_at: string | null };
type Observation = { domain: string; expected_ips: string[]; expected_version: number; propagation_status: string; status: string; coverage_label: string; observation_deadline: string | null; next_sample_at: string; provider_error: string; paths: Path[] };
const date = (value: string | null) => value ? new Date(value).toLocaleString() : '待确认';

export function EntryDomainWatch({ groupID, nodes, onSessionExpired, all = false }: { groupID: string; all?: boolean; nodes: { id: string; name: string }[]; onSessionExpired: () => void }) {
  const [observation, setObservation] = useState<Observation>();
  const [error, setError] = useState('');
  const refresh = useRef<() => void>(() => {});
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  useEffect(() => {
    let active = true;
    async function load() {
      try {
        const result = await api<Observation>(`/entry-groups/${groupID}/dns-watch`);
        if (active) { setObservation(result); setError(''); }
      } catch (e) {
        if (!active) return;
        if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
        else if (e instanceof APIError && e.status === 404) setObservation(undefined);
        else setError((e as Error).message);
      }
    }
    refresh.current = () => { void load(); };
    void load();
    const timer = window.setInterval(() => void load(), 5000);
    return () => { active = false; window.clearInterval(timer); };
  }, [groupID]);
  if (!observation && !error) return null;
  return <Card title="域名解析与传播观察" extra={<Button onClick={() => refresh.current()}>刷新</Button>}>
    {error && <Alert type="error" showIcon title={error} />}
    {observation && <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Descriptions column={1} size="small" items={[
        { key: 'domain', label: '域名', children: observation.domain },
        { key: 'expected', label: all ? '已发布地址集合' : '活动入口期望', children: `${observation.expected_ips.join('、') || '尚未发布'} · 期望版本 ${observation.expected_version}` },
        { key: 'progress', label: '传播进度', children: <Tag color={observation.propagation_status === '已生效' ? 'green' : observation.propagation_status === '传播未确认' ? 'orange' : 'blue'}>{observation.propagation_status || '尚未发布'}</Tag> },
        { key: 'status', label: '解析监控', children: `${observation.status} · ${observation.coverage_label}` },
        { key: 'deadline', label: '最长观察截止', children: date(observation.observation_deadline) },
        { key: 'next', label: '下次采样', children: date(observation.next_sample_at) },
      ]} />
      <Typography.Paragraph type="secondary">{all ? '解析结果没有集合外地址即一致。启用在任一路径返回该地址后生效；暂停须所有路径不再返回。相同地址仍由其他启用成员发布时，以服务商确认暂停为完成。记录状态与地址传播分别展示。' : '切换后每 30 秒查询；每条有采样的有效路径连续两次命中期望，且至少包含一条国内路径，才记已生效。旧记录 TTL 内的缓存旧值保留宽限；传播未确认会通知。'}</Typography.Paragraph>
      {observation.provider_error && <Alert type="warning" showIcon title={observation.provider_error} />}
      <Table<Path> rowKey="path_id" size="small" pagination={false} dataSource={observation.paths} scroll={{ x: 850 }} columns={[
        { title: '解析路径', render: (_, p) => `${p.node_id ? nodes.find(n => n.id === p.node_id)?.name ?? '国内检测点' : '面板本机 DoH'} · ${p.resolver}` },
        { title: '观察值（A）', render: (_, p) => p.ips.join('、') || '尚无结果' },
        { title: '路径状态', dataIndex: 'status', render: (value, p) => <Tag>{p.sampled_at ? value : '覆盖不足'}</Tag> },
        { title: '连续命中', dataIndex: 'consecutive_matches', render: value => all ? value : `${value} / 2` },
        { title: '最近采样', dataIndex: 'sampled_at', render: date },
      ]} />
    </Space>}
  </Card>;
}
