import { useId, useState, type ReactNode } from 'react';
import { DownOutlined } from '@ant-design/icons';

export function FormSection({ title, icon, description, collapsible = false, defaultCollapsed = false, children }: {
  title: string; icon: ReactNode; description?: ReactNode; collapsible?: boolean; defaultCollapsed?: boolean; children: ReactNode;
}) {
  const id = useId();
  const [collapsed, setCollapsed] = useState(defaultCollapsed);
  const hidden = collapsible && collapsed;
  const heading = <><span className="form-section-icon" aria-hidden="true">{icon}</span>{title}{collapsible && <DownOutlined className="form-section-arrow" rotate={hidden ? -90 : 0} />}</>;
  return <section className="form-section">
    <h2>{collapsible ? <button type="button" aria-expanded={!hidden} aria-controls={id} onClick={() => setCollapsed(!collapsed)}>{heading}</button> : <span className="form-section-heading">{heading}</span>}</h2>
    <div id={id} hidden={hidden}>{description && <p className="form-section-description">{description}</p>}{children}</div>
  </section>;
}
