import { useState } from 'react';
import { Alert, Button, Input, Modal, Select, Space, Table, Typography } from 'antd';
import { api, apiRawJSON, APIError } from './api';

type ImportRow = { index: number; id: string; name: string; action: 'create' | 'overwrite'; errors: string[]; conflict_count?: number };
type ImportResult = { valid: boolean; results: ImportRow[] };

export function RulesTransfer({ selectedIDs, onChanged, onSessionExpired }: { selectedIDs: string[]; onChanged: () => Promise<void>; onSessionExpired: () => void }) {
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState<'create' | 'overwrite'>('create');
  const [source, setSource] = useState('');
  const [result, setResult] = useState<ImportResult>();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [imported, setImported] = useState(false);
  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) onSessionExpired();
    else setError((e as Error).message);
  }
  function resetPreview() { setResult(undefined); setImported(false); setError(''); }
  async function download() {
    setBusy(true); setError('');
    try {
      const query = new URLSearchParams(); selectedIDs.forEach(id => query.append('id', id));
      const document = await api(`/rules/export${query.size ? `?${query}` : ''}`);
      const url = URL.createObjectURL(new Blob([JSON.stringify(document, null, 2) + '\n'], { type: 'application/json' }));
      const anchor = window.document.createElement('a'); anchor.href = url; anchor.download = 'nbpass-rules.json'; anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) { failed(e); }
    finally { setBusy(false); }
  }
  async function submit(preflight: boolean) {
    setBusy(true); setError(''); setResult(undefined);
    try {
      try { JSON.parse(source); } catch { throw new Error('JSON 无法解析，请检查格式。'); }
      const response = await apiRawJSON<ImportResult>(`/rules/import${preflight ? '/preflight' : ''}`, 'POST', `{"mode":${JSON.stringify(mode)},"document":${source}}`);
      setResult(response);
      if (!preflight) { setImported(true); await onChanged(); }
    } catch (e) {
      if (e instanceof APIError && Array.isArray(e.details.results)) setResult(e.details as ImportResult);
      failed(e);
    } finally { setBusy(false); }
  }
  return <>
    <Button loading={busy && !open} disabled={busy} onClick={() => void download()}>{selectedIDs.length ? `导出所选 ${selectedIDs.length} 条` : '导出全部 JSON'}</Button>
    <Button onClick={() => { resetPreview(); setSource(''); setOpen(true); }}>导入 JSON</Button>
    {!open && error && <Typography.Text type="danger">{error}</Typography.Text>}
    <Modal title="批量导入转发规则" open={open} width={850} onCancel={() => { if (!busy) setOpen(false); }} footer={<Space wrap>
      <Button disabled={busy} onClick={() => setOpen(false)}>关闭</Button>
      <Button loading={busy} disabled={!source || imported} onClick={() => void submit(true)}>整体预检</Button>
      <Button type="primary" loading={busy} disabled={!result?.valid || imported} onClick={() => void submit(false)}>确认{mode === 'create' ? '新增' : '覆盖'}全部规则</Button>
    </Space>}>
      <Alert type="info" showIcon title="导出仅包含规则定义，不包含凭据或运行状态。" description="支持格式 1、2。入口节点或入口组、出口组须已存在，跨面板迁移时请先修改 JSON 中的节点或组 ID。预检全部通过后才能确认导入；保存成功后仍须核对各成员的同步状态。" style={{ marginBottom: 16 }} />
      <Space orientation="vertical" style={{ width: '100%' }}>
        <Typography.Text>导入方式</Typography.Text>
        <Select value={mode} disabled={busy} style={{ width: '100%' }} onChange={value => { setMode(value); resetPreview(); }} options={[{ value: 'create', label: '新增：分配新规则 ID，保留现有规则' }, { value: 'overwrite', label: '覆盖：按规则 ID 更新已有规则，不自动新增' }]} />
        {mode === 'overwrite' && <Alert type="warning" title="覆盖会修改已有规则；目标或监听配置变化会断开相应连接。" />}
        <Input type="file" accept=".json,application/json" disabled={busy} aria-label="选择规则 JSON 文件" onChange={async e => {
          const file = e.target.files?.[0]; e.target.value = ''; if (!file) return;
          resetPreview();
          if (file.size > 1024 * 1024) { setSource(''); setError('文件须小于或等于 1 MiB。'); return; }
          setBusy(true);
          try { setSource(await file.text()); } catch { setError('无法读取文件。'); } finally { setBusy(false); }
        }} />
        <Input.TextArea value={source} disabled={busy} rows={10} aria-label="规则 JSON 内容" placeholder='粘贴 {"format_version":2,"rules":[…]}，每批 1–500 条规则' onChange={e => { setSource(e.target.value); resetPreview(); }} />
        {error && <Alert type="error" showIcon title={error} />}
        {result && <>
          <Alert showIcon type={imported ? 'success' : result.valid ? 'info' : 'error'} title={imported ? '整批已保存，请核对规则同步状态。' : result.valid ? '全部预检通过，尚未写入。请核对下表后确认导入。' : '预检失败，未写入任何规则。'} />
          <Table<ImportRow> size="small" rowKey="index" dataSource={result.results} pagination={{ pageSize: 10 }} scroll={{ x: 650 }} columns={[
            { title: '序号', dataIndex: 'index', width: 60 },
            { title: '规则', dataIndex: 'name' },
            { title: '操作', render: (_, row) => row.action === 'create' ? '新增（新 ID）' : <span>覆盖 <Typography.Text code>{row.id}</Typography.Text></span> },
            { title: '预检结果', render: (_, row) => row.errors.length ? <>{row.errors.map((reason, i) => <div key={i}><Typography.Text type="danger">{reason}</Typography.Text></div>)}{(row.conflict_count ?? 0) > 1 && <Typography.Text type="secondary">共 {row.conflict_count} 处相关监听，显示首处原因；修正后请重新预检。</Typography.Text>}</> : '通过' },
          ]} />
        </>}
      </Space>
    </Modal>
  </>;
}
