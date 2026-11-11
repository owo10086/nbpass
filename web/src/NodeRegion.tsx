import { useEffect, useState } from 'react';
import { Alert, Button, Form, Input, Modal, Switch, Typography } from 'antd';
import { api } from './api';
import { GlobalOutlined, RadarChartOutlined } from '@ant-design/icons';
import { FormSection } from './FormSection';
import './servers.css';

type Region = { name: string; mainland_china: boolean };
export type NodeRegionFields = {
  public_ipv4: string; region: Region; automatic_region: Region; region_source: 'auto' | 'manual';
  domestic_checker: boolean; checker_disabled: boolean; checker_label: string;
};
type RegionNode = NodeRegionFields & { id: string; name: string };
type Values = { manual: boolean; name: string; mainland_china: boolean; checker_enabled: boolean };

export function NodeRegion({ node, error, onClose, onSaved, onError }: {
  node?: RegionNode; error?: string; onClose: () => void; onSaved: () => Promise<void>; onError: (e: unknown) => void;
}) {
  const [form] = Form.useForm<Values>();
  const [saving, setSaving] = useState(false);
  const manual = Form.useWatch('manual', form);

  useEffect(() => {
    if (node) form.setFieldsValue({ manual: node.region_source === 'manual', name: node.region.name === '未知' ? '' : node.region.name,
      mainland_china: node.region.mainland_china, checker_enabled: !node.checker_disabled });
  }, [node, form]);

  async function save(values: Values) {
    if (!node) return;
    setSaving(true);
    try {
      await api(`/nodes/${node.id}/region`, 'PUT', values.manual ? { name: values.name, mainland_china: values.mainland_china } : null);
      await api(`/nodes/${node.id}/checker`, 'PUT', { enabled: values.checker_enabled });
      await onSaved();
      onClose();
    } catch (e) { onError(e); }
    finally { setSaving(false); }
  }

  return <Modal title={`地区与国内检测点${node ? `：${node.name}` : ''}`} open={!!node} onCancel={onClose} footer={null} className="servers-modal" width={640} destroyOnHidden>
    {error && <Alert type="error" showIcon title={error} className="message" />}
    <Form form={form} layout="vertical" onFinish={save} clearOnDestroy>
      <FormSection title="地区信息" icon={<GlobalOutlined />}>
      <Typography.Paragraph type="secondary">自动识别：{node?.automatic_region.name}；公网 IPv4：{node?.public_ipv4 || '尚未查询到'}</Typography.Paragraph>
      <Form.Item name="manual" label="手动覆盖地区" valuePropName="checked"><Switch /></Form.Item>
      {manual && <>
        <Form.Item name="name" label="地区名称" rules={[{ required: true, whitespace: true, message: '请输入地区名称' }, { validator: (_, value: string) => new TextEncoder().encode(value?.trim() || '').length <= 200 ? Promise.resolve() : Promise.reject(new Error('地区名称最多 200 字节')) }]}><Input maxLength={200} placeholder="例如：中国大陆·广东·深圳" /></Form.Item>
        <Form.Item name="mainland_china" label="地区位于中国大陆（不含港澳台）" valuePropName="checked"><Switch /></Form.Item>
      </>}
      </FormSection>
      <FormSection title="国内检测点" icon={<RadarChartOutlined />}>
      <Form.Item name="checker_enabled" label="允许承担国内检测点角色" valuePropName="checked"><Switch /></Form.Item>
      <Alert type="info" showIcon title="仅中国大陆服务器会自动承担国内检测点角色。" description="人工覆盖在 IP 变化后仍保留；关闭允许开关后，该服务器不参与国内检测。恢复自动识别不会重开已关闭的角色。" />
      </FormSection>
      <Button block type="primary" htmlType="submit" loading={saving} style={{ marginTop: 16 }}>保存</Button>
    </Form>
  </Modal>;
}
