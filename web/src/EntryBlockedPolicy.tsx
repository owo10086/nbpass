import { useEffect, useState } from 'react';
import { Card, Select, Typography } from 'antd';
import { api } from './api';

export function EntryBlockedPolicy({ path, onError }: { path: string; onError: (e: unknown) => void }) {
  const [policy, setPolicy] = useState<string>();
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    let active = true;
    void api<{ policy: string }>(`${path}/blocked-policy`).then(v => { if (active) setPolicy(v.policy); }).catch(e => { if (active) onError(e); });
    return () => { active = false; };
  }, [path]);
  async function save(value: string) {
    setSaving(true);
    try { const v = await api<{ policy: string }>(`${path}/blocked-policy`, 'PUT', { policy: value }); setPolicy(v.policy); }
    catch (e) { onError(e); }
    finally { setSaving(false); }
  }
  return <Card title="被墙处理策略">
    <Select aria-label="被墙处理策略" style={{ width: '100%' }} value={policy} disabled={!policy || saving} loading={saving} onChange={value => void save(value)} options={[{ value: 'disabled', label: '停用自动处理' }, { value: 'rotate_ip', label: '策略①：仅换 IP' }]} />
    <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>成员认定被墙后，需节点已开启自动换 IP 才会申请；受节点共享额度、暂停、冻结及维护限制。换成后更新仍使用该节点旧 IP 的受管记录，新 IP 另做自测复检。</Typography.Paragraph>
  </Card>;
}
