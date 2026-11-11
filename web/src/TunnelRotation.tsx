import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Modal, Popconfirm, Space, Spin, Typography } from 'antd';
import { SafetyCertificateOutlined, TeamOutlined } from '@ant-design/icons';
import { api, APIError } from './api';
import { FormSection } from './FormSection';
import { StatusDot } from './StatusDot';
import './Endpoints.css';

type Rotation = {
  phase: string; kind?: string; deadline?: string; overdue: boolean; warning?: string;
  entries: { node_id: string; name: string; confirmed: boolean; connected: boolean }[];
};
const phases: Record<string, string> = { idle: '尚未轮换', preparing: '等待端点准备', distributing: '等待入口确认', activating: '等待端点撤下旧材料', cleaning: '等待入口清理旧证书信任', complete: '已完成' };

export function TunnelRotation({ nodeID, onSessionExpired, onChanged }: { nodeID: string; onSessionExpired: () => void; onChanged: () => void }) {
  const [state, setState] = useState<Rotation>();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const expired = useRef(onSessionExpired); expired.current = onSessionExpired;
  useEffect(() => {
    if (!open) return;
    let disposed = false;
    async function load() {
      try {
        const result = await api<Rotation>(`/nodes/${nodeID}/tunnel/rotation`);
        if (!disposed) { setState(result); setError(''); }
      } catch (e) {
        if (disposed) return;
        if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
        else setError((e as Error).message);
      }
    }
    void load();
    const timer = window.setInterval(() => void load(), 5000);
    return () => { disposed = true; window.clearInterval(timer); };
  }, [nodeID, open]);
  async function rotate(kind: string) {
    setSaving(true);
    try {
      await api(`/nodes/${nodeID}/tunnel/rotation`, 'POST', { kind });
      setState(await api<Rotation>(`/nodes/${nodeID}/tunnel/rotation`));
      setError(''); onChanged();
    } catch (e) {
      if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
      else setError((e as Error).message);
    } finally { setSaving(false); }
  }
  const active = !state || (state.phase !== 'idle' && state.phase !== 'complete');
  return <>
    <Button type="text" size="small" onClick={() => { setState(undefined); setError(''); setOpen(true); }}>证书与凭据轮换</Button>
    <Modal className="endpoint-modal" title="端点证书与隧道凭据轮换" open={open} onCancel={() => { setOpen(false); onChanged(); }} footer={null} closable={!saving} maskClosable={!saving} keyboard={!saving} width={720}>
      {error && <Alert type="error" title={error} showIcon className="message" />}
      <FormSection title="轮换进度" icon={<SafetyCertificateOutlined />} description="轮换先准备端点，再等待所有相关入口确认，随后撤下旧材料。已有业务连接继续转发。满 7 天仍未确认时保留旧材料并提示。">
      {!state && !error && <Spin />}
      {state && <>
        <div className="endpoint-progress"><StatusDot tone={state.overdue ? 'warn' : state.phase === 'complete' ? 'ok' : state.phase === 'idle' ? 'muted' : 'info'}>{phases[state.phase] ?? state.phase}</StatusDot></div>
        {state.deadline && <Typography.Paragraph type="secondary">确认期限：{new Date(state.deadline).toLocaleString()}</Typography.Paragraph>}
        {state.warning && <Alert type="warning" title={state.warning} showIcon className="message" />}
      </>}
      </FormSection>
      {state && <FormSection title="入口确认" icon={<TeamOutlined />} description={<>已确认 <span className="mono">{state.entries.filter(entry => entry.confirmed).length} / {state.entries.length}</span> 个相关入口。</>}>
        <ul className="endpoint-rotation-entries">{state.entries.map(entry => <li key={entry.node_id}><strong>{entry.name}</strong><StatusDot tone={entry.confirmed ? 'ok' : 'warn'}>{entry.confirmed ? '已确认' : '待确认'}</StatusDot><StatusDot tone={entry.connected ? 'ok' : 'muted'}>{entry.connected ? '控制已连接' : '控制未连接'}</StatusDot></li>)}</ul>
        {state.entries.length === 0 && <Typography.Paragraph type="secondary">暂无相关入口，轮换仍需等待端点应用确认。</Typography.Paragraph>}
      </FormSection>}
      <Space wrap className="endpoint-form-footer">
        {[['both', '同时轮换'], ['certificate', '仅轮换证书'], ['credentials', '仅轮换凭据']].map(([kind, label]) => <Popconfirm key={kind} title={`确认${label}？`} description="新旧材料将并存，直到所有相关入口确认。" onConfirm={() => rotate(kind)} disabled={active || saving}><Button disabled={active} loading={saving}>{label}</Button></Popconfirm>)}
      </Space>
    </Modal>
  </>;
}
