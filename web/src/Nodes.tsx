import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Form, Input, Modal, Popconfirm, Space, Table, Tag, Typography } from 'antd';
import { api, APIError } from './api';
import { NodeRegion, type NodeRegionFields } from './NodeRegion';
import { ProbeDetails } from './ProbeDetails';
import { NodeValidationActions } from './NodeValidationActions';
import { NodeRotation } from './NodeRotation';

type NodeRow = NodeRegionFields & {
  id: string; name: string; status: string; status_label: string; connected: boolean;
  last_seen_at: string | null; maintenance_until: string | null;
  desired_revision: number; applied_revision: number; config_status: string; software_version: string; apply_error: string;
};
type Installation = { id: string; installation_command: string; ticket_expires_at: string };

export function Nodes({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [rows, setRows] = useState<NodeRow[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [adding, setAdding] = useState(false);
  const [saving, setSaving] = useState(false);
  const [installation, setInstallation] = useState<Installation>();
  const [regionNode, setRegionNode] = useState<NodeRow>();
  const [rotationNode, setRotationNode] = useState<string>();
  const [probeNode, setProbeNode] = useState<string>();
  const [form] = Form.useForm();
  const expired = useRef(onSessionExpired);
  expired.current = onSessionExpired;

  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) expired.current();
    else setError((e as Error).message);
  }

  async function load() {
    setLoading(true);
    try { setRows((await api<{ nodes: NodeRow[] }>('/nodes')).nodes); setError(''); }
    catch (e) { failed(e); }
    finally { setLoading(false); }
  }

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 10_000);
    return () => window.clearInterval(timer);
  }, []);

  async function add(values: { name: string }) {
    setSaving(true);
    try {
      const result = await api<Installation>('/nodes', 'POST', values);
      setAdding(false); form.resetFields(); setInstallation(result); await load();
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }

  async function credential(row: NodeRow, operation: 'reset' | 'revoke') {
    try {
      const result = await api<Installation>(`/nodes/${row.id}/token/${operation}`, 'POST');
      if (operation === 'reset') setInstallation(result);
      await load();
    } catch (e) { failed(e); }
  }

  async function maintenance(row: NodeRow) {
    try {
      await api(`/nodes/${row.id}/maintenance`, 'PUT', { enabled: row.status !== 'maintenance' });
      await load();
    } catch (e) { failed(e); }
  }

  return <>
    <div className="nodes-heading">
      <div><Typography.Title level={2}>节点</Typography.Title><Typography.Paragraph type="secondary">查看控制连接、最近活性与配置版本。</Typography.Paragraph></div>
      <Space><Button onClick={() => void load()}>刷新</Button><Button type="primary" onClick={() => setAdding(true)}>添加节点</Button></Space>
    </div>
    {error && <Alert title={error} type="error" showIcon className="message" />}
    <Card styles={{ body: { padding: 0 } }}>
      <Table<NodeRow> rowKey="id" dataSource={rows} loading={loading} scroll={{ x: 1300 }} locale={{ emptyText: '暂无节点，添加后复制安装命令到服务器执行。' }} columns={[
        { title: '节点', key: 'name', render: (_, row) => <Space orientation="vertical" size={0}><Typography.Text strong>{row.name}</Typography.Text><Typography.Text type="secondary">{row.software_version || '尚未接入'}</Typography.Text></Space> },
        { title: '地区', key: 'region', render: (_, row) => <Space orientation="vertical" size={2}><span>{row.region.name} <Tag>{row.region_source === 'manual' ? '人工覆盖' : '自动识别'}</Tag></span><Typography.Text type="secondary">{row.public_ipv4 || '尚未查询到 IP'}</Typography.Text></Space> },
        { title: '国内检测点角色', key: 'checker', render: (_, row) => <Tag color={row.domestic_checker ? 'green' : 'default'}>{row.checker_label}</Tag> },
        { title: '状态', key: 'status', render: (_, row) => <Space orientation="vertical" size={2}><Tag color={{ online: 'green', offline: 'default', maintenance: 'blue', pending: 'orange' }[row.status]}>{row.status_label}</Tag>{row.status === 'online' && !row.connected && <Typography.Text type="secondary">控制连接已中断</Typography.Text>}{row.status === 'maintenance' && row.maintenance_until && <Typography.Text type="secondary">至 {new Date(row.maintenance_until).toLocaleTimeString()}</Typography.Text>}</Space> },
        { title: '最近活性', dataIndex: 'last_seen_at', render: (value: string | null) => value ? new Date(value).toLocaleString() : '—' },
        { title: '配置版本', key: 'revision', render: (_, row) => <Space orientation="vertical" size={0}><span>期望 {row.desired_revision} / 已应用 {row.applied_revision}</span><Typography.Text type={row.config_status === '正常' ? 'success' : 'secondary'}>{row.config_status}</Typography.Text>{row.apply_error && <Typography.Text type="danger">{row.apply_error}</Typography.Text>}</Space> },
        { title: '操作', key: 'actions', render: (_, row) => <Space wrap>
          <Button size="small" onClick={() => setRegionNode(row)}>地区与检测点</Button>
          <Button size="small" onClick={() => setProbeNode(row.id)}>查看探针</Button>
          <Button size="small" onClick={() => setRotationNode(row.id)}>换 IP</Button>
          <Button size="small" onClick={() => void maintenance(row)}>{row.status === 'maintenance' ? '结束维护' : '维护 10 分钟'}</Button>
          <Popconfirm title="重置节点 token？" description="当前控制连接将断开，需要重新执行安装命令。" onConfirm={() => credential(row, 'reset')}><Button size="small">重置 token</Button></Popconfirm>
          <Popconfirm title="吊销节点 token？" description="当前控制连接将断开，原凭据将失效。" onConfirm={() => credential(row, 'revoke')}><Button danger size="small">吊销</Button></Popconfirm>
          <NodeValidationActions node={row} onSaved={load} onError={failed} />
        </Space> },
      ]} />
    </Card>
    <NodeRotation node={rows.find(row => row.id === rotationNode)} onClose={() => setRotationNode(undefined)} onError={failed} />
    <NodeRegion node={regionNode} onClose={() => setRegionNode(undefined)} onSaved={load} onError={failed} />
    {rows.find(row => row.id === probeNode) && <ProbeDetails node={rows.find(row => row.id === probeNode)!} onClose={() => setProbeNode(undefined)} onSessionExpired={onSessionExpired} />}
    <Modal title="添加节点" open={adding} onCancel={() => { setAdding(false); form.resetFields(); }} footer={null} destroyOnHidden>
      <Form form={form} layout="vertical" onFinish={add} clearOnDestroy>
        <Form.Item name="name" label="节点名称" rules={[{ required: true, whitespace: true, message: '请输入节点名称' }]}><Input autoFocus maxLength={200} placeholder="例如：入口 A" /></Form.Item>
        <Button block type="primary" htmlType="submit" loading={saving}>生成安装命令</Button>
      </Form>
    </Modal>
    <Modal title="安装节点" open={!!installation} onCancel={() => setInstallation(undefined)} footer={<Button type="primary" onClick={() => setInstallation(undefined)}>完成</Button>} width={760} destroyOnHidden>
      <Typography.Paragraph>在 Linux amd64 服务器执行以下命令，安装后回到列表确认在线状态。</Typography.Paragraph>
      <Alert type="info" showIcon title="安装票据只能使用一次，30 分钟后失效。" description={installation ? `有效至 ${new Date(installation.ticket_expires_at).toLocaleString()}` : ''} />
      <Typography.Paragraph className="installation-command" copyable={{ text: installation?.installation_command }}><code>{installation?.installation_command}</code></Typography.Paragraph>
    </Modal>
  </>;
}
