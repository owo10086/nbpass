import React, { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Alert, Button, Card, ConfigProvider, Empty, Form, Input, Layout, Menu, Space, Spin, Typography } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import 'antd/dist/reset.css';
import './style.css';
import { api } from './api';
import { EntryGroups } from './EntryGroups';
import { Rules } from './Rules';
import { Nodes } from './Nodes';
import { Events } from './Events';
import { Security } from './Security';
import { Telegram } from './Telegram';

type AuthStatus = { setup_required: boolean; authenticated: boolean };

function App() {
  const [status, setStatus] = useState<AuthStatus>();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [form] = Form.useForm();
  const [page, setPage] = useState('nodes');

  async function refresh() {
    setError('');
    try { setStatus(await api<AuthStatus>('/auth/status')); }
    catch (e) { setError((e as Error).message); }
  }

  useEffect(() => { void refresh(); }, []);
  useEffect(() => { form.resetFields(); }, [status?.setup_required, form]);

  async function submit(values: { password: string }) {
    setBusy(true);
    setError('');
    setNotice('');
    try {
      if (status?.setup_required) {
        await api('/auth/setup', 'POST', { password: values.password });
        form.resetFields();
        setNotice('管理员密码已设置，请使用新密码登录。');
      } else {
        await api('/auth/login', 'POST', { password: values.password });
        form.resetFields();
      }
      await refresh();
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }

  async function endSession(all = false) {
    setBusy(true);
    setError('');
    try {
      await api(all ? '/auth/sessions/revoke' : '/auth/logout', 'POST');
      setNotice(all ? '所有会话已退出，请重新登录。' : '已安全退出。');
      await refresh();
    } catch (e) { setError((e as Error).message); await refresh(); }
    finally { setBusy(false); }
  }

  if (!status) return (
    <main className="loading-screen">
      {error ? <Space direction="vertical"><Alert title={error} type="error" /><Button onClick={() => void refresh()}>重新连接</Button></Space> : <Spin description="正在连接面板" />}
    </main>
  );

  if (status.authenticated) return (
    <Layout className="panel-layout">
      <Layout.Sider breakpoint="lg" collapsedWidth="0" className="sidebar">
        <div className="brand"><span className="brand-mark">n</span><strong>nbpass</strong></div>
        <Menu theme="dark" selectedKeys={[page]} onClick={({ key }) => setPage(key)} items={[{ key: 'overview', label: '面板概览' }, { key: 'nodes', label: '节点' }, { key: 'entry-groups', label: '入口组' }, { key: 'rules', label: '转发规则' }, { key: 'events', label: '事件日志' }, { key: 'telegram', label: 'Telegram 通知' }, { key: 'security', label: '安全设置' }]} />
        <div className="sidebar-caption">流量转发，由你掌控</div>
      </Layout.Sider>
      <Layout>
        <Layout.Header className="panel-header"><span>{{ nodes: '节点', 'entry-groups': '入口组', rules: '转发规则', security: '安全设置', events: '事件日志', telegram: 'Telegram 通知', overview: '面板概览' }[page]}</span><Space><span className="admin-label">管理员</span><Button loading={busy} onClick={() => void endSession()}>退出登录</Button></Space></Layout.Header>
        <Layout.Content className="panel-content">
          {page === 'nodes' ? <Nodes onSessionExpired={() => void refresh()} /> : page === 'entry-groups' ? <EntryGroups onSessionExpired={() => void refresh()} /> : page === 'rules' ? <Rules onSessionExpired={() => void refresh()} /> : page === 'events' ? <Events onSessionExpired={() => void refresh()} /> : page === 'telegram' ? <Telegram onSessionExpired={() => void refresh()} /> : page === 'security' ? <Security onSessionExpired={() => void refresh()} onPasswordChanged={() => { setNotice('密码已修改，全部会话已退出，请使用新密码登录。'); void refresh(); }} /> : <><Typography.Title level={2}>欢迎使用 nbpass</Typography.Title>
          <Typography.Paragraph type="secondary">在这里管理你的节点与转发规则。</Typography.Paragraph>
          {error && <Alert title={error} type="error" showIcon className="message" />}
          <Card className="empty-panel"><Empty description="面板已就绪，暂无配置" image={Empty.PRESENTED_IMAGE_SIMPLE} /></Card></>}
          <div className="session-note"><Typography.Text type="secondary">会话空闲 24 小时或登录满 7 天后，需要重新登录。</Typography.Text><Button type="link" loading={busy} onClick={() => void endSession(true)}>退出所有会话</Button></div>
        </Layout.Content>
      </Layout>
    </Layout>
  );

  const setup = status.setup_required;
  return (
    <main className="auth-page">
      <section className="auth-intro">
        <div className="intro-brand"><span className="brand-mark">n</span>nbpass</div>
        <div><div className="eyebrow">你的网络 · 你的掌控</div><h1>让每一次连接<br />都有条不紊。</h1><p>统一管理节点与转发规则，<br />从一个可靠的面板开始。</p></div>
        <div className="intro-footer">流量转发面板</div>
      </section>
      <section className="auth-form-region">
        <Card className="auth-card" variant="borderless">
          <Typography.Text className="step-label">{setup ? '首次使用' : '管理员访问'}</Typography.Text>
          <Typography.Title level={2}>{setup ? '设置管理员密码' : '登录面板'}</Typography.Title>
          <Typography.Paragraph type="secondary">{setup ? '为面板设置一个只有你知道的密码。' : '输入管理员密码，继续管理你的网络。'}</Typography.Paragraph>
          {error && <Alert title={error} type="error" showIcon className="message" />}
          {notice && <Alert title={notice} type="success" showIcon className="message" />}
          <Form form={form} layout="vertical" onFinish={submit} requiredMark={false} clearOnDestroy>
            <Form.Item name="password" label="管理员密码" rules={[{ required: true, message: '请输入管理员密码' }, ...(setup ? [{ validator: async (_: unknown, value: string) => { const size = new TextEncoder().encode(value ?? '').length; if (size < 12 || size > 72) throw new Error('密码须为 12–72 字节'); } }] : [])]}>
              <Input.Password size="large" placeholder={setup ? '设置一个足够长的密码' : '输入管理员密码'} autoComplete={setup ? 'new-password' : 'current-password'} autoFocus />
            </Form.Item>
            {setup && <Form.Item name="confirmation" label="确认密码" dependencies={['password']} rules={[{ required: true, message: '请再次输入密码' }, { validator: async (_, value) => { if (value && value !== form.getFieldValue('password')) throw new Error('两次密码不一致'); } }]}><Input.Password size="large" placeholder="再次输入密码" autoComplete="new-password" /></Form.Item>}
            <Button type="primary" htmlType="submit" size="large" block loading={busy}>{setup ? '设置密码' : '登录'}</Button>
          </Form>
          <Typography.Paragraph type="secondary" className="auth-hint">{setup ? '请妥善保管密码。设置完成后即可登录。' : '请在自己的设备上登录，使用完毕后安全退出。'}</Typography.Paragraph>
        </Card>
      </section>
    </main>
  );
}

createRoot(document.getElementById('root')!).render(
  <React.StrictMode><ConfigProvider locale={zhCN} theme={{ token: { colorPrimary: '#185adb', borderRadius: 10, fontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", sans-serif' } }}><App /></ConfigProvider></React.StrictMode>,
);
