import assert from 'node:assert/strict';
import test from 'node:test';
import { updateMemberSelection } from './src/entryGroupMembers.ts';

test('穿梭添加成员保留原首选入口，并将新成员追加到末尾', () => {
  const original = ['preferred', 'backup'];
  assert.deepEqual(updateMemberSelection(original, ['new', 'backup', 'preferred']), ['preferred', 'backup', 'new']);
  assert.deepEqual(original, ['preferred', 'backup']);
});

test('移除成员后保留剩余优先级，并保留未出现在服务器清单中的已有成员', () => {
  assert.deepEqual(updateMemberSelection(['offline', 'preferred', 'backup'], ['backup', 'offline']), ['offline', 'backup']);
});

test('反复选择不会重复添加成员，清空仍交给表单做必填校验', () => {
  assert.deepEqual(updateMemberSelection(['a'], ['a', 'b', 'b']), ['a', 'b']);
  assert.deepEqual(updateMemberSelection(['a'], []), []);
});
