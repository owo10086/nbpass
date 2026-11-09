import { useEffect, useState } from 'react';

export type Page = 'overview' | 'nodes' | 'entry-groups' | 'exit-groups' | 'rules' | 'dns-watch' | 'events' | 'telegram' | 'security' | 'retention';
export type Route = { page: Page; focus?: string };

const pages: Page[] = ['overview', 'nodes', 'entry-groups', 'exit-groups', 'rules', 'dns-watch', 'events', 'telegram', 'security', 'retention'];

// 地址形如 #/nodes?focus=<节点 ID>；focus 让目标页把对应行滚动到可见处并短暂高亮。
function parse(): Route {
  const [path, query = ''] = window.location.hash.replace(/^#\/?/, '').split('?');
  const page = pages.includes(path as Page) ? path as Page : 'overview';
  return { page, focus: new URLSearchParams(query).get('focus') ?? undefined };
}

export function go(page: Page, focus?: string) {
  window.location.hash = `/${page}${focus ? `?focus=${encodeURIComponent(focus)}` : ''}`;
}

export function useRoute() {
  const [route, setRoute] = useState(parse);
  useEffect(() => {
    const update = () => setRoute(parse());
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
