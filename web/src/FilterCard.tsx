import type { ReactNode } from 'react';
import { Button } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';

export function FilterCard({ children, onRefresh, loading = false }: { children: ReactNode; onRefresh?: () => void; loading?: boolean }) {
  return <section className="filter-card" aria-label="筛选条件">
    {children}
    {onRefresh && <Button icon={<ReloadOutlined />} loading={loading} onClick={onRefresh}>刷新</Button>}
  </section>;
}

// htmlFor 关联 Input/Select 等控件的 id，避免只有视觉标签。
export function FilterField({ label, htmlFor, children }: { label: string; htmlFor: string; children: ReactNode }) {
  return <div className="filter-field"><label htmlFor={htmlFor}>{label}</label>{children}</div>;
}
