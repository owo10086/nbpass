import { useEffect, useState } from 'react';
import { Select, Typography } from 'antd';
import { SafetyOutlined } from '@ant-design/icons';
import { api } from './api';
import { FormSection } from './FormSection';

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
  return <FormSection title="被墙处理策略" icon={<SafetyOutlined />}>
    <Select aria-label="被墙处理策略" style={{ width: '100%' }} value={policy} disabled={!policy || saving} loading={saving} onChange={value => void save(value)} options={[{ value: 'disabled', label: '停用自动处理' }, { value: 'rotate_ip', label: '策略①：仅换 IP' }, { value: 'switch_entry', label: '策略②：仅入口切换' }, { value: 'switch_then_rotate', label: '策略③：先入口切换再换 IP' }]} />
    <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>{policy === 'switch_then_rotate' ? '活动入口认定被墙后先切到就绪备用，确认受管记录写入后再给原服务器换 IP；没有备用时退化为仅换 IP。需服务器开启自动换 IP，额度、暂停和冻结与其他用途共用。新 IP 复检恢复后，首选入口按连续成功、驻留及暂停规则回切；复检异常、待确认、失败或受限时留在备用并通知。管理员手动切换会使本次自动切回失效。' : policy === 'switch_entry' ? '活动入口认定被墙后切到就绪的候选成员；没有候选时保留最后的解析并通知，本组不发起换 IP。非活动成员被墙只排除候选。首选入口恢复后按连续成功、驻留时间和手动暂停规则回切。其他服务器组或转发端点角色仍可按各自设置申请换 IP。' : policy === 'rotate_ip' ? '成员认定被墙后，需服务器已开启自动换 IP 才会申请；受服务器共享额度、暂停、冻结及维护限制。换成后更新仍使用该服务器旧 IP 的受管记录，新 IP 另做自测复检。' : '停用后，本服务器组不因成员被墙发起自动处理；离线故障切换和首选入口回切仍按各自规则执行。'}</Typography.Paragraph>
  </FormSection>;
}
