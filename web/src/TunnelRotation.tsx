import { useEffect, useState } from 'react';
import { Alert, Button, Modal, Popconfirm, Space, Tag, Typography } from 'antd';
import { api, APIError } from './api';

type Rotation = {
  phase: string; kind?: string; deadline?: string; overdue: boolean; warning?: string;
  entries: { node_id: string; name: string; confirmed: boolean; connected: boolean }[];
};
const phases: Record<string, string> = { idle: '尚未轮换', preparing: '等待出口准备', distributing: '等待入口确认', activating: '等待出口撤下旧材料', cleaning: '等待入口清理旧证书信任', complete: '已完成' };

export function TunnelRotation({ nodeID, onSessionExpired, onChanged }: { nodeID: string; onSessionExpired: () => void; onChanged: () => void }) {
  const [state, setState] = useState<Rotation>();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (!open) return;
    let disposed = false;
    async function load() {
      try {
        const result = await api<Rotation>(`/nodes/${nodeID}/tunnel/rotation`);
        if (!disposed) { setState(result); setError(''); }
      } catch (e) {
        if (disposed) return;
        if (e instanceof APIError && (e.status === 401 || e.status === 428)) onSessionExpired();
        else setError((e as Error).message);
      }
    }
    void load();
    const timer = window.setInterval(() => void load(), 5000);
    return () => { disposed = true; window.clearInterval(timer); };
  }, [nodeID, open, onSessionExpired]);
  async function rotate(kind: string) {
    setSaving(true);
    try {
      await api(`/nodes/${nodeID}/tunnel/rotation`, 'POST', { kind });
      setState(await api<Rotation>(`/nodes/${nodeID}/tunnel/rotation`));
      setError(''); onChanged();
    } catch (e) {
      if (e instanceof APIError && (e.status === 401 || e.status === 428)) onSessionExpired();
      else setError((e as Error).message);
    } finally { setSaving(false); }
  }
  const active = !state || (state.phase !== 'idle' && state.phase !== 'complete');
  return <>
    <Button size="small" onClick={() => setOpen(true)}>证书与凭据轮换</Button>
    <Modal title="出口证书与隧道凭据轮换" open={open} onCancel={() => { setOpen(false); onChanged(); }} footer={null}>
      {error && <Alert type="error" title={error} showIcon className="message" />}
      <Typography.Paragraph>轮换先准备出口，再等待所有相关入口确认，随后撤下旧材料。已有业务连接继续转发。满 7 天仍未确认时保留旧材料并提示。</Typography.Paragraph>
      {state && <>
        <Tag color={state.overdue ? 'warning' : state.phase === 'complete' ? 'success' : 'processing'}>{phases[state.phase] ?? state.phase}</Tag>
        {state.deadline && <Typography.Paragraph type="secondary">确认期限：{new Date(state.deadline).toLocaleString()}</Typography.Paragraph>}
        {state.warning && <Alert type="warning" title={state.warning} showIcon className="message" />}
        <Typography.Paragraph>入口确认：{state.entries.filter(e => e.confirmed).length} / {state.entries.length}</Typography.Paragraph>
        {state.entries.map(entry => <div key={entry.node_id} style={{ marginBottom: 8 }}><Space wrap><Typography.Text>{entry.name}</Typography.Text><Tag color={entry.confirmed ? 'success' : 'warning'}>{entry.confirmed ? '已确认' : '待确认'}</Tag><Tag>{entry.connected ? '控制已连接' : '控制未连接'}</Tag></Space></div>)}
        {state.entries.length === 0 && <Typography.Paragraph type="secondary">暂无相关入口，轮换仍需等待出口应用确认。</Typography.Paragraph>}
      </>}
      <Space wrap style={{ marginTop: 16 }}>
        {[['both', '同时轮换'], ['certificate', '仅轮换证书'], ['credentials', '仅轮换凭据']].map(([kind, label]) => <Popconfirm key={kind} title={`确认${label}？`} description="新旧材料将并存，直到所有相关入口确认。" onConfirm={() => rotate(kind)} disabled={active || saving}><Button disabled={active} loading={saving}>{label}</Button></Popconfirm>)}
      </Space>
    </Modal>
  </>;
}
