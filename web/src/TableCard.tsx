import type { ReactNode } from 'react';
import { Table, type TableProps } from 'antd';

export type TableCardProps<T extends object> = Omit<TableProps<T>, 'title'> & { title?: string; actions?: ReactNode };

// 透传 AntD Table 的选择、排序、横向滚动、分页等能力，不持有业务状态。
export function TableCard<T extends object>({ title, actions, pagination, ...props }: TableCardProps<T>) {
  return <section className="table-card" aria-label={title}>
    {(title || actions) && <div className="table-card-head">{title && <h2>{title}</h2>}{actions && <div className="table-card-actions">{actions}</div>}</div>}
    <Table<T> {...props} pagination={pagination === false ? false : {
      showTotal: total => `共 ${total} 条`, placement: ['bottomEnd'], ...pagination,
    }} />
  </section>;
}
