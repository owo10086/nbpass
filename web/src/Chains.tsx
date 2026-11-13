import { useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Button, Form, Input, Modal, Select, Typography } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, NodeIndexOutlined, PlusOutlined, SearchOutlined, SettingOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { PageHeader } from './PageHeader';
import { FilterCard, FilterField } from './FilterCard';
import { TableCard } from './TableCard';
import { FormSection } from './FormSection';
import { StatusDot } from './StatusDot';
import { ColorTag } from './ColorTag';
import { pageTitles, useFocusRow } from './route';
import { endpointMemberOptions, memberKey, type Endpoint, type EndpointGroup, type EndpointNode, type EndpointOption } from './EndpointData';
import {
  addChainGroup, moveChainGroup, removeChainGroup, chainFormValues, chainSaveRequest,
  chainMatchesQuery, chainObservationStatus, observationMember,
  type ChainHop, type ChainObservation, type ChainValues, type ForwardChain,
} from './ChainsData';
import './Chains.css';

function ChainGroupsInput({ value = [], onChange, groups, options, disabled }: {
  value?: string[]; onChange?: (value: string[]) => void; groups: EndpointGroup[]; options: EndpointOption[]; disabled: boolean;
}) {
  const [adding, setAdding] = useState<string>();
  const [error, setError] = useState('');
  function add() {
    if (!adding) return;
    try { onChange?.(addChainGroup(value, adding)); setAdding(undefined); setError(''); }
    catch (e) { setError((e as Error).message); }
  }
  return <div className="chain-groups-input">
    <ol className="chain-editor-hops">
      {value.map((id, index) => {
        const group = groups.find(group => group.id === id);
        return <li key={id}>
          <div className="chain-hop-heading"><strong>第 {index + 1} 跳 · {group?.name ?? id}</strong><div className="chain-hop-actions">
            <Button size="small" aria-label={`第 ${index + 1} 跳上移`} icon={<ArrowUpOutlined />} disabled={disabled || index === 0} onClick={() => onChange?.(moveChainGroup(value, id, 'up'))} />
            <Button size="small" aria-label={`第 ${index + 1} 跳下移`} icon={<ArrowDownOutlined />} disabled={disabled || index === value.length - 1} onClick={() => onChange?.(moveChainGroup(value, id, 'down'))} />
            <Button size="small" danger aria-label={`移除第 ${index + 1} 跳`} icon={<DeleteOutlined />} disabled={disabled} onClick={() => onChange?.(removeChainGroup(value, id))}>移除</Button>
          </div></div>
          <span className="cell-sub mono">{id}</span>
          {!group?.member_addresses.length && <StatusDot tone="warn">端点组不存在或为空，请移除后重新选择</StatusDot>}
          <ol className="chain-candidates">{group?.member_addresses.map((member, priority) => {
            const option = options.find(option => option.id === memberKey(member));
            return <li key={memberKey(member)}><ColorTag tone={priority === 0 ? 'orange' : 'neutral'}>{priority === 0 ? '主端点' : `备用 ${priority}`}</ColorTag>
              <span>{option?.name ?? `${member.node_id} · ${member.address_id}`}<span className="cell-sub mono">{option?.address ?? '地址未配置，请刷新或检查端点组'}</span></span>
            </li>;
          })}</ol>
        </li>;
      })}
    </ol>
    <div className="chain-add-group"><Select aria-label="添加端点组" showSearch optionFilterProp="label" placeholder="按路径顺序选择端点组" value={adding} disabled={disabled} onChange={setAdding} options={groups.map(group => ({
      value: group.id, label: `${group.name}（${group.member_addresses.length} 个候选）${value.includes(group.id) ? ' · 已添加' : ''}`,
      disabled: value.includes(group.id) || !group.member_addresses.length,
    }))} /><Button icon={<PlusOutlined />} disabled={disabled || !adding} onClick={add}>添加一跳</Button></div>
    {error && <Alert title={error} type="error" showIcon />}
  </div>;
}

function HopCandidates({ hop }: { hop: ChainHop }) {
  return <div className="chain-path-hop">
    <a href={`#/endpoint-groups?focus=${encodeURIComponent(hop.exit_group_id)}`}>第 {hop.position} 跳 · {hop.name}</a>
    <ol className="chain-candidates">{hop.members.map((member, index) => <li key={memberKey(member)}>
      <ColorTag tone={index === 0 ? 'orange' : 'neutral'}>{index === 0 ? '主端点' : `备用 ${index}`}</ColorTag>
      <span><a href={`#/endpoints?focus=${encodeURIComponent(member.node_id)}`}>{member.node_name || member.node_id}</a>
        <span className="cell-sub">所选地址：{member.address_id === 'public' ? '公网' : member.address_id}</span>
        <span className="cell-sub mono">{member.address}:{member.port}</span>
      </span>
    </li>)}</ol>
  </div>;
}

