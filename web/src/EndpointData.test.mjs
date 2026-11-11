import assert from 'node:assert/strict';
import { test } from 'node:test';
import { moveMembers, transferMembers, healthStatus, healthIsFresh } from './EndpointData.ts';

test('添加端点追加到主备队尾，不重复、不重排已有成员', () => {
  const current = ['backup', 'primary'];
  assert.deepEqual(transferMembers(current, ['primary', 'new', 'new'], 'add'), ['backup', 'primary', 'new']);
  assert.deepEqual(current, ['backup', 'primary']);
});

test('移除主端点后下一成员成为主端点，其余备用顺序保持', () => {
  assert.deepEqual(transferMembers(['primary', 'backup-1', 'backup-2'], ['primary'], 'remove'), ['backup-1', 'backup-2']);
  assert.deepEqual(transferMembers(['only'], ['only'], 'remove'), []);
});

test('批量上下移保持相邻成员的相对顺序，边界成员不越界', () => {
  const current = ['a', 'b', 'c', 'd', 'e'];
  assert.deepEqual(moveMembers(current, ['b', 'c', 'e'], 'up'), ['b', 'c', 'a', 'e', 'd']);
  assert.deepEqual(moveMembers(current, ['a', 'c', 'd'], 'down'), ['b', 'a', 'e', 'c', 'd']);
  assert.deepEqual(moveMembers(current, ['a', 'b'], 'up'), current);
  assert.deepEqual(moveMembers(current, ['d', 'e'], 'down'), current);
  assert.deepEqual(current, ['a', 'b', 'c', 'd', 'e']);
});

test('过期的健康证据仅显示历史，不能渲染为当前健康或故障', () => {
  assert.deepEqual(healthStatus(false, 'healthy'), { tone: 'muted', label: '历史 · 健康' });
  assert.deepEqual(healthStatus(false, 'unhealthy'), { tone: 'muted', label: '历史 · 故障' });
  assert.deepEqual(healthStatus(true, 'healthy'), { tone: 'ok', label: '健康' });
  assert.deepEqual(healthStatus(true, 'unhealthy'), { tone: 'danger', label: '故障' });
  assert.deepEqual(healthStatus(true, 'recovering'), { tone: 'warn', label: '恢复观察中' });
  assert.deepEqual(healthStatus(true, 'unknown'), { tone: 'muted', label: '待探测' });
});

test('健康观测在采样后 10 秒自动过期，后续请求挂起也不能保留实时标签', () => {
  const sampled = Date.parse('2026-11-11T00:00:00Z');
  const report = { fresh: true, sampled_at: '2026-11-11T00:00:00Z', exits: [] };
  assert.equal(healthIsFresh(report, sampled + 9999), true);
  assert.equal(healthIsFresh(report, sampled + 10000), false);
  assert.equal(healthIsFresh(report, sampled + 60000), false);
  assert.equal(healthIsFresh({ ...report, fresh: false }, sampled), false);
  assert.equal(healthIsFresh({ ...report, sampled_at: null }, sampled), false);
  assert.equal(healthIsFresh({ ...report, sampled_at: 'invalid' }, sampled), false);
  assert.equal(healthIsFresh(report, sampled - 1), false);
  assert.equal(healthIsFresh(undefined, sampled), false);
});
