import React, { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Alert, App as AntApp, Button, ConfigProvider, Dropdown, Form, Input, Spin } from 'antd';
import {
  CloudServerOutlined, DashboardOutlined, GlobalOutlined, HistoryOutlined, LoginOutlined, LogoutOutlined,
  SafetyOutlined, SendOutlined, SwapOutlined, UnorderedListOutlined, UserOutlined,
} from '@ant-design/icons';
import zhCN from 'antd/locale/zh_CN';
import 'antd/dist/reset.css';
import './style.css';
import { api, APIError } from './api';
import { EntryGroups } from './EntryGroups';
import { ExitGroups } from './ExitGroups';
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
import { useRoute, type Page } from './route';

type AuthStatus = { setup_required: boolean; authenticated: boolean };
type Counts = { nodes: number; entry: number; exit: number; rules: number; alerts: number };

const navigation: { title?: string; items: { page: Page; label: string; icon: React.ReactNode; count?: keyof Counts }[] }[] = [
  { items: [{ page: 'overview', label: '概览', icon: <DashboardOutlined />, count: 'alerts' }] },
  { title: '转发', items: [
    { page: 'nodes', label: '节点', icon: <CloudServerOutlined />, count: 'nodes' },
    { page: 'entry-groups', label: '入口组', icon: <LoginOutlined />, count: 'entry' },
    { page: 'exit-groups', label: '出口组', icon: <LogoutOutlined />, count: 'exit' },
    { page: 'rules', label: '转发规则', icon: <SwapOutlined />, count: 'rules' },
  ] },
  { title: '监控', items: [
    { page: 'dns-watch', label: '解析监控', icon: <GlobalOutlined /> },
    { page: 'incidents', label: '故障', icon: <HistoryOutlined /> },
    { page: 'events', label: '事件日志', icon: <UnorderedListOutlined /> },
  ] },
  { title: '设置', items: [
    { page: 'telegram', label: 'Telegram 通知', icon: <SendOutlined /> },
    { page: 'security', label: '安全设置', icon: <SafetyOutlined /> },
    { page: 'retention', label: '保留期设置', icon: <HistoryOutlined /> },
  ] },
];

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
      <div className="shell">
        <aside className="sidebar">
          <div className="brand"><span className="brand-mark">n</span>nbpass</div>
          <nav className="nav" aria-label="主导航">
            {navigation.map((group, i) => <div className="nav-group" key={group.title ?? i}>
              {group.title && <div className="nav-group-title">{group.title}</div>}
              {group.items.map(item => {
                const value = item.count && counts?.[item.count];
                const alert = item.count === 'alerts';
                return <a key={item.page} href={`#/${item.page}`} className="nav-item" aria-current={page === item.page ? 'page' : undefined}>
                  {item.icon}{item.label}
                  {alert ? !!value && <span className="nav-count alert" aria-label={`${value} 项需要处理`}>{value}</span> : value !== undefined && <span className="nav-count">{value}</span>}
                </a>;
              })}
            </div>)}
          </nav>
          <div className="account">
            <Dropdown trigger={['click']} placement="topLeft" menu={{ items: [
              { key: 'note', type: 'group', label: '会话空闲 24 小时或登录满 7 天后需重新登录' },
              { key: 'logout', label: '退出登录', onClick: () => void endSession() },
              { key: 'revoke', label: '退出所有会话', danger: true, onClick: () => void endSession(true) },
            ] }}>
              <button type="button" className="nav-item" disabled={busy}><UserOutlined />管理员</button>
            </Dropdown>
          </div>
        </aside>
        <main className="main">
          <div className="page" key={page}>
            {error && <Alert title={error} type="error" showIcon className="message" />}
            {page === 'overview' ? <Overview state={operations} />
              : page === 'nodes' ? <Nodes focus={route.focus} onSessionExpired={expired} />
              : page === 'entry-groups' ? <EntryGroups focus={route.focus} onSessionExpired={expired} />
              : page === 'exit-groups' ? <ExitGroups onSessionExpired={expired} />
              : page === 'rules' ? <Rules onSessionExpired={expired} />
              : page === 'dns-watch' ? <DNSWatch onSessionExpired={expired} />
              : page === 'incidents' ? <Incidents focus={route.focus} operations={operations.data} onSessionExpired={expired} />
              : page === 'events' ? <Events onSessionExpired={expired} />
              : page === 'telegram' ? <Telegram onSessionExpired={expired} />
              : page === 'retention' ? <Retention onSessionExpired={expired} />
              : <Security onSessionExpired={expired} onPasswordChanged={() => { setNotice('密码已修改，全部会话已退出，请使用新密码登录。'); void refresh(); }} />}
          </div>
        </main>
      </div>
    );
  }

  const setup = status.setup_required;
  return (
    <main className="auth-page">
      <div className="auth-box">
        <div className="auth-brand"><span className="brand-mark">n</span>nbpass</div>
        <section className="auth-card">
          <h1>{setup ? '设置管理员密码' : '登录面板'}</h1>
          <p className="lead">{setup ? '首次使用，请设置一个 12–72 字节的密码。' : '输入管理员密码。'}</p>
          {error && <Alert title={error} type="error" showIcon className="message" />}
          {notice && <Alert title={notice} type="success" showIcon className="message" />}
          <Form form={form} layout="vertical" onFinish={submit} requiredMark={false} clearOnDestroy>
            <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入管理员密码' }, ...(setup ? [{ validator: async (_: unknown, value: string) => { const size = new TextEncoder().encode(value ?? '').length; if (size < 12 || size > 72) throw new Error('密码须为 12–72 字节'); } }] : [])]}>
              <Input.Password autoComplete={setup ? 'new-password' : 'current-password'} autoFocus />
            </Form.Item>
            {setup && <Form.Item name="confirmation" label="确认密码" dependencies={['password']} rules={[{ required: true, message: '请再次输入密码' }, { validator: async (_, value) => { if (value && value !== form.getFieldValue('password')) throw new Error('两次密码不一致'); } }]}><Input.Password autoComplete="new-password" /></Form.Item>}
            <Button type="primary" htmlType="submit" block loading={busy}>{setup ? '设置密码' : '登录'}</Button>
          </Form>
        </section>
        <p className="auth-foot">{setup ? '设置完成后使用新密码登录。' : '使用完毕后请退出登录。'}</p>
      </div>
    </main>
  );
}

