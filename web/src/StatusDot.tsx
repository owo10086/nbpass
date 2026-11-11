import type { ReactNode } from 'react';

export type StatusTone = 'ok' | 'warn' | 'danger' | 'info' | 'muted';

export function StatusDot({ tone = 'muted', children }: { tone?: StatusTone; children: ReactNode }) {
  return <span className="status"><span className={`dot ${tone}`} aria-hidden="true" />{children}</span>;
}
