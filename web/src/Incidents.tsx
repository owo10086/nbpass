import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Form, Input, Select } from 'antd';
import { api, APIError } from './api';
import { PageHeader } from './PageHeader';
import { go } from './route';
import type { OperationsView } from './Operations';
import { IncidentDetails } from './IncidentDetails';
import { FilterCard } from './FilterCard';
import { TableCard } from './TableCard';
import { StatusDot } from './StatusDot';
import { ColorTag } from './ColorTag';
import './OtherPages.css';

export type Incident = { synced_at?: string; sync_error?: string; id: string; kind: string; title: string; impact: string; handling: string; started_at: string; updated_at: string; ended_at: string | null; closure: string; refs: { kind: string; id: string; name: string; exists: boolean }[]; reasons: string[] };
export const incidentKinds: Record<string, string> = { offline: '控制离线', blocked: '被墙', rule: '配置、监听或隧道', dns: 'DNS' };
export const handlingLabels: Record<string, string> = { waiting: '等待条件', processing: '处理中', manual: '需要人工', ended: '已结束' };
export const impactLabels: Record<string, string> = { abnormal: '检测到转发异常', unknown: '影响待确认', cleared: '异常证据已解除', no_known_issue: '未发现转发异常' };
type Page = { incidents: Incident[]; next_before: string | null; synced_at: string | null; sync_error: string };