const theme = {
  token: {
    colorPrimary: '#2563eb', colorLink: '#2563eb', colorText: '#18181b', colorTextSecondary: '#5f5f67', colorTextTertiary: '#75757d',
    colorBorder: '#e8e8eb', colorBorderSecondary: '#e8e8eb', colorSuccess: '#16a34a', colorWarning: '#d97706', colorError: '#dc2626',
    borderRadius: 6, borderRadiusLG: 8, fontSize: 14, controlHeight: 32,
    fontFamily: "-apple-system, BlinkMacSystemFont, 'SF Pro Text', 'PingFang SC', 'Segoe UI', 'Microsoft YaHei', sans-serif",
    fontFamilyCode: "'SF Mono', 'JetBrains Mono', ui-monospace, Menlo, Consolas, monospace",
    boxShadowSecondary: '0 1px 2px rgb(0 0 0 / 4%), 0 8px 24px -8px rgb(0 0 0 / 14%)',
    motionDurationMid: '0.15s', motionDurationSlow: '0.2s',
  },
  components: {
    Button: { primaryShadow: 'none', defaultShadow: 'none', dangerShadow: 'none', fontWeight: 500 },
    Table: { headerBg: '#fafafa', headerColor: '#5f5f67', headerSplitColor: 'transparent', cellPaddingBlock: 10, cellPaddingInline: 12, rowHoverBg: '#fafafa', headerBorderRadius: 0 },
    Card: { headerFontSize: 14 },
    Modal: { titleFontSize: 16 },
    Tag: { defaultBg: '#f4f4f5' },
  },
};

createRoot(document.getElementById('root')!).render(
  <React.StrictMode><ConfigProvider locale={zhCN} theme={theme} button={{ autoInsertSpace: false }}><AntApp><App /></AntApp></ConfigProvider></React.StrictMode>,
);
