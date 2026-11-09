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
    <Select aria-label="被墙处理策略" style={{ width: '100%' }} value={policy} disabled={!policy || saving} loading={saving} onChange={value => void save(value)} options={[{ value: 'disabled', label: '停用自动处理' }, { value: 'rotate_ip', label: '策略①：仅换 IP' }, { value: 'switch_entry', label: '策略②：仅入口切换' }, { value: 'switch_then_rotate', label: '策略③：先入口切换再换 IP（尚未实现）', disabled: true }]} />
    <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>{policy === 'switch_entry' ? '活动入口认定被墙后切到就绪的候选成员；没有候选时保留最后的解析并通知，本组不发起换 IP。非活动成员被墙只排除候选。首选入口恢复后按连续成功、驻留时间和手动暂停规则回切。其他入口组或出口角色仍可按各自设置申请换 IP。' : policy === 'rotate_ip' ? '成员认定被墙后，需节点已开启自动换 IP 才会申请；受节点共享额度、暂停、冻结及维护限制。换成后更新仍使用该节点旧 IP 的受管记录，新 IP 另做自测复检。' : '停用后，本入口组不因成员被墙发起自动处理；离线故障切换和首选入口回切仍按各自规则执行。'}</Typography.Paragraph>
  </Card>;
}
