import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Form, Input, Modal, Popconfirm, Radio, Select, Switch, Tooltip, Typography } from 'antd';
import { ClusterOutlined, InfoCircleOutlined, PlusOutlined, SearchOutlined, TeamOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import type { AllMember } from './AllPublishDNS';
import type { GroupSyncMember } from './ConfigSync';
import { EntryMembers } from './EntryMembers';
import type { EntryMemberState } from './EntryReadiness';
import { EntryDNSStatus } from './EntryDNSStatus';
import { PageHeader } from './PageHeader';
import { FilterCard, FilterField } from './FilterCard';
import { TableCard } from './TableCard';
import { ColorTag } from './ColorTag';
import { StatusDot, type StatusTone } from './StatusDot';
import { FormSection } from './FormSection';
import { useFocusRow } from './route';
import { byText, useDragOrder } from './ListOrder';
import './EntryGroups.css';

export type EntryGroup = {
  id: string; name: string; members: string[]; publication_mode: string;
  observation_enabled: boolean; publication_status: string; sync_status: string;
  sync_members: GroupSyncMember[]; member_states: EntryMemberState[]; member_records?: AllMember[];
};
type Node = { id: string; name: string };
type DomainView = { hostname: string; error?: string };

function publicationTone(status: string): StatusTone {
  if (status === '服务商已写入') return 'ok';
  if (status === '明确失败') return 'danger';
  if (status === '结果未确认' || status === '部分完成') return 'warn';
  return status === '期望已变更' ? 'info' : 'muted';
}

export function EntryGroups({ focus, onSessionExpired }: { focus?: string; onSessionExpired: () => void }) {
  const [rows, setRows] = useState<EntryGroup[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [domains, setDomains] = useState<Record<string, DomainView>>({});
  const [editing, setEditing] = useState<EntryGroup | null>();
  const [statusID, setStatusID] = useState<string>();
  const [query, setQuery] = useState('');
  const [modeFilter, setModeFilter] = useState('');
  const [observationFilter, setObservationFilter] = useState('');
  const [error, setError] = useState('');
  const [formError, setFormError] = useState('');
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [observing, setObserving] = useState<string[]>([]);
  const [page, setPage] = useState(1);
  const [form] = Form.useForm();
  const mode = Form.useWatch('publication_mode', form);
  const active = useRef(false);
  const loadingRef = useRef(false);
  const reloadRequested = useRef(false);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else if (active.current) setError((e as Error).message);
  }
  async function load() {
    if (loadingRef.current) { reloadRequested.current = true; return; }
    loadingRef.current = true; setLoading(true);
    try {
      const [groups, members] = await Promise.all([api<{ entry_groups: EntryGroup[] }>('/entry-groups'), api<{ nodes: Node[] }>('/nodes')]);
      if (!active.current) return;
      setRows(groups.entry_groups); setNodes(members.nodes); setLoaded(true);
      // 列表接口仅在全部发布模式返回记录。主备域名从现有绑定接口补齐，读取失败不冒充未绑定。
      const records = await Promise.all(groups.entry_groups.map(async group => {
        if (group.publication_mode === 'all') return [group.id, { hostname: [...new Set((group.member_records ?? []).flatMap(m => m.record ? [m.record.hostname] : []))].join('、') }] as const;
        try { return [group.id, await api<DomainView>(`/entry-groups/${group.id}/managed-record`)] as const; }
        catch (e) {
          if (e instanceof APIError && e.status === 404) return [group.id, { hostname: '' }] as const;
          if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
          return [group.id, { hostname: '', error: (e as Error).message }] as const;
        }
      }));
      if (active.current) setDomains(Object.fromEntries(records));
    } catch (e) { failed(e); }
    finally {
      loadingRef.current = false;
      if (active.current) {
        setLoading(false);
        if (reloadRequested.current) { reloadRequested.current = false; void load(); }
      }
    }
  }
  const filtered = !!(query.trim() || modeFilter || observationFilter);
  const order = useDragOrder(rows, setRows, '/entry-groups', { filtered, onError: failed, reload: load });
  useEffect(() => {
    active.current = true; void load();
    const timer = window.setInterval(() => { if (!order.busy()) void load(); }, 10_000);
    return () => { active.current = false; window.clearInterval(timer); };
  }, []);
  useEffect(() => { setPage(1); }, [query, modeFilter, observationFilter]);
  const rowClassName = useFocusRow(focus, loaded);
  useEffect(() => {
    if (focus && loaded) {
      const index = rows.findIndex(row => row.id === focus);
      if (index >= 0) { setPage(Math.floor(index / 10) + 1); setStatusID(focus); }
    }
  }, [focus, loaded]);
  function edit(row: EntryGroup | null) {
    setFormError(''); form.resetFields();
    form.setFieldsValue(row ? { name: row.name, members: row.members, publication_mode: row.publication_mode } : { members: [], publication_mode: 'primary_backup' });
    setEditing(row);
  }
  function closeEditor() { setEditing(undefined); form.resetFields(); setFormError(''); }
  async function save(values: { name: string; members: string[]; publication_mode: string }) {
    setSaving(true); setFormError('');
    try {
      // 编辑请求不提交发布模式，确保建组后只修改名称和成员。
      const body = editing ? { name: values.name.trim(), members: values.members } : { ...values, name: values.name.trim() };
      await api(editing ? `/entry-groups/${editing.id}` : '/entry-groups', editing ? 'PUT' : 'POST', body);
      closeEditor(); await load();
    } catch (e) {
      if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
      else setFormError((e as Error).message);
    } finally { setSaving(false); }
  }
  async function remove(id: string) {
    setError('');
    try { await api(`/entry-groups/${id}`, 'DELETE'); await load(); } catch (e) { failed(e); }
  }
  async function observe(row: EntryGroup, enabled: boolean) {
    setObserving(ids => [...ids, row.id]); setError('');
    try {
      await api(`/entry-groups/${row.id}/observation`, 'PUT', { enabled });
      setRows(current => current.map(group => group.id === row.id ? { ...group, observation_enabled: enabled } : group));
      await load();
    } catch (e) { failed(e); }
    finally { if (active.current) setObserving(ids => ids.filter(id => id !== row.id)); }
  }
  const visible = rows.filter(row => {
    const text = [row.id, row.name, domains[row.id]?.hostname, ...row.members.map(id => nodes.find(n => n.id === id)?.name ?? id)].join(' ').toLowerCase();
    return (!query.trim() || text.includes(query.trim().toLowerCase())) && (!modeFilter || row.publication_mode === modeFilter)
      && (!observationFilter || row.observation_enabled === (observationFilter === 'enabled'));
  });
  const selected = rows.find(row => row.id === statusID);
  return <div className="sg-page">
    <PageHeader title="服务器组管理" description="多台服务器统一部署端口，通过主备或全部发布维护组入口。单成员组无需绑定 DNS 也可使用。" actions={<Button type="primary" icon={<PlusOutlined />} onClick={() => edit(null)}>创建组</Button>} />
    {error && <Alert title={error} type="error" showIcon closable onClose={() => setError('')} className="message" />}
    {order.error && <Alert title={order.error} type="error" showIcon className="message" />}
    <FilterCard onRefresh={() => { setError(''); void load(); }} loading={loading}>
      <FilterField label="搜索" htmlFor="sg-search"><Input id="sg-search" className="sg-filter-search" prefix={<SearchOutlined />} placeholder="名称 / 域名 / 服务器 / ID" allowClear value={query} onChange={e => setQuery(e.target.value)} /></FilterField>
      <FilterField label="发布模式" htmlFor="sg-mode"><Select id="sg-mode" value={modeFilter} onChange={setModeFilter} options={[{ value: '', label: '全部模式' }, { value: 'primary_backup', label: '主备' }, { value: 'all', label: '全部发布' }]} /></FilterField>
      <FilterField label="仅观测" htmlFor="sg-observe"><Select id="sg-observe" value={observationFilter} onChange={setObservationFilter} options={[{ value: '', label: '全部状态' }, { value: 'enabled', label: '开启' }, { value: 'disabled', label: '关闭' }]} /></FilterField>
    </FilterCard>
    <TableCard<EntryGroup> title="服务器组" actions={order.restore} rowKey="id" dataSource={visible} loading={!loaded && loading}
      pagination={{ current: page, pageSize: 10, showSizeChanger: false, onChange: setPage }}
      onChange={order.onChange} onRow={order.onRow} rowClassName={row => `${rowClassName(row.id)} ${order.rowClass(row.id)}`} scroll={{ x: 1150 }}
      locale={{ emptyText: filtered ? '没有符合条件的服务器组' : '暂无服务器组，请先添加服务器，再创建组。' }} columns={[
        order.handle,
        { title: 'ID', dataIndex: 'id', width: 125, render: value => <Tooltip title={value}><span className="mono sg-id">{value}</span></Tooltip> },
        { title: '显示名称 / 入口域名', dataIndex: 'name', ...order.sorted('name'), sorter: byText(row => row.name), render: (value, row) => <>
          <span className="sg-name">{value}</span>
          <span className="sg-sub mono">{domains[row.id]?.error ? <Tooltip title={domains[row.id].error}>域名读取失败</Tooltip> : domains[row.id]?.hostname || (domains[row.id] ? '未绑定受管域名' : '正在读取域名…')}</span>
        </> },
        { title: '发布模式', width: 115, render: (_, row) => <ColorTag tone={row.publication_mode === 'all' ? 'blue' : 'purple'}>{row.publication_mode === 'all' ? '全部发布' : '主备'}</ColorTag> },
        { title: '服务器', width: 95, ...order.sorted('members'), sorter: (a, b) => a.members.length - b.members.length, render: (_, row) => <Tooltip title={row.members.map((id, i) => `${nodes.find(n => n.id === id)?.name ?? id}${i === 0 && row.publication_mode !== 'all' ? '（首选入口）' : ''}`).join('、')}><span className="sg-count">{row.members.length}</span></Tooltip> },
        { title: <Tooltip title="显示真实发布阶段；绑定并首次发布后，按健康条件维护 DNS。仅观测会抑制后续自动动作。">DNS 自动切换 <InfoCircleOutlined /></Tooltip>, width: 185, render: (_, row) => <><StatusDot tone={publicationTone(row.publication_status)}>{row.publication_status}</StatusDot><span className="sg-sub">配置同步：{row.sync_status}</span></> },
        { title: '仅观测', width: 100, render: (_, row) => <Switch size="small" checked={row.observation_enabled} loading={observing.includes(row.id)} checkedChildren="开" unCheckedChildren="关" aria-label={`${row.name}仅观测`} onChange={enabled => void observe(row, enabled)} /> },
        { title: '操作', key: 'actions', width: 195, align: 'right', render: (_, row) => <div className="sg-actions">
          <Button size="small" type="link" onClick={() => edit(row)}>编辑</Button>
          <Button size="small" type="link" onClick={() => setStatusID(row.id)}>DNS 状态</Button>
          <Popconfirm title="删除服务器组？" description="须先删除使用本组的端口。解绑后的旧 DNSPod 记录保持原状，请自行核对处理。" onConfirm={() => remove(row.id)}><Button danger size="small" type="text">删除</Button></Popconfirm>
        </div> },
      ]} />
    {selected && <EntryDNSStatus key={selected.id} group={selected} nodes={nodes} onRefresh={load} loading={loading}
      observing={observing.includes(selected.id)} onObserve={enabled => observe(selected, enabled)} onClose={() => { setStatusID(undefined); void load(); }} onSessionExpired={onSessionExpired} />}
    <Modal title={editing ? '编辑服务器组' : '创建服务器组'} className="sg-modal" open={editing !== undefined} width={1000} onCancel={closeEditor} footer={null} forceRender closable={!saving} maskClosable={!saving} keyboard={!saving}>
      <Form form={form} layout="vertical" onFinish={save} disabled={saving}>
        {formError && <Alert title={formError} type="error" showIcon className="message" />}
        <FormSection title="基本信息" icon={<InfoCircleOutlined />}>
          <Form.Item name="name" label="显示名称" rules={[{ required: true, whitespace: true, message: '请输入服务器组名称' }]}><Input maxLength={200} placeholder="例如：华东入口组" /></Form.Item>
          <Form.Item name="publication_mode" label="发布模式" extra="创建后不能更改；需要换模式时，新建组并修改端口的线路。" rules={[{ required: true }]}>
            <Radio.Group className="sg-mode-options" disabled={!!editing || saving}>
              <Radio value="primary_backup"><strong>主备</strong><small>只发布活动入口，按首选顺序切换。支持 A / CNAME 记录。</small></Radio>
              <Radio value="all"><strong>全部发布</strong><small>每台服务器绑定一条 A 记录，按健康状态自动摘除与加回。</small></Radio>
            </Radio.Group>
          </Form.Item>
        </FormSection>
        <FormSection title="服务器成员" icon={<TeamOutlined />}>
          <EntryMembers nodes={nodes} all={mode === 'all'} lockedMembers={(editing?.member_records ?? []).filter(m => m.record).map(m => m.node_id)} />
        </FormSection>
        <FormSection title="DNS 自动切换" icon={<ClusterOutlined />}>
          <div className="sg-sunken sg-toggle"><div><strong>受管域名与发布</strong><p>{editing ? '在「DNS 状态」中配置受管记录、首次发布和自动处理策略。' : '先创建组，再通过「DNS 状态」绑定受管域名并首次发布。'}</p></div>
            {editing && <Button onClick={() => { const id = editing.id; closeEditor(); setStatusID(id); }}>DNS 状态</Button>}
          </div>
          <Typography.Text type="secondary">发布前不会自动修改 DNS。全部发布仅使用 A；主备 CNAME 指向活动服务器已发布的受管域名。</Typography.Text>
        </FormSection>
        <div className="sg-form-actions"><Button onClick={closeEditor} disabled={saving}>取消</Button><Button type="primary" htmlType="submit" loading={saving}>{editing ? '保存更改' : '创建组'}</Button></div>
      </Form>
    </Modal>
  </div>;
}