// 后端与历史记录沿用旧术语；只转换已知系统文案，保留资源名称和原始错误。
export function displaySystemText(value: string, namePrefix = '', groupTemplate = false): string {
  if (namePrefix && value.startsWith(namePrefix)) return namePrefix + displaySystemText(value.slice(namePrefix.length), '', groupTemplate);
  const labels: Record<string, string> = {
    '单成员入口组': '单成员服务器组',
    '节点状态': '服务器状态',
    '节点维护中': '服务器维护中',
    '节点已认定被墙': '服务器已认定被墙',
    '节点被推定被墙': '服务器被推定被墙',
    '控制连接中断，节点可能仍按缓存转发': '控制连接中断，服务器可能仍按缓存转发',
    '控制连接状态不能证明业务是否中断；节点可能按缓存继续转发': '控制连接状态不能证明业务是否中断；服务器可能按缓存继续转发',
    '节点被推定被墙；实际规则影响需结合入口与解析证据': '服务器被推定被墙；实际端口影响需结合入口与解析证据',
    '入口组对外使用情况待结合受管记录确认': '服务器组对外使用情况待结合受管记录确认',
    '最后确认发布的节点已不在当前规则成员中，影响待确认': '最后确认发布的服务器已不在当前端口成员中，影响待确认',
    '本入口未就绪，请查看逐规则原因': '本入口未就绪，请查看逐端口原因',
    '该成员没有绑定节点记录，CNAME 组无法指向它': '该成员没有绑定服务器记录，CNAME 组无法指向它',
    '该成员的节点记录尚未首次发布，CNAME 组无法指向它': '该成员的服务器记录尚未首次发布，CNAME 组无法指向它',
    '该成员的节点记录已暂停，CNAME 组无法指向它': '该成员的服务器记录已暂停，CNAME 组无法指向它',
    '规则证据异常': '端口证据异常',
    '规则配置/入口检查': '端口配置/入口检查',
    '入口组策略决策': '服务器组策略决策',
    '节点已删除，历史保留': '服务器已删除，历史保留',
    '原节点回切已由既有协调器提交，写入与传播仍分别核对': '原服务器回切已由既有协调器提交，写入与传播仍分别核对',
    '换 IP 结果未确认，自动换 IP 已冻结，请到节点页面核对': '换 IP 结果未确认，自动换 IP 已冻结，请到服务器页面核对',
    '新地址复检已通过，入口组回切及传播仍以各自分支为准': '新地址复检已通过，服务器组回切及传播仍以各自分支为准',
    '连续无改善，现有自动换 IP 已暂停；请到节点页面核对并按原流程人工恢复': '连续无改善，现有自动换 IP 已暂停；请到服务器页面核对并按原流程人工恢复',
    '节点控制连接已建立': '服务器控制连接已建立',
    '节点控制连接中断超过 90 秒，判定离线': '服务器控制连接中断超过 90 秒，判定离线',
    '节点已恢复上线': '服务器已恢复上线',
    '节点进程启动': '服务器进程启动',
    '节点配置应用失败': '服务器配置应用失败',
    '节点本地事件存在缺口': '服务器本地事件存在缺口',
    '共用节点需要换 IP，但本组没有就绪候选；保留最后的解析，按节点总授权、共享额度与冻结规则处理': '共用服务器需要换 IP，但本组没有就绪候选；保留最后的解析，按服务器总授权、共享额度与冻结规则处理',
    '活动入口被墙，没有就绪备用，退化为策略①申请换 IP，仍受节点授权、额度与冻结限制': '活动入口被墙，没有就绪备用，退化为策略①申请换 IP，仍受服务器授权、额度与冻结限制',
    '节点未开启自动换 IP，只按最少保留数摘除并通知': '服务器未开启自动换 IP，只按最少保留数摘除并通知',
    '换 IP 结果未确认，节点已冻结，请核对或人工处理': '换 IP 结果未确认，服务器已冻结，请核对或人工处理',
    '新 IP 三轮复检无改善，继续受节点共享额度与无改善暂停限制': '新 IP 三轮复检无改善，继续受服务器共享额度与无改善暂停限制',
    '节点不支持新 IP 复检，自动许可已取消且未投递': '服务器不支持新 IP 复检，自动许可已取消且未投递',
    '节点复检能力变化，许可可能已执行；暂停投递，升级节点后核对原动作': '服务器复检能力变化，许可可能已执行；暂停投递，升级服务器后核对原动作',
    '单节点升级已排队，进入 10 分钟维护期': '单服务器升级已排队，进入 10 分钟维护期',
    '节点升级完成，新进程版本一致且本地业务就绪': '服务器升级完成，新进程版本一致且本地业务就绪',
    '节点连续三轮自测异常，认定被墙（自测推定）': '服务器连续三轮自测异常，认定被墙（自测推定）',
    '节点自测正常，已解除疑似被墙或被墙': '服务器自测正常，已解除疑似被墙或被墙',
    '节点连续三轮最终结果异常，认定被墙（含检测点补判）': '服务器连续三轮最终结果异常，认定被墙（含检测点补判）',
    '节点最终结果正常，已解除疑似被墙或被墙（检测点补判）': '服务器最终结果正常，已解除疑似被墙或被墙（检测点补判）',
    '加回暂停：记录地址、面板确认地址与健康证据地址不一致，请先在节点页同步地址': '加回暂停：记录地址、面板确认地址与健康证据地址不一致，请先在服务器页同步地址',
  };
  if (Object.hasOwn(labels, value)) return labels[value];
  if (value.startsWith('转发规则“')) return value.replace(/^转发规则“/, '端口“');
  if (value.startsWith('隧道规则“')) return value.replace(/^隧道规则“/, '单跳转发端口“').replace(/”没有获授权出口的新鲜成功握手$/, '”没有获授权转发端点的新鲜成功握手');
  const evidence = value.match(/^节点 (.*)，期望 (\d+)，报告 (\d+)；出口组 (.*)$/);
  if (evidence) return `服务器 ${evidence[1]}，期望 ${evidence[2]}，报告 ${evidence[3]}；端点组 ${evidence[4]}`;
  const record = value.match(/^该成员的节点记录地址（(.*)）与发布地址（(.*)）不一致，请先同步节点地址$/);
  if (record) return `该成员的服务器记录地址（${record[1]}）与发布地址（${record[2]}）不一致，请先同步服务器地址`;
  const address = value.match(/^节点 (\S+) 上报地址变化为 (\S+)，面板确认地址仍为 (\S+)；受管记录保持原状，请管理员手动同步地址$/);
  if (address) return `服务器 ${address[1]} 上报地址变化为 ${address[2]}，面板确认地址仍为 ${address[3]}；受管记录保持原状，请管理员手动同步地址`;
  const recheck = value.match(/^(.+)；新地址复检：(.*)$/);
  if (recheck) return `${displaySystemText(recheck[1], '', groupTemplate)}；新地址复检：${recheck[2]}`;
  const upgrade = value.match(/^节点升级阶段：([a-z_]+)$/);
  if (upgrade) return `服务器升级阶段：${upgrade[1]}`;
  const branch = value.match(/^(.+)(；已切离时留在备用入口，无备用时保留当前解析)$/);
  if (branch) return displaySystemText(branch[1], '', groupTemplate) + branch[2];
  const impact = value.match(/^(.+)(；(?:已摘除的记录保持暂停，不因换 IP 改值提前启用|仍发布地址，等待摘除或最少保留数决定，尚未获准换 IP|因最少保留数仍发布地址，换 IP 期间该组可能中断，新地址确认后记录改值并保持启用))$/);
  if (impact) return displaySystemText(impact[1], '', groupTemplate) + impact[2];
  if (!groupTemplate) return value;
  const observation = value.match(/^(入口组 .+ 观测中：本来会自动换 IP \S+ )（共享节点的所有自动用途暂停）$/);
  if (observation) return observation[1].replace(/^入口组 /, '服务器组 ') + '（共享服务器的所有自动用途暂停）';
  if (/^入口节点 \S+ 动作 \S+ 期望版本 \d+：/.test(value)) return value.replace(/^入口节点 /, '入口服务器 ');
  const group = value.match(/^入口组 (.+(?: 期望版本 \d+(?:（[^）]*）)?|（策略③）| 观测中| 成员 [^\s：]+))：([\s\S]*)$/);
  if (group) return `服务器组 ${group[1]}：${displaySystemText(group[2])}`;
  if (value.startsWith('入口组 ')) return value.replace(/^入口组 /, '服务器组 ');
  return value;
}

