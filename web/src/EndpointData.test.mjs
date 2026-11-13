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

test('地址表单保留专线稳定 ID，空对外端口明确清空并显示本机监听端口', async () => {
  const { endpointPayload, endpointFormValues, effectiveEndpointPort } = await import('./EndpointData.ts');
  const endpoint = { node_id: 'b', address: 'exit.test', listen_port: 18443, public_port: 2443, dedicated_addresses: [
    { id: 'stable-ix', name: '沪日 IX', address: '10.0.0.2', public_port: null },
  ] };
  assert.deepEqual(endpointFormValues(endpoint), endpoint);
  assert.equal(effectiveEndpointPort(endpoint), 2443);
  assert.equal(effectiveEndpointPort({ public_port: null, listen_port: 18443 }), 18443);
  const values = { ...endpoint, address: ' EXIT.Test. ', public_port: undefined, dedicated_addresses: [
    { id: 'stable-ix', name: ' 沪日 IX ', address: ' IX.Test. ', public_port: undefined },
    { name: '新增', address: '10.0.0.3', public_port: 3443 },
  ] };
  assert.deepEqual(endpointPayload(values), { address: 'exit.test', listen_port: 18443, transport: 'wss', public_port: null, dedicated_addresses: [
    { id: 'stable-ix', name: '沪日 IX', address: 'ix.test', public_port: null },
    { name: '新增', address: '10.0.0.3', public_port: 3443 },
  ] });
  assert.deepEqual(endpointPayload({ ...values, dedicated_addresses: [] }).dedicated_addresses, []);
  assert.deepEqual(endpointFormValues(undefined), { address: '', listen_port: undefined, public_port: null, dedicated_addresses: [] });
});

test('地址提示保留公网、本机端口及全部专线，并分别使用各自有效端口', async () => {
  const { endpointAddressDetails } = await import('./EndpointData.ts');
  assert.deepEqual(endpointAddressDetails({ node_id: 'exit', address: 'public.test', listen_port: 18443, public_port: 2443, dedicated_addresses: [
    { id: 'ix', name: '沪日 IX', address: 'ix.test', public_port: null },
    { name: '备用专线', address: '10.0.0.3', public_port: 3443 },
  ] }), [
    { label: '公网地址', value: 'public.test:2443' },
    { label: '本机监听端口', value: '18443' },
    { label: '沪日 IX', value: 'ix.test:18443' },
    { label: '备用专线', value: '10.0.0.3:3443' },
  ]);
});

test('无专线且未设置公网对外端口时，地址提示使用本机监听端口', async () => {
  const { endpointAddressDetails } = await import('./EndpointData.ts');
  assert.deepEqual(endpointAddressDetails({ node_id: 'exit', address: '203.0.113.20', listen_port: 443 }), [
    { label: '公网地址', value: '203.0.113.20:443' },
    { label: '本机监听端口', value: '443' },
  ]);
});
