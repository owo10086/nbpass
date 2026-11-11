import React, { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Alert, App as AntApp, Button, ConfigProvider, Form, Input, Spin } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import 'antd/dist/reset.css';
import './style.css';
import { api } from './api';
import { EntryGroups } from './EntryGroups';
import { ExitGroups } from './ExitGroups';
import { Endpoints } from './Endpoints';
import { Rules } from './Rules';
import { Nodes } from './Nodes';
import { Incidents } from './Incidents';
import { Events } from './Events';
import { Security } from './Security';
import { Telegram } from './Telegram';
import { DNSWatch } from './DNSWatch';
import { Retention } from './Retention';
import { Overview } from './Overview';
import { useOperations } from './Operations';
import { useRoute } from './route';
import { AppShell } from './AppShell';
import { PlannedPage } from './PlannedPage';
import { applyTheme, themeConfig } from './theme';
import { LockOutlined } from '@ant-design/icons';
import { PageHeader } from './PageHeader';
import { FormSection } from './FormSection';
import './AuthPages.css';

type AuthStatus = { setup_required: boolean; authenticated: boolean };

function App() {
  const [status, setStatus] = useState<AuthStatus>();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [form] = Form.useForm();
  const route = useRoute();

  async function refresh() {
    setError('');
    try { setStatus(await api<AuthStatus>('/auth/status')); }
    catch (e) { setError((e as Error).message); }
  }

  useEffect(() => { void refresh(); }, []);
  useEffect(() => { form.resetFields(); }, [status?.setup_required, form]);
  const operations = useOperations(!!status?.authenticated, () => void refresh());
  const counts = operations.data?.counts;

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
      {error ? <div><Alert title={error} type="error" showIcon className="message" /><Button onClick={() => void refresh()}>重新连接</Button></div> : <Spin description="正在连接面板" />}
    </main>
  );

  if (status.authenticated) {
    const expired = () => void refresh();
    const page = route.page;
    return (
      <AppShell page={page} counts={counts} busy={busy} onLogout={all => void endSession(all)}>
        {error && <Alert title={error} type="error" showIcon className="message" />}
        {page === 'overview' ? <Overview state={operations} />
          : page === 'servers' ? <Nodes focus={route.focus} onSessionExpired={expired} />
          : page === 'server-groups' ? <EntryGroups focus={route.focus} onSessionExpired={expired} />
          : page === 'endpoints' ? <Endpoints focus={route.focus} onSessionExpired={expired} />
          : page === 'endpoint-groups' ? <ExitGroups focus={route.focus} onSessionExpired={expired} />
          : page === 'ports' ? <Rules focus={route.focus} onSessionExpired={expired} />
          : page === 'chains' ? <PlannedPage title="转发链管理" description="未来将支持编排多个转发端点组成转发链。" />
          : page === 'smart-dns' ? <PlannedPage title="智能 DNS" description="未来将支持按线路与可用性管理 DNS 解析策略。" />
          : page === 'latency' ? <PlannedPage title="延迟监控总览" description="未来将汇总线路延迟与连通性监控结果。" />
          : page === 'dns-watch' ? <DNSWatch focus={route.focus} onSessionExpired={expired} />
          : page === 'incidents' ? <Incidents focus={route.focus} operations={operations.data} onSessionExpired={expired} />
          : page === 'events' ? <Events onSessionExpired={expired} />
          : page === 'telegram' ? <Telegram onSessionExpired={expired} />
          : page === 'retention' ? <Retention onSessionExpired={expired} />
          : <Security onSessionExpired={expired} onPasswordChanged={() => { setNotice('密码已修改，全部会话已退出，请使用新密码登录。'); void refresh(); }} />}
      </AppShell>
    );
  }

  const setup = status.setup_required;
  return (
    <main className="auth-page zf-auth-page">
      <div className="auth-box">
        <div className="auth-brand"><span className="brand-mark">n</span>nbpass</div>
        <section className="auth-card">
          <PageHeader title={setup ? '首次设置' : '登录面板'} description={setup ? '创建管理员密码，开始管理服务器与端口。' : '输入管理员密码，管理服务器与端口。'} />
          {error && <Alert title={error} type="error" showIcon className="message" />}
          {notice && <Alert title={notice} type="success" showIcon className="message" />}
          <Form form={form} layout="vertical" onFinish={submit} requiredMark={false} clearOnDestroy>
            <FormSection title={setup ? '设置管理员密码' : '管理员验证'} icon={<LockOutlined />}>
            <Form.Item name="password" label="密码" extra={setup ? '密码须为 12–72 字节，中文等字符会占用多个字节。' : undefined} rules={[{ required: true, message: '请输入管理员密码' }, ...(setup ? [{ validator: async (_: unknown, value: string) => { const size = new TextEncoder().encode(value ?? '').length; if (size < 12 || size > 72) throw new Error('密码须为 12–72 字节'); } }] : [])]}>
              <Input.Password autoComplete={setup ? 'new-password' : 'current-password'} autoFocus />
            </Form.Item>
            {setup && <Form.Item name="confirmation" label="确认密码" dependencies={['password']} rules={[{ required: true, message: '请再次输入密码' }, { validator: async (_, value) => { if (value && value !== form.getFieldValue('password')) throw new Error('两次密码不一致'); } }]}><Input.Password autoComplete="new-password" /></Form.Item>}
            <Button type="primary" htmlType="submit" block loading={busy}>{setup ? '设置密码' : '登录'}</Button>
            </FormSection>
          </Form>
        </section>
        <p className="auth-foot">{setup ? '设置完成后使用新密码登录。' : '使用完毕后请退出登录。'}</p>
      </div>
    </main>
  );
}

applyTheme();

createRoot(document.getElementById('root')!).render(
  <React.StrictMode><ConfigProvider locale={zhCN} theme={themeConfig} button={{ autoInsertSpace: false }}><AntApp><App /></AntApp></ConfigProvider></React.StrictMode>,
);
