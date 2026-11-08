import { useEffect, useState } from 'react';
import { Alert, Button, Descriptions, Form, Input, Modal, Popconfirm, Select, Space, Tag, Typography } from 'antd';
import { api } from './api';

type RotationNode = { id: string; name: string; connected: boolean };
type Action = { id: string; old_ip: string; new_ip: string; state: string; reason: string; attempts: number; expires_at: string; updated_at: string };
type State = { configured: boolean; unsafe_network: boolean; warning: string; action: Action | null };
type Values = { url: string; method: string; headers: string; body: string; private_endpoints: string };
const labels: Record<string, string> = { authorized: '等待节点执行', started: '已开始调用', observing: '正在观察 IP', changed: 'IP 已变化', cancelled: '已取消', unconfirmed: '结果未确认' };

export function NodeRotation({ node, onClose, onError }: { node?: RotationNode; onClose: () => void; onError: (e: unknown) => void }) {
  const [state, setState] = useState<State>();
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [triggering, setTriggering] = useState(false);
  const [form] = Form.useForm<Values>();
  const id = node?.id;
  const path = `/nodes/${id}/ip-rotation`;

  useEffect(() => {
    setState(undefined); setEditing(false); form.resetFields();
    if (!id) return;
    let active = true;
    async function load() {
      try { const result = await api<State>(`/nodes/${id}/ip-rotation`); if (active) setState(result); }
      catch (e) { if (active) onError(e); }
    }
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => { active = false; window.clearInterval(timer); form.resetFields(); };
  }, [id, form]);

  async function save(values: Values) {
    setSaving(true);
    try {
      let headers: unknown;
      try { headers = JSON.parse(values.headers || '{}'); } catch { throw new Error('请求头请填写 JSON 对象'); }
      if (headers === null || Array.isArray(headers) || typeof headers !== 'object' || Object.values(headers).some(v => typeof v !== 'string')) throw new Error('请求头必须是字符串值的 JSON 对象');
      await api(path, 'PUT', { url: values.url, method: values.method, headers, body: values.body || '', private_endpoints: (values.private_endpoints || '').split('\n').map(v => v.trim()).filter(Boolean) });
      form.resetFields(); setEditing(false); setState(await api<State>(path));
    } catch (e) { onError(e); }
    finally { setSaving(false); }
  }
  async function trigger() {
    setTriggering(true);
    try { await api(`${path}/actions`, 'POST'); setState(await api<State>(path)); }
    catch (e) { onError(e); }
    finally { setTriggering(false); }
  }
  const action = state?.action;
  const busy = !!action && !['changed', 'cancelled'].includes(action.state);
  function close() { form.resetFields(); setEditing(false); onClose(); }

  return <Modal title={`换 IP${node ? `：${node.name}` : ''}`} open={!!node} onCancel={close} footer={null} width={720} destroyOnHidden>
    <Alert type="warning" showIcon title="换 IP 可能中断已有连接。" description="观察到公网 IPv4 变化才算换成，可达性仍需复检。当前操作不更新受管记录或出口地址。" />
    {state?.warning && <Alert style={{ marginTop: 12 }} type="warning" showIcon title={state.warning} />}
    <Space style={{ margin: '16px 0' }} wrap>
      <Tag>{state?.configured ? '接口已配置，凭据不回显' : '尚未配置接口'}</Tag>
      <Button disabled={!state} onClick={() => { form.resetFields(); setEditing(!editing); }}>{editing ? '收起设置' : state?.configured ? '替换接口设置' : '配置换 IP 接口'}</Button>
      <Popconfirm title="立即手动换 IP？" description="请确认目标为该节点；进行中或结果未确认时不能再次发起。" onConfirm={trigger}>
        <Button danger disabled={!state?.configured || !node?.connected || busy} loading={triggering}>手动换 IP</Button>
      </Popconfirm>
    </Space>
    {!node?.connected && <Typography.Paragraph type="secondary">节点控制连接离线，无法签发执行许可。</Typography.Paragraph>}
    {action && <Descriptions column={1} size="small" bordered items={[
      { key: 'state', label: '执行进度', children: <Tag color={action.state === 'changed' ? 'green' : action.state === 'unconfirmed' ? 'orange' : 'blue'}>{labels[action.state] || action.state}</Tag> },
      { key: 'reason', label: '说明', children: action.reason || '两分钟内有效的执行许可已下发' },
      { key: 'address', label: '公网 IPv4', children: `${action.old_ip} → ${action.new_ip || '待确认'}` },
      { key: 'attempts', label: '换后观察', children: `${action.attempts} / 5 次，每次间隔 60 秒` },
      { key: 'expires', label: '许可到期', children: new Date(action.expires_at).toLocaleString() },
      { key: 'id', label: '动作 ID', children: <Typography.Text copyable style={{ wordBreak: 'break-all' }}>{action.id}</Typography.Text> },
    ]} />}
    {editing && <Form form={form} layout="vertical" onFinish={save} initialValues={{ method: 'POST', headers: '{}' }} clearOnDestroy style={{ marginTop: 16 }}>
      <Typography.Paragraph type="secondary">每次保存替换完整设置。可用变量：{'{{node_id}}、{{node_name}}、{{old_ip}}、{{action_id}}、{{timestamp}}'}。变量仅用于 URL 路径、查询参数值、请求头值和 JSON 字符串值。</Typography.Paragraph>
      <Form.Item name="url" label="接口 URL" rules={[{ required: true, message: '请输入接口 URL' }]}><Input autoComplete="off" placeholder="https://vendor.example/change" maxLength={4096} /></Form.Item>
      <Form.Item name="method" label="HTTP 方法" rules={[{ required: true }]}><Select options={['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD'].map(value => ({ value, label: value }))} /></Form.Item>
      <Form.Item name="headers" label="请求头（JSON 对象）"><Input.TextArea autoComplete="off" rows={3} placeholder={'{"Authorization":"Bearer ..."}'} /></Form.Item>
      <Form.Item name="body" label="请求体（JSON，可留空）"><Input.TextArea autoComplete="off" rows={4} placeholder={'{"instance":"{{node_id}}"}'} maxLength={4096} /></Form.Item>
      <Form.Item name="private_endpoints" label="显式授权的内网端点（每行一个，含协议、主机、端口和精确路径）" extra="默认禁止访问内网。仅授权你管理的具体接口；重定向路径也需逐项授权。"><Input.TextArea autoComplete="off" rows={2} placeholder="http://10.0.0.2:8000/change" /></Form.Item>
      <Button type="primary" htmlType="submit" loading={saving} block>加密保存完整设置</Button>
    </Form>}
  </Modal>;
}
