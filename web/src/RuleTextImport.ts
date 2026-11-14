import type { RuleInput, RuleMode } from './RulesData';

export type RuleTextRow = Pick<RuleInput, 'name' | 'listen_port' | 'target_address' | 'target_port'> & { line: number };
export type RuleTextError = { line: number; message: string };
export type RuleImportShared = { line: string; mode: RuleMode; exit_group_id?: string; chain_id?: string; allow_private: boolean };

export function parseRuleText(text: string): { rows: RuleTextRow[]; errors: RuleTextError[] } {
  const rows: RuleTextRow[] = [];
  const errors: RuleTextError[] = [];
  text.split(/\r\n|\n|\r/).forEach((source, index) => {
    if (!source.trim()) return;
    const line = index + 1;
    const fields = source.split('#').map(value => value.trim());
    if (fields.length !== 4) {
      errors.push({ line, message: '每行必须用半角 # 分隔为 4 段：规则名#监听端口#目标地址#目标端口。' });
      return;
    }
    const [name, listen, target_address, target] = fields;
    const reasons: string[] = [];
    if (!name) reasons.push('规则名称不能为空');
    else if (new TextEncoder().encode(name).length > 200) reasons.push('规则名称不能超过 200 字节（UTF-8）');
    for (const [label, value] of [['监听端口', listen], ['目标端口', target]]) {
      if (!/^[0-9]+$/.test(value) || Number(value) < 1 || Number(value) > 65535) reasons.push(`${label}须为 1–65535 的整数，不能为空`);
    }
    if (reasons.length) errors.push({ line, message: reasons.join('；') });
    else rows.push({ line, name, listen_port: Number(listen), target_address, target_port: Number(target) });
  });
  if (rows.length > 500) errors.push({ line: rows[500].line, message: '每批最多导入 500 条有效规则，请分批导入。' });
  return { rows, errors };
}

export function buildRuleImportPayload(rows: RuleTextRow[], shared: RuleImportShared): { format_version: 3; rules: RuleInput[] } {
  return { format_version: 3, rules: rows.map(row => ({
    name: row.name, listen_port: row.listen_port, target_address: row.target_address, target_port: row.target_port,
    target_family: 'ipv4', allow_private: shared.allow_private, enabled: true,
    ...(shared.line.startsWith('node:') ? { entry_node_id: shared.line.slice(5) } : { entry_group_id: shared.line.slice(6) }),
    kind: shared.mode === 'direct' ? 'direct' : 'tunnel',
    ...(shared.mode === 'single' ? { exit_group_id: shared.exit_group_id } : {}),
    ...(shared.mode === 'chain' ? { chain_id: shared.chain_id } : {}),
  })) };
}

export function mapRuleImportResults<T>(results: T[], rows: RuleTextRow[]): (T & { line: number })[] {
  return results.map((row, index) => ({ ...row, line: rows[index].line }));
}
