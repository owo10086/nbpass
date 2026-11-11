import { useRef, useState, type DragEvent, type HTMLAttributes, type Key } from 'react';
import { Button, Tooltip } from 'antd';
import type { ColumnType, SorterResult, SortOrder } from 'antd/es/table/interface';
import { HolderOutlined } from '@ant-design/icons';
import { api } from './api';

// 没有实时值的行无论升序降序都排在最后；antd 降序时会把比较结果取反。
export function byMetric<T>(value: (row: T) => number | null | undefined) {
  return (a: T, b: T, order?: SortOrder) => {
    const x = value(a), y = value(b);
    if (x == null || y == null) return x == null && y == null ? 0 : (x == null) === (order === 'ascend') ? 1 : -1;
    return x - y;
  };
}

export function byText<T>(value: (row: T) => string) {
  return (a: T, b: T) => value(a).localeCompare(value(b), 'zh-CN', { numeric: true });
}

type Options = {
  // 搜索、筛选或只显示单行时，看到的不是完整列表，拖动结果无法对应保存的顺序。
  filtered?: boolean;
  onError: (e: unknown) => void;
  reload: () => Promise<void>;
};

// 保存的自定义顺序 + 临时列排序。列排序是受控的：生效时手柄置灰，可一键恢复自定义顺序。
// 拖动只在当前页内进行，提交时仍提交全集顺序；拖动和保存期间 busy() 为真，页面暂停定时刷新。
export function useDragOrder<T extends { id: string }>(rows: T[], setRows: (rows: T[]) => void, path: string, { filtered, onError, reload }: Options) {
  const [sort, setSort] = useState<{ key?: Key; order?: SortOrder }>({});
  const [dragging, setDragging] = useState<string>();
  const [over, setOver] = useState<string>();
  const [saving, setSaving] = useState(false);
  // 写入失败独立保存，成功的定时读取不能把它清除；再次调整时重新确认结果。
  const [error, setError] = useState('');
  const source = useRef<string>(undefined);
  const busy = useRef(false);
  const blocked = !!sort.order || !!filtered || saving;
  const index = (id?: string) => rows.findIndex(row => row.id === id);

  function start(e: DragEvent<HTMLElement>, id: string) {
    if (blocked || busy.current) { e.preventDefault(); return; }
    source.current = id; busy.current = true;
    setDragging(id);
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', id);
    const row = e.currentTarget.closest('tr');
    if (row) e.dataTransfer.setDragImage(row, 16, row.offsetHeight / 2);
  }

  // dragend 总在 drop 之后触发；source 仍有值说明没有放到任何行上。
  function end() {
    if (source.current) { source.current = undefined; busy.current = false; }
    setDragging(undefined); setOver(undefined);
  }

  async function drop(target: string) {
    const from = index(source.current), to = index(target);
    source.current = undefined;
    setDragging(undefined); setOver(undefined);
    if (from < 0 || to < 0 || from === to) { busy.current = false; return; }
    setSaving(true);
    setError('');
    const next = [...rows];
    next.splice(to, 0, ...next.splice(from, 1));
    setRows(next);
    try { await api(`${path}/order`, 'PUT', { ids: next.map(row => row.id) }); }
    catch (e) { setError((e as Error).message); onError(e); }
    finally {
      try { await reload(); }
      catch (e) { onError(e); }
      finally { busy.current = false; setSaving(false); }
    }
  }

  const handle: ColumnType<T> = {
    key: 'drag-handle', width: 36, align: 'center', fixed: 'left',
    render: (_, row) => blocked
      ? <Tooltip title={saving ? '正在保存顺序，请稍候' : '清除排序/筛选后可拖动'}><HolderOutlined className="drag-handle disabled" /></Tooltip>
      : <span className="drag-handle" draggable aria-label="拖动调整顺序" onDragStart={e => start(e, row.id)} onDragEnd={end}><HolderOutlined /></span>,
  };

  return {
    error,
    handle,
    busy: () => busy.current,
    // 受控列排序：把它展开到可排序列上，key 用来对应当前排序列。
    sorted: (key: string) => ({ key, sortOrder: sort.key === key ? sort.order ?? null : null }),
    onChange: (_pagination: unknown, _filters: unknown, sorter: SorterResult<T> | SorterResult<T>[]) => {
      const s = Array.isArray(sorter) ? sorter[0] : sorter;
      setSort({ key: s?.columnKey, order: s?.order ?? undefined });
    },
    restore: sort.order ? <Button onClick={() => setSort({})}>恢复自定义顺序</Button> : null,
    onRow: (row: T): HTMLAttributes<HTMLElement> => ({
      onDragOver: e => { if (!source.current) return; e.preventDefault(); e.dataTransfer.dropEffect = 'move'; if (over !== row.id) setOver(row.id); },
      onDrop: e => { if (!source.current) return; e.preventDefault(); void drop(row.id); },
    }),
    rowClass: (id: string) => {
      if (!dragging) return '';
      if (id === dragging) return 'row-dragging';
      if (id !== over) return '';
      return index(dragging) < index(id) ? 'row-drop-after' : 'row-drop-before';
    },
  };
}
