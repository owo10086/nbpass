import { useEffect, useState } from 'react';

export const pageTitles = {
  overview: '概览', ports: '端口管理 | 转发规则', servers: '服务器管理 | 节点管理', endpoints: '转发端点 | 隧道接收端',
  'endpoint-groups': '端点组管理 | 出口组', chains: '转发链管理 | 多跳路径', 'server-groups': '服务器组管理 | 入口组',
  'smart-dns': '智能 DNS', latency: '延迟监控总览', 'dns-watch': '解析监控',
  incidents: '故障', events: '事件日志', telegram: 'Telegram 通知', security: '安全设置', retention: '保留期',
};
export type Page = keyof typeof pageTitles;
export type Route = { page: Page; focus?: string };

const aliases = { rules: 'ports', nodes: 'servers', 'entry-groups': 'server-groups', 'exit-groups': 'endpoint-groups' } as const;
type LegacyPage = keyof typeof aliases;

// 查询串原样保留，避免通知旧链接中的重复参数、编码或未知参数丢失。
export function resolveRoute(hash: string): Route & { hash: string } {
  const raw = hash.replace(/^#\/?/, '');
  const queryAt = raw.indexOf('?');
  const path = queryAt < 0 ? raw : raw.slice(0, queryAt);
  const query = queryAt < 0 ? '' : raw.slice(queryAt);
  const canonical = Object.hasOwn(aliases, path) ? aliases[path as LegacyPage] : path;
  const page = Object.hasOwn(pageTitles, canonical) ? canonical as Page : 'overview';
  return { page, focus: new URLSearchParams(query).get('focus') ?? undefined, hash: `#/${page}${query}` };
}

function readRoute(): Route {
  const { hash, ...route } = resolveRoute(window.location.hash);
  if (hash !== window.location.hash) window.history.replaceState(null, '', hash);
  return route;
}

// 旧业务调用仍可使用原名字，地址栏始终生成新路由。
export function go(page: Page | LegacyPage, focus?: string) {
  window.location.hash = resolveRoute(`#/${page}${focus ? `?focus=${encodeURIComponent(focus)}` : ''}`).hash.slice(1);
}

export function useRoute() {
  const [route, setRoute] = useState(readRoute);
  useEffect(() => {
    const update = () => setRoute(readRoute());
    window.addEventListener('hashchange', update);
    return () => window.removeEventListener('hashchange', update);
  }, []);
  return route;
}

// 首次拿到数据后定位一次目标行；返回值用作 Table 的 rowClassName。
export function useFocusRow(focus: string | undefined, ready: boolean) {
  const [active, setActive] = useState<string>();
  useEffect(() => {
    if (!focus || !ready) return;
    setActive(focus);
    const frame = window.requestAnimationFrame(() => document.querySelector(`[data-row-key="${CSS.escape(focus)}"]`)?.scrollIntoView({ block: 'center', behavior: 'smooth' }));
    const timer = window.setTimeout(() => setActive(undefined), 2400);
    return () => { window.cancelAnimationFrame(frame); window.clearTimeout(timer); };
  }, [focus, ready]);
  return (key: string) => key === active ? 'row-focus' : '';
}
