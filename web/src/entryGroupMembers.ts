// 穿梭控件给出的顺序可能把新成员放在前面；成员顺序决定主备优先级，必须显式保留。
export function updateMemberSelection(current: string[], selected: string[]): string[] {
  const next = new Set(selected);
  return [...current.filter(id => next.has(id)), ...[...next].filter(id => !current.includes(id))];
}
