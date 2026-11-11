import type { ReactNode } from 'react';

export type TagTone = 'neutral' | 'green' | 'red' | 'orange' | 'gold' | 'blue' | 'purple';

// 分类（如转发模式）用色块，状态用 StatusDot。
export function ColorTag({ tone = 'neutral', children }: { tone?: TagTone; children: ReactNode }) {
  return <span className={`color-tag ${tone}`}>{children}</span>;
}
