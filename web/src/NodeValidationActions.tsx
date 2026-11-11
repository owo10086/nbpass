import { useEffect, useState } from 'react';
import { Alert, Button, Form, InputNumber, Modal, Space, Table, Typography } from 'antd';
import { api, APIError } from './api';

type Reference = { kind: 'rule' | 'entry_group' | 'exit_group'; id: string; name: string };
type ListenerPorts = { tunnel_listen_port: number; check_port: number };
type Node = { id: string; name: string };

const kindLabels = { rule: '转发规则', entry_group: '入口组', exit_group: '出口组' };

export type NodeValidationMode = 'ports' | 'delete';

// 由节点列表的「更多」菜单打开；mode 为空时不显示任何对话框。
export function NodeValidationActions({ node, mode, onClose, onSaved, onError }: { node: Node; mode?: NodeValidationMode; onClose: () => void; onSaved: () => Promise<void>; onError: (e: unknown) => void }) {
  const [view, setView] = useState<NodeValidationMode>();
  const [warnings, setWarnings] = useState<string[]>([]);
  const [references, setReferences] = useState<Reference[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [form] = Form.useForm<ListenerPorts>();

  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) onError(e);
    else setError((e as Error).message);
  }

  useEffect(() => { if (mode) void open(mode); else setView(undefined); }, [mode, node.id]);

  async function open(mode: NodeValidationMode) {
    setError(''); setLoading(true); setView(mode); setReferences([]); setWarnings([]); form.resetFields();
    try {
      if (mode === 'ports') form.setFieldsValue(await api<ListenerPorts>(`/nodes/${node.id}/listener-ports`));
      else { const result = await api<{ references: Reference[]; warnings: string[] }>(`/nodes/${node.id}/references`); setReferences(result.references); setWarnings(result.warnings); }
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }

  async function save(ports: ListenerPorts) {
    setLoading(true); setError('');
    try { await api(`/nodes/${node.id}/listener-ports`, 'PUT', ports); setView(undefined); onClose(); await onSaved(); }
    catch (e) { failed(e); }
    finally { setLoading(false); }
  }

  async function remove() {
    setLoading(true); setError('');
    try { await api(`/nodes/${node.id}`, 'DELETE'); setView(undefined); onClose(); await onSaved(); }
    catch (e) {
      if (e instanceof APIError && Array.isArray(e.details.references)) setReferences(e.details.references as Reference[]);
      failed(e);
    } finally { setLoading(false); }
  }

  return <>
    <Modal title={`节点「${node.name}」的监听端口`} open={view === 'ports'} onCancel={onClose} footer={null} forceRender>
      <Form form={form} layout="vertical" onFinish={save}>
        {error && <Alert title={error} type="error" showIcon className="message" />}
        <Typography.Paragraph type="secondary">固定检测服务监听下方检测端口，用于国内检测点补判，请放行相应 TCP 入站连接。转发规则不能占用检测端口或隧道预留端口。</Typography.Paragraph>
        <Form.Item name="tunnel_listen_port" label="隧道监听端口" rules={[{ required: true, message: '请输入隧道监听端口' }]}><InputNumber min={1} max={65535} precision={0} disabled={loading} style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="check_port" label="固定检测端口" rules={[{ required: true, message: '请输入检测端口' }]}><InputNumber min={1} max={65535} precision={0} disabled={loading} style={{ width: '100%' }} /></Form.Item>
        <Button type="primary" htmlType="submit" block loading={loading}>保存端口</Button>
      </Form>
    </Modal>
    <Modal title={`删除节点「${node.name}」`} open={view === 'delete'} onCancel={onClose} width={680} footer={<Space><Button onClick={onClose}>取消</Button><Button danger type="primary" loading={loading} disabled={!!error || references.length > 0} onClick={() => void remove()}>确认删除节点</Button></Space>}>
      {error && <Alert title={error} type="error" showIcon className="message" />}
      {warnings.map((warning, i) => <Alert key={i} title={warning} type="warning" showIcon className="message" />)}
      {references.length > 0 ? <>
        <Alert title="节点仍被引用，请先移除以下配置中的关联。" type="warning" showIcon className="message" />
        <Table<Reference> size="small" rowKey={ref => `${ref.kind}:${ref.id}`} dataSource={references} pagination={false} scroll={{ x: 440, y: 320 }} columns={[
          { title: '引用类型', render: (_, ref) => kindLabels[ref.kind] },
          { title: '名称', dataIndex: 'name' },
          { title: '标识', dataIndex: 'id', render: value => <Typography.Text copyable style={{ wordBreak: 'break-all' }}>{value}</Typography.Text> },
        ]} />
      </> : <Typography.Paragraph>{loading ? '正在检查引用…' : '当前没有规则或分组引用。删除会使节点凭据和安装票据失效，并断开控制连接。'}</Typography.Paragraph>}
    </Modal>
  </>;
}
