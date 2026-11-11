import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Modal, Spin, Switch, Typography } from 'antd';
import { SafetyOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { FormSection } from './FormSection';
import { StatusDot } from './StatusDot';
import { TableCard } from './TableCard';
import './Endpoints.css';

type Entry = { node_id: string; name: string; connected: boolean; applied: boolean; applied_revision: number; handshaken: boolean; handshaken_at: string | null };
type Progress = { enabled: boolean; address: string; action_id: string; entries: Entry[] };

export function ExitBlocked({ nodeID, onSessionExpired, onChanged }: { nodeID: string; onSessionExpired: () => void; onChanged: () => void }) {
  const [open, setOpen] = useState(false);
  const [state, setState] = useState<Progress>();
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  const path = `/nodes/${nodeID}/exit-blocked`;
  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else setError((e as Error).message);
  }
  useEffect(() => {
    if (!open) return;
    let active = true;
    setState(undefined); setError('');
    async function load() {
      try { const result = await api<Progress>(path); if (active) { setState(result); setError(''); } }
      catch (e) { if (active) failed(e); }
    }
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => { active = false; window.clearInterval(timer); };
  }, [open, path]);
  async function toggle(enabled: boolean) {
    setSaving(true);
    try { await api(path, 'PUT', { enabled }); setState(await api<Progress>(path)); setError(''); }
    catch (e) { failed(e); }
    finally { setSaving(false); }
  }
  return <>
    <Button type="text" size="small" onClick={() => setOpen(true)}>被墙处理与地址进度</Button>
    <Modal className="endpoint-modal" title="端点被墙处理与地址进度" open={open} onCancel={() => { setOpen(false); onChanged(); }} footer={null} width={860} closable={!saving} maskClosable={!saving} keyboard={!saving}>
      {error && <Alert type="error" title={error} showIcon className="message" />}
      <FormSection title="被墙处理" icon={<SafetyOutlined />} description="开启后，端点被认定被墙时申请换 IP。请先在服务器管理配置换 IP 接口、开启自测和服务器自动换 IP 总授权。端点与服务器组共用该服务器的额度、冷却和暂停状态。">
      {!state && !error && <Spin />}
      <div className="endpoint-progress"><Typography.Text>端点被墙处理</Typography.Text><Switch aria-label="端点被墙处理" checked={state?.enabled ?? false} disabled={!state || saving} loading={saving} onChange={value => void toggle(value)} /><Typography.Text>当前地址：<span className="mono">{state?.address ?? '加载中'}</span></Typography.Text></div>
      <Alert type="info" showIcon title="确认 IP 变化后自动更新相关入口" description="“已应用”表示入口已保存并应用新地址；“已握手”表示该入口已通过新地址的真实单跳转发握手。两项独立确认，离线入口重连后继续同步。已有确认为本次地址更新的历史进度，不代表当前链路健康。换 IP 可能中断已有连接，其他端点继续接新连接。" />
      {state?.action_id && <Typography.Paragraph className="endpoint-action-id">本次换 IP 动作：<Typography.Text className="mono" copyable>{state.action_id}</Typography.Text></Typography.Paragraph>}
      </FormSection>
      <TableCard<Entry> title="地址同步进度" rowKey="node_id" size="small" dataSource={state?.entries ?? []} loading={!state && !error} pagination={false} scroll={{ x: 680 }} locale={{ emptyText: state?.action_id ? '暂无相关入口' : '尚无端点地址更新' }} columns={[
        { title: '入口', dataIndex: 'name' },
        { title: '控制连接', render: (_, row) => <StatusDot tone={row.connected ? 'ok' : 'muted'}>{row.connected ? '已连接' : '离线 · 重连后继续'}</StatusDot> },
        { title: '新地址应用', render: (_, row) => <StatusDot tone={row.applied ? 'ok' : 'warn'}>{row.applied ? '已应用' : '待确认'}</StatusDot> },
        { title: '新地址握手', render: (_, row) => <StatusDot tone={row.handshaken ? 'ok' : 'warn'}>{row.handshaken ? '已握手' : '待确认'}</StatusDot> },
        { title: '握手确认时间', render: (_, row) => row.handshaken_at ? <span className="mono">{new Date(row.handshaken_at).toLocaleString()}</span> : '—' },
      ]} />
    </Modal>
  </>;
}
