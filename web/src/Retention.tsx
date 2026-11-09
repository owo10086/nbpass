import { useEffect, useState } from 'react';
import { Alert, Button, Card, Form, InputNumber, Spin, Typography } from 'antd';
import { api, APIError } from './api';

type Settings = { event_days: number; probe_hours: number };

export function Retention({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [form] = Form.useForm<Settings>();
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let active = true;
    setError('');
    async function load() {
      try {
        const settings = await api<Settings>('/settings/retention');
        if (active) { form.setFieldsValue(settings); setLoaded(true); }
      } catch (e) {
        if (!active) return;
        if (e instanceof APIError && (e.status === 401 || e.status === 428)) onSessionExpired();
        else setError((e as Error).message);
      }
    }
    void load();
    return () => { active = false; };
  }, [form, reload]);

  async function save(values: Settings) {
    setSaving(true); setError(''); setNotice('');
    try {
      const settings = await api<Settings>('/settings/retention', 'PUT', values);
      form.setFieldsValue(settings);
      setNotice('保留期已保存，过期历史将在后台分批清理。');
    } catch (e) {
      if (e instanceof APIError && (e.status === 401 || e.status === 428)) onSessionExpired();
      else setError((e as Error).message);
    } finally { setSaving(false); }
  }

  return <>
    <Typography.Title level={2}>保留期设置</Typography.Title>
    <Typography.Paragraph type="secondary">按数据类型设置保留时间，控制历史数据占用的磁盘空间。</Typography.Paragraph>
    {error && <Alert title={error} type="error" showIcon className="message" action={!loaded && <Button onClick={() => setReload(v => v + 1)}>重新加载</Button>} />}
    {notice && <Alert title={notice} type="success" showIcon className="message" />}
    <Card title="历史数据">
      {!loaded ? !error && <Spin description="正在加载保留期" /> : <Form form={form} layout="vertical" onFinish={save} clearOnDestroy style={{ maxWidth: 480 }}>
        <Form.Item name="event_days" label="事件日志保留天数" extra="默认 90 天，可设置 1–3650 天。" rules={[{ required: true, type: 'integer', min: 1, max: 3650, message: '请输入 1–3650 的整数天数' }]}>
          <InputNumber min={1} max={3650} step={1} style={{ width: '100%' }} addonAfter="天" />
        </Form.Item>
        <Form.Item name="probe_hours" label="探针曲线保留小时数" extra="默认 24 小时，可设置 1–8760 小时；图表展示最近 24 小时。" rules={[{ required: true, type: 'integer', min: 1, max: 8760, message: '请输入 1–8760 的整数小时数' }]}>
          <InputNumber min={1} max={8760} step={1} style={{ width: '100%' }} addonAfter="小时" />
        </Form.Item>
        <Typography.Paragraph type="secondary">缩短保留期会删除过期历史，延长保留期无法恢复已清理的数据。清理不影响换 IP 额度、进行中的动作、配置版本和规则累计流量。</Typography.Paragraph>
        <Button type="primary" htmlType="submit" loading={saving}>保存保留期</Button>
      </Form>}
    </Card>
  </>;
}
