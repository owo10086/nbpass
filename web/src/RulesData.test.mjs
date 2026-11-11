import assert from 'node:assert/strict';
import { test } from 'node:test';
import { buildRuleInput, filterRules } from './RulesData.ts';

const rows = [
  { id: 'a', name: '香港 Web', entry_node_id: 'shared', kind: 'direct', enabled: true, target_address: 'Web.Example.com', target_port: 443 },
  { id: 'b', name: '备用服务', entry_group_id: 'shared', kind: 'tunnel', enabled: false, target_address: '203.0.113.10', target_port: 8443 },
  { id: 'c', name: '香港备用', entry_node_id: 'other', kind: 'tunnel', enabled: true, target_address: 'backup.example.com', target_port: 443 },
];

test('名称与目标搜索忽略大小写及两端空格，支持地址和端口', () => {
  for (const [search, ids] of [
    ['香港', ['a', 'c']], [' WEB.EXAMPLE.COM ', ['a']], ['203.0.113.10:8443', ['b']], ['8443', ['b']], ['没有匹配', []],
  ]) {
    assert.deepEqual(filterRules(rows, { search }).map(row => row.id), ids);
  }
});

test('线路筛选区分同 ID 的服务器和服务器组', () => {
  assert.deepEqual(filterRules(rows, { line: 'node:shared' }).map(row => row.id), ['a']);
  assert.deepEqual(filterRules(rows, { line: 'group:shared' }).map(row => row.id), ['b']);
});

test('模式和状态筛选取交集，暂停状态不与未筛选混淆', () => {
  assert.deepEqual(filterRules(rows, { kind: 'tunnel', enabled: false }).map(row => row.id), ['b']);
  assert.deepEqual(filterRules(rows, { kind: 'direct', enabled: false }), []);
  assert.deepEqual(filterRules(rows, { search: '香港', kind: 'tunnel', enabled: true }).map(row => row.id), ['c']);
});

test('关联端口定位继续生效，清空筛选保留自定义列表顺序', () => {
  assert.deepEqual(filterRules(rows, {}, 'b').map(row => row.id), ['b']);
  assert.deepEqual(filterRules(rows, {}, 'deleted'), []);
  assert.deepEqual(filterRules(rows, {}).map(row => row.id), ['a', 'b', 'c']);
  assert.deepEqual(rows.map(row => row.id), ['a', 'b', 'c']);
});

const values = {
  name: '测试端口', line: 'node:shared', kind: 'direct', listen_port: 32000,
  target_address: 'example.com', target_port: 443, allow_private: false, enabled: true,
};

test('保存直连端口只提交现有 API 字段，清除残留的服务器组和端点组', () => {
  const input = buildRuleInput({ ...values, entry_group_id: 'stale', exit_group_id: 'stale', id: 'a', sync_status: '正常' });
  assert.deepEqual(JSON.parse(JSON.stringify(input)), {
    name: '测试端口', entry_node_id: 'shared', kind: 'direct', listen_port: 32000,
    target_address: 'example.com', target_family: 'ipv4', target_port: 443, allow_private: false, enabled: true,
  });
});

test('服务器组单跳转发保留端点组，自动分配端口同时提交 0 和分配标记', () => {
  const input = buildRuleInput({ ...values, line: 'group:shared', kind: 'tunnel', exit_group_id: 'exit', random_port: true });
  assert.deepEqual(JSON.parse(JSON.stringify(input)), {
    name: '测试端口', entry_group_id: 'shared', kind: 'tunnel', exit_group_id: 'exit', listen_port: 0, random_port: true,
    target_address: 'example.com', target_family: 'ipv4', target_port: 443, allow_private: false, enabled: true,
  });
});
