import { useEffect, useState, type ReactNode } from 'react';
import { Breadcrumb, Button, Drawer, Dropdown } from 'antd';
import { DashboardOutlined, DownOutlined, MenuOutlined, SettingOutlined, SwapOutlined, AlertOutlined, UserOutlined } from '@ant-design/icons';
import { pageTitles, type Page } from './route';

type Counts = { nodes: number; entry: number; exit: number; rules: number; alerts: number };
const groups: { title: string; icon: ReactNode; pages: Page[] }[] = [
  { title: '端口管理', icon: <SwapOutlined />, pages: ['ports', 'servers', 'endpoints', 'endpoint-groups', 'chains', 'server-groups', 'smart-dns', 'latency'] },
  { title: '监控告警', icon: <AlertOutlined />, pages: ['dns-watch', 'incidents', 'events'] },
  { title: '系统设置', icon: <SettingOutlined />, pages: ['telegram', 'security', 'retention'] },
];
const countKeys: Partial<Record<Page, keyof Counts>> = { overview: 'alerts', ports: 'rules', servers: 'nodes', 'endpoint-groups': 'exit', 'server-groups': 'entry' };

export function AppShell({ page, counts, busy, onLogout, children }: {
  page: Page; counts?: Counts; busy: boolean; onLogout: (all?: boolean) => void; children: ReactNode;
}) {
  const [closed, setClosed] = useState<string[]>([]);
  const [mobile, setMobile] = useState(() => window.matchMedia('(max-width: 860px)').matches);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const group = groups.find(item => item.pages.includes(page));

  useEffect(() => {
    const query = window.matchMedia('(max-width: 860px)');
    const update = () => { setMobile(query.matches); setDrawerOpen(false); };
    query.addEventListener('change', update);
    return () => query.removeEventListener('change', update);
  }, []);
  useEffect(() => {
    // 同页查询参数、旧链接和地址栏修改也应收起导航，不能只依赖 page 变化。
    const close = () => setDrawerOpen(false);
    window.addEventListener('hashchange', close);
    return () => window.removeEventListener('hashchange', close);
  }, []);
  useEffect(() => {
    setClosed(items => items.filter(title => title !== group?.title));
    setDrawerOpen(false);
    document.title = `${pageTitles[page]} · nbpass`;
    window.scrollTo(0, 0);
  }, [page, group?.title]);

  function link(target: Page) {
    const key = countKeys[target];
    const count = key && counts?.[key];
    return <a key={target} href={`#/${target}`} className={`nav-item${target === 'overview' ? ' nav-home' : ''}`} aria-current={page === target ? 'page' : undefined} onClick={() => setDrawerOpen(false)}>
      {target === 'overview' && <DashboardOutlined />}<span>{pageTitles[target]}</span>
      {count !== undefined && (target !== 'overview' || count > 0) && <span className={`nav-count${target === 'overview' ? ' alert' : ''}`} aria-label={target === 'overview' ? `${count} 项需要处理` : undefined}>{count}</span>}
    </a>;
  }
  const navigation = <>
    <div className="brand"><span className="brand-mark">n</span>nbpass</div>
    <nav className="nav" aria-label="主导航">
      {link('overview')}
      {groups.map((item, i) => {
        const open = !closed.includes(item.title);
        return <section className="nav-group" key={item.title}>
          <button type="button" className={`nav-group-title${item === group ? ' active' : ''}`} aria-expanded={open} aria-controls={`nav-group-${mobile ? 'mobile' : 'desktop'}-${i}`} onClick={() => setClosed(items => open ? [...items, item.title] : items.filter(title => title !== item.title))}>
            {item.icon}{item.title}<DownOutlined className="nav-arrow" rotate={open ? 180 : 0} />
          </button>
          <div id={`nav-group-${mobile ? 'mobile' : 'desktop'}-${i}`} hidden={!open}>{item.pages.map(link)}</div>
        </section>;
      })}
    </nav>
  </>;

  return <div className="shell">
    {!mobile && <aside className="sidebar">{navigation}</aside>}
    <Drawer title="主导航" placement="left" open={mobile && drawerOpen} onClose={() => setDrawerOpen(false)} size="min(280px, 88vw)" className="navigation-drawer" styles={{ body: { padding: 0 } }}>
      {mobile && navigation}
    </Drawer>
    <div className="main">
      <header className="topbar">
        {mobile && <Button type="text" icon={<MenuOutlined />} aria-label="打开导航" onClick={() => setDrawerOpen(true)} />}
        <Breadcrumb items={[...(group ? [{ title: group.title }] : []), { title: pageTitles[page] }]} />
        <Dropdown trigger={['click']} placement="bottomRight" menu={{ items: [
          { key: 'note', type: 'group', label: '会话空闲 24 小时或登录满 7 天后需重新登录' },
          { key: 'logout', label: '退出登录', onClick: () => onLogout() },
          { key: 'revoke', label: '退出所有会话', danger: true, onClick: () => onLogout(true) },
        ] }}>
          <button type="button" className="account-button" aria-label="账户菜单" disabled={busy}><span className="avatar"><UserOutlined /></span><span className="account-label">管理员</span><DownOutlined /></button>
        </Dropdown>
      </header>
      <main className="page" key={page}>{children}</main>
    </div>
  </div>;
}
