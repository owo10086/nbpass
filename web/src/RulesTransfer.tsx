import { useState } from 'react';
import { Alert, Button, Checkbox, Form, Input, Modal, Segmented, Select, Space, Tabs, Typography } from 'antd';
import { DownloadOutlined, FileTextOutlined, ImportOutlined } from '@ant-design/icons';
import { api, apiRawJSON, APIError } from './api';
import { FormSection } from './FormSection';
import { TableCard } from './TableCard';
import { buildRuleImportPayload, mapRuleImportResults, parseRuleText, type RuleImportShared, type RuleTextError } from './RuleTextImport';
import { ruleModeLabels, type Group, type PathNode } from './RulesData';
import type { ForwardChain } from './ChainsData';

type ImportRow = { index: number; line?: number; id: string; name: string; action: 'create' | 'overwrite'; errors: string[]; conflict_count?: number };
type ImportResult = { valid: boolean; results: ImportRow[] };

export function RulesTransfer({ selectedIDs, nodes, groups, exitGroups, chains, onChanged, onSessionExpired }: {
  selectedIDs: string[]; nodes: PathNode[]; groups: Group[]; exitGroups: Group[]; chains: ForwardChain[];
  onChanged: () => Promise<void>; onSessionExpired: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<'json' | 'text'>('json');
  const [mode, setMode] = useState<'create' | 'overwrite'>('create');
  const [source, setSource] = useState('');
  const [textSource, setTextSource] = useState('');
  const [shared, setShared] = useState<RuleImportShared>({ line: '', mode: 'direct', allow_private: false });
  const [localErrors, setLocalErrors] = useState<RuleTextError[]>([]);
  const [result, setResult] = useState<ImportResult>();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [imported, setImported] = useState(false);
  function failed(e: unknown) {
    if (e instanceof APIError && (e.status === 401 || e.status === 428)) onSessionExpired();
    else setError((e as Error).message);
  }
  function resetPreview() { setResult(undefined); setImported(false); setError(''); setLocalErrors([]); }
  function updateShared(next: Partial<RuleImportShared>) { setShared(current => ({ ...current, ...next })); resetPreview(); }
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
    if (busy || imported || (!preflight && !result?.valid)) return;
    const parsed = tab === 'text' ? parseRuleText(textSource) : undefined;
    function showResult(response: ImportResult) {
      setResult(parsed ? { ...response, results: mapRuleImportResults(response.results, parsed.rows) } : response);
    }
    setBusy(true); setError(''); setResult(undefined); setLocalErrors([]);
    try {
      const path = `/rules/import${preflight ? '/preflight' : ''}`;
      let response: ImportResult;
      if (parsed) {
        if (parsed.errors.length) { setLocalErrors(parsed.errors); return; }
        if (!parsed.rows.length) throw new Error('请至少输入一条规则。');
        if (!shared.line) throw new Error('请选择入口节点或入口组。');
        if (shared.mode === 'single' && !shared.exit_group_id) throw new Error('请选择端点组。');
        if (shared.mode === 'chain' && !shared.chain_id) throw new Error('请选择已保存的转发链。');
        response = await api<ImportResult>(path, 'POST', { mode: 'create', document: buildRuleImportPayload(parsed.rows, shared) });
      } else {
        try { JSON.parse(source); } catch { throw new Error('JSON 无法解析，请检查格式。'); }
        response = await apiRawJSON<ImportResult>(path, 'POST', `{"mode":${JSON.stringify(mode)},"document":${source}}`);
      }
      showResult(response);
      if (!preflight) { setImported(true); await onChanged(); }
    } catch (e) {
      // 逐条结果已说明失败原因，不再重复显示整体错误。
      if (e instanceof APIError && Array.isArray(e.details.results)) showResult(e.details as ImportResult);
      else failed(e);
    } finally { setBusy(false); }
  }
  return <>
    <Button icon={<DownloadOutlined />} loading={busy && !open} disabled={busy} onClick={() => void download()}>{selectedIDs.length ? `导出所选 ${selectedIDs.length} 条` : '导出全部 JSON'}</Button>
    <Button icon={<ImportOutlined />} disabled={busy} onClick={() => { resetPreview(); setSource(''); setTextSource(''); setTab('json'); setShared({ line: '', mode: 'direct', allow_private: false }); setOpen(true); }}>批量导入</Button>
    {!open && error && <Typography.Text type="danger">{error}</Typography.Text>}
    <Modal className="ports-transfer-modal" title="批量导入端口转发" open={open} width={850} onCancel={() => { if (!busy) setOpen(false); }} closable={!busy} keyboard={!busy} footer={<Space wrap>
      <Button disabled={busy} onClick={() => setOpen(false)}>关闭</Button>
      <Button loading={busy} disabled={busy || !(tab === 'text' ? textSource.trim() : source) || imported} onClick={() => void submit(true)}>整体预检</Button>
      <Button type="primary" loading={busy} disabled={busy || !result?.valid || imported} onClick={() => void submit(false)}>确认{tab === 'text' || mode === 'create' ? '新增' : '覆盖'}全部端口</Button>
    </Space>}>
      <Alert type="info" showIcon title="整批预检通过后才能确认导入；任一条有误，整批不保存。" description={tab === 'text' ? '文本导入仅新增规则，全部启用；入口、链路形态与私网选项对整批生效。保存成功后仍须核对各成员的同步状态。' : '导出仅包含端口转发定义，不包含凭据或运行状态。当前导出格式 3，以 chain_id 引用共享转发链；仍兼容导入格式 1、2。导入不会自动创建引用的转发链、端点组、服务器或服务器组；它们须已存在，跨面板迁移时请先核对 JSON 中的所有引用 ID。保存成功后仍须核对各成员的同步状态。'} className="message" />
      <FormSection title="导入配置" icon={<FileTextOutlined />}>
        <Tabs activeKey={tab} onChange={key => { setTab(key === 'text' ? 'text' : 'json'); resetPreview(); }} items={[
          { key: 'json', label: 'JSON 导入', disabled: busy, children: <>
            <Form layout="vertical">
              <Form.Item label="导入方式"><Select value={mode} disabled={busy} onChange={value => { setMode(value); resetPreview(); }} options={[{ value: 'create', label: '新增：分配新端口 ID，保留现有端口' }, { value: 'overwrite', label: '覆盖：按端口 ID 更新已有端口，不自动新增' }]} /></Form.Item>
            </Form>
            <div className="ports-import-source">
              {mode === 'overwrite' && <Alert type="warning" title="覆盖会修改已有端口；目标或服务端口等配置变化会断开相应连接。" />}
              <Input type="file" accept=".json,application/json" disabled={busy} aria-label="选择端口 JSON 文件" onChange={async e => {
                const file = e.target.files?.[0]; e.target.value = ''; if (!file) return;
                resetPreview();
                if (file.size > 1024 * 1024) { setSource(''); setError('文件须小于或等于 1 MiB。'); return; }
                setBusy(true);
                try { setSource(await file.text()); } catch { setError('无法读取文件。'); } finally { setBusy(false); }
              }} />
              <Input.TextArea value={source} disabled={busy} rows={10} aria-label="端口 JSON 内容" placeholder='粘贴 {"format_version":3,"rules":[…]}，每批 1–500 条端口转发' onChange={e => { setSource(e.target.value); resetPreview(); }} />
            </div>
          </> },
          { key: 'text', label: '文本导入', disabled: busy, children: <Form layout="vertical" disabled={busy}>
            <Form.Item label="入口" required extra="整批共用一个入口节点或入口组；不同入口请分批导入。">
              <Select value={shared.line || undefined} showSearch optionFilterProp="label" placeholder="选择入口节点或入口组" onChange={line => updateShared({ line })} options={[
                { label: '入口节点', options: nodes.map(node => ({ value: `node:${node.id}`, label: node.name })) },
                { label: '入口组', options: groups.map(group => ({ value: `group:${group.id}`, label: group.name })) },
              ]} />
            </Form.Item>
            <Form.Item label="链路形态" required>
              <Segmented value={shared.mode} options={Object.entries(ruleModeLabels).map(([value, label]) => ({ value, label }))} onChange={value => updateShared({ mode: value as RuleImportShared['mode'], exit_group_id: undefined, chain_id: undefined })} />
            </Form.Item>
            {shared.mode === 'single' && <Form.Item label="端点组" required><Select value={shared.exit_group_id} showSearch optionFilterProp="label" placeholder="选择端点组" options={exitGroups.map(group => ({ value: group.id, label: group.name }))} onChange={exit_group_id => updateShared({ exit_group_id })} /></Form.Item>}
            {shared.mode === 'chain' && <Form.Item label="转发链" required><Select value={shared.chain_id} showSearch optionFilterProp="label" placeholder="选择已保存的转发链" options={chains.map(chain => ({ value: chain.id, label: `${chain.name}（${chain.rule_count} 个引用端口）` }))} onChange={chain_id => updateShared({ chain_id })} /></Form.Item>}
            <Form.Item extra="域名解析到私网、回环或链路本地地址时，同样需要显式允许。"><Checkbox checked={shared.allow_private} onChange={e => updateShared({ allow_private: e.target.checked })}>允许私网目标</Checkbox></Form.Item>
            <Form.Item label="规则文本" required extra="每批 1–500 条；只认半角 #，每段去首尾空白，空行跳过，不支持注释。名称必填且不超过 200 字节，监听与目标端口必填（1–65535），目标地址为 IPv4 或域名。">
              <Input.TextArea value={textSource} rows={10} aria-label="规则文本内容" placeholder={'每行：规则名#监听端口#目标地址#目标端口\n例如：zx香港#33311#hka.4441234.xyz#3400'} onChange={e => { setTextSource(e.target.value); resetPreview(); }} />
            </Form.Item>
          </Form> },
        ]} />
      </FormSection>
      {error && <Alert className="ports-import-error" type="error" showIcon title={error} />}
      {localErrors.length > 0 && <>
        <Alert type="error" showIcon title="本地校验未通过，未发送请求。请修正后重新预检。" />
        <TableCard<RuleTextError> title="本地校验结果" size="small" rowKey="line" dataSource={localErrors} pagination={{ pageSize: 10, showSizeChanger: false }} columns={[
          { title: '原文行号', dataIndex: 'line', width: 100, render: line => `第 ${line} 行` },
          { title: '错误原因', dataIndex: 'message', render: message => <Typography.Text type="danger">{message}</Typography.Text> },
        ]} />
      </>}
      {result && <>
        <Alert showIcon type={imported ? 'success' : result.valid ? 'info' : 'error'} title={imported ? '整批已保存，请核对端口同步状态。' : result.valid ? '全部预检通过，尚未写入。请核对下表后确认导入。' : '预检失败，未写入任何端口。'} />
        <TableCard<ImportRow> title="预检结果" size="small" rowKey="index" dataSource={result.results} pagination={{ pageSize: 10, showSizeChanger: false }} scroll={{ x: 650 }} columns={[
          tab === 'text' ? { title: '原文行号', dataIndex: 'line', width: 100, render: line => `第 ${line} 行` } : { title: '序号', dataIndex: 'index', width: 60 },
          { title: '端口名称', dataIndex: 'name' },
          { title: '操作', render: (_, row) => row.action === 'create' ? '新增（新 ID）' : <span>覆盖 <Typography.Text code>{row.id}</Typography.Text></span> },
          { title: '预检结果', render: (_, row) => row.errors.length ? <>{row.errors.map((reason, i) => <div key={i}><Typography.Text type="danger">{reason}</Typography.Text></div>)}{(row.conflict_count ?? 0) > 1 && <Typography.Text type="secondary">共 {row.conflict_count} 处相关监听，显示首处原因；修正后请重新预检。</Typography.Text>}</> : '通过' },
        ]} />
      </>}
    </Modal>
  </>;
}
