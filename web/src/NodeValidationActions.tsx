import { useState } from 'react';
import { Alert, Button, Form, InputNumber, Modal, Space, Table, Typography } from 'antd';
import { api, APIError } from './api';

type Reference = { kind: 'rule' | 'entry_group' | 'exit_group'; id: string; name: string };
type ListenerPorts = { tunnel_listen_port: number; check_port: number };
type Node = { id: string; name: string };

const kindLabels = { rule: '转发规则', entry_group: '入口组', exit_group: '出口组' };

export function NodeValidationActions({ node, onSaved, onError }: { node: Node; onSaved: () => Promise<void>; onError: (e: unknown) => void }) {
  const [view, setView] = useState<'ports' | 'delete'>();
  const [references, setReferences] = useState<Reference[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [form] = Form.useForm<ListenerPorts>();

  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) onError(e);
    else setError((e as Error).message);
  }

  async function open(mode: 'ports' | 'delete') {
    setError(''); setLoading(true); setView(mode); setReferences([]); form.resetFields();
    try {
      if (mode === 'ports') form.setFieldsValue(await api<ListenerPorts>(`/nodes/${node.id}/listener-ports`));
      else setReferences((await api<{ references: Reference[] }>(`/nodes/${node.id}/references`)).references);
    } catch (e) { failed(e); }
    finally { setLoading(false); }
  }

  async function save(ports: ListenerPorts) {
    setLoading(true); setError('');
    try { await api(`/nodes/${node.id}/listener-ports`, 'PUT', ports); setView(undefined); await onSaved(); }
    catch (e) { failed(e); }
    finally { setLoading(false); }
  }

  async function remove() {
    setLoading(true); setError('');
    try { await api(`/nodes/${node.id}`, 'DELETE'); setView(undefined); await onSaved(); }
    catch (e) {
      if (e instanceof APIError && Array.isArray(e.details.references)) setReferences(e.details.references as Reference[]);
      failed(e);
    } finally { setLoading(false); }
  }

  return <>
    <Button size="small" onClick={() => void open('ports')}>监听端口</Button>
    <Button danger size="small" onClick={() => void open('delete')}>删除</Button>
    <Modal title={`节点「${node.name}」的监听端口`} open={view === 'ports'} onCancel={() => setView(undefined)} footer={null} forceRender>
      <Form form={form} layout="vertical" onFinish={save}>
        {error && <Alert title={error} type="error" showIcon className="message" />}
        <Typography.Paragraph type="secondary">为隧道监听和固定检测服务预留 TCP 端口，转发规则不能占用。这两项服务将在后续版本接入。</Typography.Paragraph>
        <Form.Item name="tunnel_listen_port" label="隧道监听端口" rules={[{ required: true, message: '请输入隧道监听端口' }]}><InputNumber min={1} max={65535} precision={0} disabled={loading} style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="check_port" label="固定检测端口" rules={[{ required: true, message: '请输入检测端口' }]}><InputNumber min={1} max={65535} precision={0} disabled={loading} style={{ width: '100%' }} /></Form.Item>
        <Button type="primary" htmlType="submit" block loading={loading}>保存端口</Button>
      </Form>
    </Modal>
    <Modal title={`删除节点「${node.name}」`} open={view === 'delete'} onCancel={() => setView(undefined)} width={680} footer={<Space><Button onClick={() => setView(undefined)}>取消</Button><Button danger type="primary" loading={loading} disabled={!!error || references.length > 0} onClick={() => void remove()}>确认删除节点</Button></Space>}>
      {error && <Alert title={error} type="error" showIcon className="message" />}
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