function Observation({ hop, observation, nodes, now }: { hop: ChainHop; observation: ChainObservation; nodes: EndpointNode[]; now: number }) {
  const status = chainObservationStatus(observation, now, hop);
  const member = observationMember(hop, observation);
  const nodeName = (id: string) => nodes.find(node => node.id === id)?.name ?? id;
  const sampled = Date.parse(observation.sampled_at);
  return <div className="chain-observation">
    <StatusDot tone={status.tone}>{status.label}</StatusDot>
    <span className="cell-sub">{nodeName(observation.from_node_id)} → {member?.node_name || nodeName(observation.to_node_id)}</span>
    <span className="cell-sub mono">{member ? `${member.address}:${member.port}` : '未匹配当前候选地址'}</span>
    <details className="chain-evidence"><summary>查看本边配置证据</summary><dl>
      <dt>发起服务器</dt><dd className="mono">{observation.from_node_id}</dd>
      <dt>目标服务器 / 所选地址 ID</dt><dd className="mono">{observation.to_node_id} / {observation.address_id}</dd>
      <dt>引用端口 / 规则代次</dt><dd><a className="mono" href={`#/ports?focus=${encodeURIComponent(observation.rule_id)}`}>{observation.rule_id}</a> / {observation.generation}</dd>
      <dt>端点配置摘要</dt><dd className="mono">{observation.endpoint_config_digest || '未提供'}</dd>
      <dt>链内容指纹</dt><dd className="mono">{observation.chain_fingerprint || '未提供'}</dd>
      <dt>采样时间</dt><dd>{Number.isFinite(sampled) ? new Date(sampled).toLocaleString() : '无有效采样时间'}</dd>
      <dt>服务端状态 / 新鲜标记</dt><dd>{observation.state} / {observation.fresh ? '新鲜' : '历史'}</dd>
    </dl></details>
  </div>;
}