export function displayRotationNotice(value: string): string {
  // 通知带有历史名称（允许含冒号），只从已知正文模板的末尾识别边界。
  const group = value.match(/^([\s\S]*：)(入口组 [^\s：]+（策略③）：[\s\S]+)，动作 ([^\s，]+)$/);
  if (group) return `${group[1]}${displaySystemText(group[2], '', true)}，动作 ${group[3]}`;
  const node = value.match(/^([\s\S]*：)(节点不支持新 IP 复检，自动许可已取消且未投递|节点复检能力变化，许可可能已执行；暂停投递，升级节点后核对原动作)，动作 ([^\s，]+)$/);
  if (node) return `${node[1]}${displaySystemText(node[2])}，动作 ${node[3]}`;
  return /，动作 [^\s，]+$/.test(value) ? value : displaySystemText(value);
}

export function Incidents({ focus, operations, onSessionExpired }: { focus?: string; operations?: OperationsView; onSessionExpired: () => void }) {
  const [page, setPage] = useState<Page>();
  const [filters, setFilters] = useState<Record<string, string>>({});
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const request = useRef(0);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  async function load(before?: string) {
    const version = ++request.current; setLoading(true);
    try {
      const query = new URLSearchParams({ limit: '30', ...filters }); if (before) query.set('before', before);
      const result = await api<Page>(`/incidents?${query}`);
      if (version === request.current) { setPage(old => ({ ...result, incidents: before && old ? [...old.incidents, ...result.incidents] : result.incidents })); setError(''); }
    } catch (e) {
      if (version === request.current) { setError((e as Error).message); if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current(); }
    } finally { if (version === request.current) setLoading(false); }
  }
  useEffect(() => { if (!focus) void load(); return () => { request.current++; }; }, [filters, focus]);
  if (focus) return <IncidentDetails id={focus} onSessionExpired={onSessionExpired} />;
  return <div className="zf-other-page">
    <PageHeader title="故障" description="按故障追查证据与处理分支；控制离线和 DNS 状态不能单独证明目标业务是否中断。" />
    <Form className="zf-other-filters" layout="inline" colon={false} onFinish={values => {
      const next: Record<string, string> = {};
      for (const [key, value] of Object.entries(values)) if (value) next[key] = key === 'from' || key === 'to' ? new Date(String(value)).toISOString() : String(value);
      setFilters(next);
    }}>
      <FilterCard onRefresh={() => void load()} loading={loading}>
      <Form.Item name="status" label="状态"><Select allowClear placeholder="全部状态" options={[{ value: 'open', label: '未结束' }, { value: 'manual', label: '需要人工' }, { value: 'waiting', label: '等待条件' }, { value: 'processing', label: '处理中' }, { value: 'ended', label: '已结束' }]} /></Form.Item>
      <Form.Item name="kind" label="类型"><Select allowClear placeholder="全部类型" options={Object.entries(incidentKinds).map(([value, label]) => ({ value, label }))} /></Form.Item>
      <Form.Item name="node_id" label="服务器"><Select allowClear showSearch optionFilterProp="label" placeholder="全部服务器" options={operations?.nodes.map(n => ({ value: n.id, label: n.name }))} /></Form.Item>
      <Form.Item name="group_id" label="服务器组"><Select allowClear showSearch optionFilterProp="label" placeholder="全部服务器组" options={operations?.groups.map(g => ({ value: g.id, label: g.name }))} /></Form.Item>
      <Form.Item name="rule_id" label="端口"><Select allowClear showSearch optionFilterProp="label" placeholder="全部端口" options={operations?.rules.map(r => ({ value: r.id, label: r.name }))} /></Form.Item>
      <Form.Item name="watch_id" label="解析监控 ID"><Input allowClear /></Form.Item>
      <Form.Item name="from" label="开始"><Input type="datetime-local" /></Form.Item><Form.Item name="to" label="结束"><Input type="datetime-local" /></Form.Item>
      <Form.Item><Button type="primary" htmlType="submit">筛选</Button></Form.Item>
      </FilterCard>
    </Form>
    {error && <Alert title={error} description="保留上次列表；筛选后的数据尚未成功读取时，请勿将旧列表当作新结果。" type="error" showIcon className="message" />}
    {page?.sync_error && <Alert title={page.sync_error} description={page.synced_at ? `最近整理：${new Date(page.synced_at).toLocaleString()}` : '等待首次整理'} type="warning" showIcon className="message" />}
    <TableCard<Incident> title="故障记录" rowKey="id" dataSource={page?.incidents} loading={loading} pagination={{ pageSize: 30, showSizeChanger: false }} scroll={{ x: 1100 }} locale={{ emptyText: '暂无符合条件的故障' }} footer={() => <div className="zf-other-table-footer"><span className="muted num">已加载 {page?.incidents.length ?? 0} 条</span><Button disabled={!page?.next_before} loading={loading} onClick={() => void load(page?.next_before || undefined)}>加载更早故障</Button></div>} columns={[
      { title: '故障', render: (_, r) => <Button type="link" onClick={() => go('incidents', r.id)}>{r.title}</Button> },
      { title: '类型', render: (_, r) => <ColorTag>{incidentKinds[r.kind] || r.kind}</ColorTag> },
      { title: '当前判断', render: (_, r) => <StatusDot tone={r.impact === 'abnormal' ? 'danger' : r.impact === 'unknown' ? 'warn' : 'muted'}>{impactLabels[r.impact] || r.impact}</StatusDot> },
      { title: '处理状态', render: (_, r) => <StatusDot tone={r.handling === 'manual' ? 'warn' : r.handling === 'processing' ? 'info' : 'muted'}>{handlingLabels[r.handling] || r.handling}</StatusDot> },
      { title: '首次观察 / 最近更新', render: (_, r) => <span className="num">{new Date(r.started_at).toLocaleString()}<span className="cell-sub">{new Date(r.updated_at).toLocaleString()}</span></span> },
      { title: '结束方式', render: (_, r) => r.closure || '—' },
    ]} />
  </div>;
}
