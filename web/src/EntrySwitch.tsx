import { Button, Checkbox, Form, Input, Popconfirm, Select, Space, Typography } from 'antd';
import { SwapOutlined } from '@ant-design/icons';
import { api } from './api';
import { FormSection } from './FormSection';
import { StatusDot } from './StatusDot';

export type EntrySwitchState = {
  desired_version: number;
  preferred_node_id: string;
  auto_failback_paused: boolean;
  auto_failback_status: string;
};

export function EntrySwitch({ cname, path, members, nodes, publication, saving, setSaving, onStart, onChanged, onError }: {
  cname?: boolean;
  path: string;
  members: string[];
  nodes: { id: string; name: string }[];
  publication: EntrySwitchState;
  saving: boolean;
  setSaving: (saving: boolean) => void;
  onStart: () => void;
  onChanged: () => Promise<void>;
  onError: (e: unknown) => void;
}) {
  const [form] = Form.useForm();
  async function submit(values: { active_node_id: string; ipv4: string; confirm_inbound: boolean }) {
    setSaving(true); onStart();
    try {
      await api(`${path}/switch`, 'POST', { ...values, expected_version: publication.desired_version });
      form.resetFields(); await onChanged();
    } catch (e) { onError(e); await onChanged(); }
    finally { setSaving(false); }
  }
  async function resume() {
    setSaving(true); onStart();
    try { await api(`${path}/resume-failback`, 'POST', { expected_version: publication.desired_version }); await onChanged(); }
    catch (e) { onError(e); await onChanged(); }
    finally { setSaving(false); }
  }
  return <FormSection title="手动切换与自动回切" icon={<SwapOutlined />}>
    <Space wrap style={{ marginBottom: 16 }}>
      <StatusDot tone={publication.auto_failback_paused ? 'warn' : 'ok'}>{publication.auto_failback_status}</StatusDot>
      {publication.auto_failback_paused && <Popconfirm title="恢复自动回切？" description="恢复后按首选入口的恢复条件回切。" onConfirm={resume}>
        <Button loading={saving}>恢复自动回切</Button>
      </Popconfirm>}
    </Space>
    <Form form={form} layout="vertical" onFinish={submit} disabled={saving}>
      <Form.Item name="active_node_id" label="切换到" rules={[{ required: true, message: '请选择目标成员' }]}>
        <Select options={members.map(id => ({ value: id, label: `${nodes.find(n => n.id === id)?.name ?? id}${publication.preferred_node_id === id ? '（首选入口）' : ''}` }))} />
      </Form.Item>
      {cname
        ? <Form.Item name="ipv4" label="期望解析 IPv4" extra="可留空：取目标服务器受管记录当前的地址。CNAME 改指向该记录，它须已首次发布、启用且地址一致。"><Input maxLength={15} placeholder="留空自动取服务器记录地址" /></Form.Item>
        : <Form.Item name="ipv4" label="该成员可入站的公网 IPv4" rules={[{ required: true, message: '请输入并核对入站地址' }]}><Input maxLength={15} placeholder="例如 1.1.1.1" /></Form.Item>}
      <Form.Item name="confirm_inbound" valuePropName="checked" rules={[{ validator: (_, v) => v ? Promise.resolve() : Promise.reject(new Error('请确认成员与入站地址')) }]}>
        <Checkbox>我确认该 IPv4 属于所选成员且可直接接收业务连接，同意替换受管记录。</Checkbox>
      </Form.Item>
      <Typography.Paragraph type="secondary">切到非首选成员后暂停自动回切，直到手动恢复。DNS 缓存可能暂时保留旧值。</Typography.Paragraph>
      <Button type="primary" htmlType="submit" loading={saving}>确认手动切换</Button>
    </Form>
  </FormSection>;
}