export function Chains({ focus, onSessionExpired }: { focus?: string; onSessionExpired: () => void }) {
  const [rows, setRows] = useState<ForwardChain[]>([]);
  const [groups, setGroups] = useState<EndpointGroup[]>([]);
  const [nodes, setNodes] = useState<EndpointNode[]>([]);
  const [exits, setExits] = useState<Endpoint[]>([]);
  const [query, setQuery] = useState('');
  const [page, setPage] = useState(1);
  const [editing, setEditing] = useState<ForwardChain | null>();
  const [deleting, setDeleting] = useState<ForwardChain>();
  const [error, setError] = useState('');
  const [saveError, setSaveError] = useState('');
  const [deleteError, setDeleteError] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [now, setNow] = useState(Date.now);
  const [form] = Form.useForm<ChainValues>();
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  const active = useRef(false);
  const request = useRef(0);
  const pending = useRef(0);
  const resourcesReady = useRef(false);
  const positioned = useRef<string | undefined>(undefined);
  function failed(e: unknown, target: 'page' | 'save' | 'delete' = 'page') {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else ({ page: setError, save: setSaveError, delete: setDeleteError })[target]((e as Error).message);
  }
  async function load(onlyChains = false) {
    onlyChains = onlyChains && resourcesReady.current;
    if (!onlyChains) resourcesReady.current = false;
    const version = ++request.current;
    pending.current++;
    if (!onlyChains) setLoading(true);
    let timeout: number | undefined;
    try {
      const [chains, resources] = await Promise.race([
        Promise.all([
          api<{ chains: ForwardChain[] }>('/forward-chains'),
          onlyChains ? undefined : Promise.all([
            api<{ exit_groups: EndpointGroup[] }>('/exit-groups'), api<{ nodes: EndpointNode[] }>('/nodes'), api<{ exits: Endpoint[] }>('/tunnel-exits'),
          ]),
        ]),
        new Promise<never>((_, reject) => { timeout = window.setTimeout(() => reject(new Error('转发链读取超时，请刷新重试')), 10000); }),
      ]);
      if (!active.current || version !== request.current) return;
      setRows(chains.chains);
      if (resources) {
        setGroups(resources[0].exit_groups); setNodes(resources[1].nodes); setExits(resources[2].exits);
        resourcesReady.current = true;
      }
      setNow(Date.now()); setError('');
    } catch (e) { if (active.current && version === request.current) failed(e); }
    finally {
      window.clearTimeout(timeout); pending.current--;
      if (active.current && version === request.current) setLoading(false);
    }
  }
  useEffect(() => {
    active.current = true; void load();
    const refresh = window.setInterval(() => { if (!pending.current) void load(true); }, 5000);
    const aging = window.setInterval(() => setNow(Date.now()), 1000);
    return () => { active.current = false; request.current++; window.clearInterval(refresh); window.clearInterval(aging); };
  }, []);
  const visible = useMemo(() => rows.filter(row => chainMatchesQuery(row, query)), [rows, query]);
  const focused = useFocusRow(focus, !loading && visible.some(row => row.id === focus));
  useEffect(() => { positioned.current = undefined; setQuery(''); }, [focus]);
  useEffect(() => {
    if (!focus || positioned.current === focus) return;
    const index = visible.findIndex(row => row.id === focus);
    if (index >= 0) { setPage(Math.floor(index / 10) + 1); positioned.current = focus; }
  }, [focus, visible]);
  const currentPage = Math.min(page, Math.max(1, Math.ceil(visible.length / 10)));
  function edit(row: ForwardChain | null) {
    setSaveError(''); form.resetFields(); form.setFieldsValue(chainFormValues(row ?? undefined)); setEditing(row);
  }
  function close() { setEditing(undefined); setSaveError(''); form.resetFields(); }
  async function save(values: ChainValues) {
    setSaving(true); setSaveError('');
    try {
      const request = chainSaveRequest(values, editing?.id);
      await api<ForwardChain>(request.path, request.method, request.body);
      if (active.current) { close(); await load(); }
    } catch (e) { if (active.current) failed(e, 'save'); }
    finally { if (active.current) setSaving(false); }
  }
  async function remove() {
    if (!deleting) return;
    setRemoving(true); setDeleteError('');
    try {
      await api(`/forward-chains/${encodeURIComponent(deleting.id)}`, 'DELETE');
      if (active.current) { setDeleting(undefined); await load(); }
    } catch (e) { if (active.current) failed(e, 'delete'); }
    finally { if (active.current) setRemoving(false); }
  }
  return <div className="chains-page">
    <PageHeader title={pageTitles.chains} description="按顺序连接端点组；每跳必经，组内按主备顺序选择候选。">
      <Button href="#/endpoint-groups">管理端点组</Button><Button type="primary" icon={<PlusOutlined />} disabled={loading} onClick={() => edit(null)}>创建转发链</Button>
    </PageHeader>
    {error && <Alert title={error} type="error" showIcon className="message" />}
    <FilterCard onRefresh={() => void load()} loading={loading}>
      <FilterField label="搜索" htmlFor="chain-search"><Input id="chain-search" prefix={<SearchOutlined />} allowClear placeholder="名称、组、服务器、所选地址或 ID" value={query} onChange={event => { setQuery(event.target.value); setPage(1); }} /></FilterField>
    </FilterCard>
    <TableCard<ForwardChain> title="转发链列表" rowKey="id" dataSource={visible} loading={loading} scroll={{ x: 1000 }} pagination={{ current: currentPage, pageSize: 10, showSizeChanger: false, onChange: setPage }} rowClassName={row => focused(row.id)} locale={{ emptyText: query.trim() ? '没有匹配的转发链' : '暂无转发链，请先创建至少两个非空端点组，再按路径顺序添加。' }} columns={[
      { title: '名称 / ID', width: 180, fixed: 'left', render: (_, row) => <><strong className="chain-name">{row.name}</strong><Typography.Text className="cell-sub mono chain-id" copyable={{ text: row.id }}>{row.id}</Typography.Text></> },
      { title: '有序路径 / 候选地址', width: 350, render: (_, row) => <div className="chain-path">{row.hops.map(hop => <HopCandidates key={hop.exit_group_id} hop={hop} />)}</div> },
      { title: '引用端口', width: 80, render: (_, row) => <ColorTag>{row.rule_count}</ColorTag> },
      { title: '逐边观测', width: 270, render: (_, row) => <div className="chain-health">{row.hops.map(hop => <div className="chain-hop-health" key={hop.exit_group_id}>
        <strong>第 {hop.position} 跳</strong>
        {hop.observations.length ? hop.observations.map(observation => <Observation key={JSON.stringify([observation.from_node_id, observation.to_node_id, observation.address_id, observation.endpoint_config_digest, observation.chain_fingerprint, observation.rule_id, observation.generation])} hop={hop} observation={observation} nodes={nodes} now={now} />) : <StatusDot>待观测</StatusDot>}
      </div>)}</div> },
      { title: '操作', key: 'actions', width: 120, fixed: 'right', align: 'right', render: (_, row) => <div className="row-actions"><Button type="text" size="small" onClick={() => edit(row)}>编辑</Button><Button type="text" size="small" danger onClick={() => { setDeleteError(''); setDeleting(row); }}>删除</Button></div> },
    ]} />
    <p className="footnote">地址为该候选服务器配置的连接地址，端口为有效拨号端口；主备顺序不表示当前业务选中了该候选。每 5 秒读取观测，采样满 10 秒或服务端标记非新鲜即显示历史。各条证据按端口、来源和配置分别展示；控制在线和本边可达均不能证明完整业务或最终目标可达。</p>
    <Modal title={editing ? '编辑转发链' : '创建转发链'} open={editing !== undefined} onCancel={close} footer={null} width={820} closable={!saving} maskClosable={!saving} keyboard={!saving} destroyOnHidden>
      <Form form={form} layout="vertical" onFinish={save} disabled={saving} requiredMark={false}>
        {saveError && <Alert title={saveError} type="error" showIcon className="message" />}
        {editing && <Alert type="warning" showIcon title={`此链当前被 ${rows.find(row => row.id === editing.id)?.rule_count ?? editing.rule_count} 个端口引用。保存名称或路径顺序会影响所有引用端口。`} description="链 ID 保持不变；服务器会校验全部受影响路径。保存失败时保留当前输入，请根据具体错误修正。" className="message" />}
        <FormSection title="基本信息" icon={<SettingOutlined />}>
          {editing && <p className="chain-id mono">链 ID：{editing.id}</p>}
          <Form.Item name="name" label="转发链名称" extra="1–200 字节；中文通常每字占 3 字节。" rules={[{ validator: async (_, value: string) => {
            if (!value?.trim() || new TextEncoder().encode(value.trim()).length > 200) throw new Error('转发链名称须为 1–200 字节');
          } }]}><Input maxLength={200} placeholder="例如：沪日专线转发" /></Form.Item>
        </FormSection>
        <FormSection title="有序端点组" icon={<NodeIndexOutlined />} description="至少两组，按实际经过顺序添加。用箭头调整先后，组内主备顺序请在端点组管理中修改。入口及所有跳的候选服务器不能重复。">
          {!groups.length && <Alert type="info" showIcon title={<>暂无端点组，请先到 <a href="#/endpoint-groups">端点组管理</a> 创建。</>} className="message" />}
          <Form.Item name="exit_group_ids" rules={[{ validator: async (_, ids: string[]) => {
            if (!ids || ids.length < 2) throw new Error('转发链至少需要两个端点组');
            if (new Set(ids).size !== ids.length) throw new Error('同一端点组不能重复添加');
            if (ids.some(id => !groups.some(group => group.id === id && group.member_addresses.length))) throw new Error('端点组不存在或为空，请移除后重新选择');
          } }]}><ChainGroupsInput groups={groups} options={endpointMemberOptions(exits, nodes)} disabled={saving} /></Form.Item>
        </FormSection>
        <div className="chain-form-footer"><Button onClick={close}>取消</Button><Button type="primary" htmlType="submit" loading={saving}>保存转发链</Button></div>
      </Form>
    </Modal>
    <Modal title="删除转发链？" open={!!deleting} okText="删除转发链" okButtonProps={{ danger: true }} confirmLoading={removing} closable={!removing} maskClosable={!removing} keyboard={!removing} cancelButtonProps={{ disabled: removing }} onCancel={() => setDeleting(undefined)} onOk={() => void remove()}>
      {deleteError && <Alert title={deleteError} type="error" showIcon className="message" />}
      <p className="chain-name">{deleting?.name}</p><p>当前引用端口数：{deleting ? rows.find(row => row.id === deleting.id)?.rule_count ?? deleting.rule_count : 0}。被引用的链不能删除，请先移除所有端口引用；停用端口仍保留引用。</p>
    </Modal>
  </div>;
}
