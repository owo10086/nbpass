import type { ReactNode } from 'react';

export function PageHeader({ title, description, actions, children }: { title: string; description?: ReactNode; actions?: ReactNode; children?: ReactNode }) {
  return <header className="page-header">
    <div><h1>{title}</h1>{description && <p>{description}</p>}</div>
    {(actions || children) && <div className="page-actions">{actions}{children}</div>}
  </header>;
}

// 设置页按「左侧说明、右侧表单」分节，节之间以细线分隔。
export function SettingsSection({ title, description, wide, children }: { title: string; description?: ReactNode; wide?: boolean; children: ReactNode }) {
  return <section className="settings-section">
    <div><h2>{title}</h2>{description && <p>{description}</p>}</div>
    <div className={wide ? 'settings-body wide' : 'settings-body'}>{children}</div>
  </section>;
}
