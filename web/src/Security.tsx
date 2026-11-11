import { useEffect, useState } from 'react';
import { Alert, Button, Form, Input, Skeleton } from 'antd';
import { LockOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import { PageHeader } from './PageHeader';
import { FormSection } from './FormSection';
import { api, APIError } from './api';
import './OtherPages.css';

export function Security({ onSessionExpired, onPasswordChanged }: { onSessionExpired: () => void; onPasswordChanged: () => void }) {
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [loading, setLoading] = useState(true);
  const [addresses, setAddresses] = useState('');
  const [saving, setSaving] = useState(false);
  const [changing, setChanging] = useState(false);
  const [proxies] = Form.useForm();
  const [password] = Form.useForm();

  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) onSessionExpired();
    setError((e as Error).message);
  }

  useEffect(() => {
    let active = true;
    void api<{ trusted_proxies: string[] }>('/settings/trusted-proxies')
      .then(result => { if (active) setAddresses(result.trusted_proxies.join('\n')); })
      .catch(e => { if (active) failed(e); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [proxies]);

  async function save(values: { addresses?: string }) {
    setSaving(true); setError(''); setNotice('');
    try {
      const trusted_proxies = (values.addresses ?? '').split('\n').map(value => value.trim()).filter(Boolean);
      const result = await api<{ trusted_proxies: string[] }>('/settings/trusted-proxies', 'PUT', { trusted_proxies });
      proxies.setFieldsValue({ addresses: result.trusted_proxies.join('\n') });
      setNotice('受信任代理已保存，新的登录请求立即生效。');
    } catch (e) { failed(e); }
    finally { setSaving(false); }
  }

  async function change(values: { current_password: string; new_password: string }) {
    setChanging(true); setError(''); setNotice('');
    try {
      await api('/auth/password', 'POST', { current_password: values.current_password, new_password: values.new_password });
      password.resetFields();
      onPasswordChanged();
    } catch (e) { failed(e); }
    finally { setChanging(false); }
  }

  return <div className="zf-other-page">
    <PageHeader title="安全设置" description="配置可信代理来源与管理员密码，管理面板访问安全。" />
    {error && <Alert title={error} type="error" showIcon className="message" />}
    {notice && <Alert title={notice} type="success" showIcon className="message" />}
    <div className="zf-other-card"><FormSection title="受信任的反向代理" icon={<SafetyCertificateOutlined />} description="登录来源使用 X-Forwarded-For。只有直连代理在此列表内才采信该头，并从右向左跳过可信代理。代理应覆盖或追加真实来源，不能原样透传客户端提供的头。连续输错密码 5 次，锁定该来源 15 分钟。">
      {loading ? <Skeleton active paragraph={{ rows: 3 }} title={false} /> : <Form form={proxies} layout="vertical" initialValues={{ addresses }} onFinish={save}>
        <Form.Item name="addresses" label="代理地址" extra="每行一个代理 IP 或 CIDR，最多 64 项。留空时按直连来源限流。仅填写你管理的代理地址。"><Input.TextArea rows={5} className="mono" placeholder={'127.0.0.1\n10.0.0.0/24\n2001:db8::/64'} autoComplete="off" /></Form.Item>
        <Button type="primary" htmlType="submit" loading={saving}>保存受信任代理</Button>
      </Form>}
    </FormSection></div>
    <div className="zf-other-card"><FormSection title="修改管理员密码" icon={<LockOutlined />} description="修改成功后，当前浏览器和其他设备的全部会话立即失效，请重新登录。">
      <Form form={password} layout="vertical" onFinish={change} clearOnDestroy>
        <Form.Item name="current_password" label="当前密码" rules={[{ required: true, message: '请输入当前密码' }]}><Input.Password autoComplete="current-password" /></Form.Item>
        <Form.Item name="new_password" label="新密码" extra="密码须为 12–72 字节，中文等字符会占用多个字节。" rules={[{ required: true, message: '请输入新密码' }, { validator: async (_, value: string) => { const length = new TextEncoder().encode(value ?? '').length; if (length < 12 || length > 72) throw new Error('密码须为 12–72 字节'); } }]}><Input.Password autoComplete="new-password" /></Form.Item>
        <Form.Item name="confirmation" label="确认新密码" dependencies={['new_password']} rules={[{ required: true, message: '请再次输入新密码' }, { validator: async (_, value: string) => { if (value && value !== password.getFieldValue('new_password')) throw new Error('两次密码不一致'); } }]}><Input.Password autoComplete="new-password" /></Form.Item>
        <Button type="primary" htmlType="submit" loading={changing}>修改密码并退出所有会话</Button>
      </Form>
    </FormSection></div>
  </div>;
}
