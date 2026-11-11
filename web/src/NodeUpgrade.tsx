import { useState } from 'react';
import { Button, Popconfirm } from 'antd';
import { api } from './api';
import { StatusDot } from './StatusDot';

type Upgrade = { state: string; steps: string[]; error?: string; manifest: { version: string } };
export type NodeUpgradeFields = { panel_version: string; version_mismatch: boolean; upgrade_capable: boolean; upgrade?: Upgrade };
const labels: Record<string, string> = { queued: '等待下载', downloading: '下载', verifying: '校验', switching: '切换', reconnecting: '回连', ready: '就绪', rolling_back: '正在回滚', rolled_back: '已回滚', failed: '失败', unconfirmed: '未确认' };
const terminal = new Set(['ready', 'rolled_back', 'failed']);

export function NodeUpgrade({ node, onSaved, onError }: { node: NodeUpgradeFields & { id: string; connected: boolean }; onSaved: () => Promise<void>; onError: (e: unknown) => void }) {
  const [saving, setSaving] = useState(false);
  async function upgrade() {
    setSaving(true);
    try { await api(`/nodes/${node.id}/upgrade`, 'POST'); await onSaved(); }
    catch (e) { onError(e); }
    finally { setSaving(false); }
  }
  const state = node.upgrade?.state;
  const tone = state === 'ready' ? 'ok' : state === 'failed' || state === 'rolled_back' ? 'danger' : 'warn';
  // 版本一致且没有进行中的升级时，不再在每行重复显示升级按钮。
  const offer = node.version_mismatch || (!!node.upgrade && state !== 'ready');
  return <div>
    {node.upgrade
      ? <StatusDot tone={tone}>升级至 <span className="mono">{node.upgrade.manifest.version}</span>：{labels[state!] || state}</StatusDot>
      : node.version_mismatch ? <StatusDot tone="warn">与面板 <span className="mono">{node.panel_version}</span> 不一致</StatusDot>
      : <span className="muted">与面板一致</span>}
    {node.upgrade && node.upgrade.steps.length > 0 && <span className="cell-sub">{node.upgrade.steps.map(step => labels[step] || step).join(' → ')}</span>}
    {node.upgrade?.error && <span className="cell-danger">{node.upgrade.error}</span>}
    {state === 'unconfirmed' && <span className="cell-sub">等待服务器回连核对，当前程序保留；请勿重复升级。</span>}
    {offer && <div style={{ marginTop: 4 }}>
      <Popconfirm title={`升级服务器至 ${node.panel_version}？`} description="升级会断开此服务器上的现有连接；服务器将进入最多 10 分钟的维护期。" onConfirm={upgrade}>
        <Button size="small" loading={saving} disabled={!node.connected || !node.upgrade_capable || (!!node.upgrade && !terminal.has(node.upgrade.state))}>升级至面板版本</Button>
      </Popconfirm>
      {!node.upgrade_capable && <span className="cell-sub">安装独立更新服务后可升级</span>}
    </div>}
  </div>;
}
