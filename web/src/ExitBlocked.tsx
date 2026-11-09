import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Modal, Space, Switch, Table, Tag, Typography } from 'antd';
import { api, APIError } from './api';

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
    <Button size="small" onClick={() => setOpen(true)}>被墙处理与地址进度</Button>
    <Modal title="出口被墙处理与地址进度" open={open} onCancel={() => { setOpen(false); onChanged(); }} footer={null} width={760}>
      {error && <Alert type="error" title={error} showIcon className="message" />}
      <Typography.Paragraph>开启后，出口被认定被墙时申请换 IP。请先在节点页配置换 IP 接口、开启自测和节点自动换 IP 总授权。出口与入口组共用该节点的额度、冷却和暂停状态。</Typography.Paragraph>
      <Space wrap style={{ marginBottom: 16 }}><Typography.Text>出口被墙处理</Typography.Text><Switch checked={state?.enabled ?? false} disabled={!state || saving} loading={saving} onChange={value => void toggle(value)} /><Typography.Text>当前地址：{state?.address ?? '加载中'}</Typography.Text></Space>
      <Alert type="info" showIcon title="确认 IP 变化后自动更新相关入口" description="“已应用”表示入口已保存并应用新地址；“已握手”表示该入口已通过新地址的真实隧道握手。两项独立确认，离线入口重连后继续同步。已有确认为本次地址更新的历史进度，不代表当前链路健康。换 IP 可能中断已有连接，其他出口继续接新连接。" />
      {state?.action_id && <Typography.Paragraph style={{ marginTop: 12 }}>本次换 IP 动作：<Typography.Text copyable style={{ wordBreak: 'break-all' }}>{state.action_id}</Typography.Text></Typography.Paragraph>}
      <Table<Entry> rowKey="node_id" size="small" dataSource={state?.entries ?? []} pagination={false} scroll={{ x: 600 }} style={{ marginTop: 12 }} locale={{ emptyText: state?.action_id ? '暂无相关入口' : '尚无出口地址更新' }} columns={[
        { title: '入口', dataIndex: 'name' },
        { title: '控制连接', render: (_, row) => <Tag>{row.connected ? '已连接' : '离线 · 重连后继续'}</Tag> },
        { title: '新地址应用', render: (_, row) => <Tag color={row.applied ? 'success' : 'warning'}>{row.applied ? '已应用' : '待确认'}</Tag> },
        { title: '新地址握手', render: (_, row) => <Tag color={row.handshaken ? 'success' : 'warning'}>{row.handshaken ? '已握手' : '待确认'}</Tag> },
        { title: '握手确认时间', render: (_, row) => row.handshaken_at ? new Date(row.handshaken_at).toLocaleString() : '—' },
      ]} />
    </Modal>
  </>;
}
