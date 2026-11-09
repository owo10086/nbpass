import { useEffect, useState } from 'react';
import { Alert, Button, Descriptions, Form, Input, Modal, Popconfirm, Select, Space, Switch, Tag, Typography } from 'antd';
import { api } from './api';

type RotationNode = { id: string; name: string; connected: boolean };
type Action = { id: string; old_ip: string; new_ip: string; state: string; reason: string; attempts: number; expires_at: string; updated_at: string; observation_complete: boolean; recheck_status: string; recheck_rounds: { result: string; finished_at: string }[] };
type State = { automatic_enabled: boolean; automatic_eligible: boolean; paused: boolean; no_improvement: number; recheck_status: string; quota: { used_hour: number; used_day: number; cooldown_seconds: number; retry_after_seconds: number }; configured: boolean; unsafe_network: boolean; warning: string; action: Action | null; frozen: boolean; freeze_reason: string; pending_operation: string; can_reconcile: boolean; can_unfreeze: boolean };
type Values = { url: string; method: string; headers: string; body: string; private_endpoints: string };
const recheckLabels: Record<string, string> = { checking: '新 IP 正在复检', recovered: '已恢复', no_improvement: '无改善', pending: '待确认' };
const labels: Record<string, string> = { authorized: '等待节点执行', started: '已开始调用', observing: '正在观察 IP', changed: 'IP 已变化', cancelled: '已取消', unconfirmed: '结果未确认', released: '管理员已解除冻结（原结果未知）' };

