import { useState } from 'react';
import { Button, Popconfirm, Space, Tag, Typography } from 'antd';
import { api } from './api';

type Upgrade = { state: string; steps: string[]; error?: string; manifest: { version: string } };
export type NodeUpgradeFields = { panel_version: string; version_mismatch: boolean; upgrade_capable: boolean; upgrade?: Upgrade };
const labels: Record<string, string> = { queued: '等待下载', downloading: '下载', verifying: '校验', switching: '切换', reconnecting: '回连', ready: '就绪', rolling_back: '正在回滚', rolled_back: '已回滚', failed: '失败', unconfirmed: '未确认' };
const terminal = new Set(['ready', 'rolled_back', 'failed', 'unconfirmed']);

export function NodeUpgrade({ node, onSaved, onError }: { node: NodeUpgradeFields & { id: string; connected: boolean }; onSaved: () => Promise<void>; onError: (e: unknown) => void }) {
  const [saving, setSaving] = useState(false);
  async function upgrade() {
    setSaving(true);
    try { await api(`/nodes/${node.id}/upgrade`, 'POST'); await onSaved(); }
    catch (e) { onError(e); }
    finally { setSaving(false); }
  }
  return <Space orientation="vertical" size={2}>
    {node.version_mismatch && <Tag color="orange">与面板 {node.panel_version} 版本不一致</Tag>}
    {node.upgrade && <>
      <Tag color={node.upgrade.state === 'ready' ? 'green' : node.upgrade.state === 'rolled_back' || node.upgrade.state === 'failed' ? 'red' : 'blue'}>升级至 {node.upgrade.manifest.version}：{labels[node.upgrade.state] || node.upgrade.state}</Tag>
      <Typography.Text type="secondary">{node.upgrade.steps.map(step => labels[step] || step).join(' → ')}</Typography.Text>
      {node.upgrade.error && <Typography.Text type="danger">{node.upgrade.error}</Typography.Text>}
    </>}
    <Popconfirm title={`升级节点至 ${node.panel_version}？`} description="升级会断开此节点上的现有连接；节点将进入最多 10 分钟的维护期。" onConfirm={upgrade}>
      <Button size="small" loading={saving} disabled={!node.connected || !node.upgrade_capable || (!!node.upgrade && !terminal.has(node.upgrade.state))}>升级至面板版本</Button>
    </Popconfirm>
    {!node.upgrade_capable && <Typography.Text type="secondary">安装独立更新服务后可升级</Typography.Text>}
  </Space>;
}