export function NodeRotation({ node, onClose, onError }: { node?: RotationNode; onClose: () => void; onError: (e: unknown) => void }) {
  const [state, setState] = useState<State>();
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [triggering, setTriggering] = useState(false);
  const [controlling, setControlling] = useState(false);
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
  async function control(operation: 'reconcile' | 'unfreeze') {
    if (!state?.action) return;
    setControlling(true);
    try { await api(`${path}/${operation}`, 'POST', { action_id: state.action.id }); setState(await api<State>(path)); }
    catch (e) { onError(e); }
    finally { setControlling(false); }
  }
  async function policy(operation: 'automatic' | 'resume', enabled?: boolean) {
    setControlling(true);
    try { await api(`${path}/${operation}`, operation === 'automatic' ? 'PUT' : 'POST', operation === 'automatic' ? { enabled } : { confirm: true }); setState(await api<State>(path)); }
    catch (e) { onError(e); }
    finally { setControlling(false); }
  }
  const action = state?.action;
  const busy = !!action && (action.recheck_status === 'checking' || !['changed', 'cancelled', 'released'].includes(action.state));
  function close() { form.resetFields(); setEditing(false); onClose(); }

  return <Modal title={`换 IP${node ? `：${node.name}` : ''}`} open={!!node} onCancel={close} footer={null} width={720} destroyOnHidden>
    <Alert type="warning" showIcon title="换 IP 可能中断已有连接。" description="观察到公网 IPv4 变化才算换成，可达性仍需复检。换成后通过协调器更新仍使用该节点旧 IP 的受管记录。" />
    {state && <Descriptions column={1} size="small" bordered style={{ marginTop: 12 }} items={[
      { key: 'automatic', label: '节点自动换 IP 总授权', children: <Space><Switch checked={state.automatic_enabled} disabled={!state.configured || controlling} onChange={value => void policy('automatic', value)} /><Typography.Text type="secondary">由入口组策略①申请</Typography.Text></Space> },
      { key: 'quota', label: '节点共享额度', children: `滚动 1 小时 ${state.quota.used_hour} / 1 次；滚动 24 小时 ${state.quota.used_day} / 3 次（手动也计入）` },
      { key: 'cooldown', label: '自动换 IP 等待', children: `冷却剩余 ${Math.ceil(state.quota.cooldown_seconds / 60)} 分钟；额度最早释放约 ${Math.ceil(state.quota.retry_after_seconds / 60)} 分钟后` },
      { key: 'no-improvement', label: '连续无改善', children: `${state.no_improvement} 次${state.paused ? ' · 自动换 IP 已暂停' : ''}` },
      { key: 'recheck', label: '新 IP 复检', children: recheckLabels[state.recheck_status] || '尚无复检结果' },
    ]} />}
    {state?.paused && <Alert style={{ marginTop: 12 }} type="warning" showIcon title="连续两次换 IP 无改善，自动换 IP 已暂停" description={<Popconfirm title="解除无改善暂停？" description="解除保留已用额度；后续正常自测也会解除暂停。" onConfirm={() => policy('resume')}><Button disabled={controlling}>手动解除无改善暂停</Button></Popconfirm>} />}
    {state?.warning && <Alert style={{ marginTop: 12 }} type="warning" showIcon title={state.warning} />}
    {state?.frozen && <Alert style={{ marginTop: 12 }} type="warning" showIcon title="自动换 IP 已冻结" description={<>
      <div>{state.freeze_reason}</div>
      <div>{action?.observation_complete ? '请先核对公网 IP。仍为旧 IP 不能证明商家未执行；手动解除表示接受原结果未知，保留历史及已用额度。' : '继续观察并记录，同一动作不会重调接口；本轮观察结束后可重新核对或手动解除。'}</div>
      <Space wrap style={{ marginTop: 8 }}>
        <Button disabled={!state.can_reconcile || controlling} loading={controlling} onClick={() => void control('reconcile')}>重新核对 IP</Button>
        <Popconfirm title="解除结果未确认冻结？" description="原接口可能已经执行。解除不代表换成，不清空去重记录或已用额度；节点确认后才允许新动作。" onConfirm={() => control('unfreeze')}>
          <Button danger disabled={!state.can_unfreeze || controlling} loading={controlling}>手动解除冻结</Button>
        </Popconfirm>
      </Space>
    </>} />}
    {state?.pending_operation && <Alert style={{ marginTop: 12 }} type="info" showIcon title={state.pending_operation === 'release' ? '解除请求已排队，等待节点持久化确认' : '正在重新核对公网 IP'} description="节点离线时保留请求，重连后继续处理；确认前保持冻结与动作互斥。" />}
    <Space style={{ margin: '16px 0' }} wrap>
      <Tag>{state?.configured ? '接口已配置，凭据不回显' : '尚未配置接口'}</Tag>
      <Button disabled={!state} onClick={() => { form.resetFields(); setEditing(!editing); }}>{editing ? '收起设置' : state?.configured ? '替换接口设置' : '配置换 IP 接口'}</Button>
      <Popconfirm title="立即手动换 IP？" description="手动换 IP 不受冷却与次数限制，执行后计入额度；进行中、复检中或结果未确认时不能再次发起。" onConfirm={trigger}>
        <Button danger disabled={!state?.configured || !node?.connected || busy} loading={triggering}>手动换 IP</Button>
      </Popconfirm>
    </Space>
    {!node?.connected && <Typography.Paragraph type="secondary">节点控制连接离线，无法签发执行许可。</Typography.Paragraph>}
    {action && <Descriptions column={1} size="small" bordered items={[
      { key: 'state', label: '执行进度', children: <Tag color={action.state === 'changed' ? 'green' : action.state === 'unconfirmed' ? 'orange' : 'blue'}>{labels[action.state] || action.state}</Tag> },
      { key: 'reason', label: '说明', children: action.reason || '两分钟内有效的执行许可已下发' },
      { key: 'address', label: '公网 IPv4', children: `${action.old_ip} → ${action.new_ip || '待确认'}` },
      { key: 'recheck-rounds', label: '新 IP 复检轮次', children: action.recheck_rounds?.map((r, i) => `${i + 1}：${({ normal: '正常', abnormal: '异常', unknown: '未知' })[r.result as 'normal' | 'abnormal' | 'unknown']}（${new Date(r.finished_at).toLocaleString()}）`).join('；') || '等待确认新 IP' },
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
