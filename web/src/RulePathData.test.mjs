import assert from 'node:assert/strict';
import { test } from 'node:test';
import { rulePathSteps, edgeStatus, grantStatus, entryStatus, affectedOperations } from './RulePathData.ts';

const now = Date.parse('2026-11-12T12:00:00Z');
const row = {
  id: 'r', kind: 'tunnel', chain_id: 'chain', entry_group_id: 'entries', target_address: 'target.example', target_port: 443,
  path: { entries: [{ node_id: 'a1', node_name: 'A1' }, { node_id: 'a2', node_name: 'A2' }],
    hops: [{ position: 1, exit_group_id: 'b', name: '日本', members: [
      { node_id: 'b1', node_name: 'B1', address_id: 'ix', address: 'ix.example', port: 8443 },
      { node_id: 'b2', node_name: 'B2', address_id: 'public', address: '203.0.113.2', port: 3443 },
    ] }, { position: 2, exit_group_id: 'c', name: '末跳', members: [{ node_id: 'c', node_name: 'C', address_id: 'ix2', address: 'c.example', port: 9443 }] }],
    target: { address: 'target.example', port: 443 } },
};
const edge = { rule_id: 'r', from_node_id: 'a1', to_node_id: 'b1', address_id: 'ix', hop_index: 1, endpoint_config_digest: 'd', chain_fingerprint: 'fp', rule_generation: 7, grant_generation: 7, desired_revision: 10, applied_revision: 8, local_ready: true, suffix_ready: true, state: 'ready', fresh: true, observed_at: '2026-11-12T11:59:59Z' };

test('完整定义保留全部入口和各跳主备、域名映射端口及最终目标，无证据不选择唯一成员', () => {
  const steps = rulePathSteps(row, '入口组');
  assert.equal(steps.length, 4);
  assert.match(steps[0], /入口组.*A1.*A2/);
  assert.match(steps[1], /第 1 跳.*日本.*主.*B1.*ix.*ix.example:8443.*备.*B2.*public.*203.0.113.2:3443/);
  assert.match(steps[2], /第 2 跳.*c.example:9443/);
  assert.match(steps[3], /最终目标.*target.example:443/);
});

test('缺失引用不退化直连，可用已保存链的定义兜底且不借管理页健康', () => {
  const missing = { ...row, path: undefined };
  assert.match(rulePathSteps(missing, '入口组').join(' → '), /链引用待确认.*chain/);
  const fallback = rulePathSteps(missing, '入口组', [{ id: 'chain', hops: row.path.hops }]);
  assert.match(fallback[1], /ix.example:8443/);
  assert.equal(edgeStatus(undefined, now).tone, 'muted');
  assert.match(edgeStatus(undefined, now).label, /待观测/);
});

test('本边、后续、grant应用失败与历史分开，版本相等不替代持久授权确认', () => {
  assert.equal(edgeStatus(edge, now).tone, 'ok');
  assert.match(edgeStatus(edge, now).label, /未探测目标/);
  assert.match(edgeStatus({ ...edge, state: 'suffix_unknown', suffix_ready: false }, now).label, /本边可达.*后续/);
  assert.match(edgeStatus({ ...edge, state: 'edge_unhealthy', local_ready: false }, now).label, /本边未就绪/);
  assert.match(edgeStatus({ ...edge, state: 'grant_failed', apply_error: 'disk full' }, now).label, /授权应用失败/);
  assert.match(grantStatus({ ...edge, apply_error: 'disk full' }), /disk full/);
  assert.match(grantStatus({ ...edge, grant_generation: 6 }), /重新.*确认/);
  assert.match(grantStatus({ ...edge, state: 'unknown', local_ready: false, suffix_ready: false }), /待.*确认/);
  assert.match(grantStatus(edge), /确认/);
  for (const old of [{ ...edge, fresh: false }, { ...edge, observed_at: '2026-11-12T11:59:40Z' }, { ...edge, observed_at: null }]) {
    assert.notEqual(edgeStatus(old, now).tone, 'ok');
    assert.match(edgeStatus(old, now).label, /历史|待观测/);
  }
});

test('入口逐A1/A2使用独立证据，过期就绪和控制离线不等于业务中断', () => {
  const a1 = { readiness_status: 'not_ready', ready: false, checked_at: edge.observed_at };
  const a2 = { readiness_status: 'ready', ready: true, checked_at: edge.observed_at };
  assert.notEqual(entryStatus(a1, now).tone, 'ok');
  assert.equal(entryStatus(a2, now).tone, 'ok');
  assert.match(entryStatus(a2, now + 11000).label, /历史/);
  assert.equal(entryStatus(undefined, now).tone, 'muted');
});

test('每跳服务器反查所有受影响端口，不将转发节点混入入口决策members', () => {
  const rules = [{ id: 'r1', members: ['a'], path_nodes: ['a', 'b', 'c'] }, { id: 'r2', members: ['d'], path_nodes: ['d', 'b', 'e'] }, { id: 'other', members: ['z'], path_nodes: [] }];
  assert.deepEqual(affectedOperations(rules, 'b').map(r => r.id), ['r1', 'r2']);
  assert.deepEqual(affectedOperations(rules, 'z').map(r => r.id), ['other']);
  assert.deepEqual(rules[0].members, ['a']);
});

test('旧单跳仅按配置顺序列候选，不为最小连接数模式臆造固定主备', () => {
  const single = { ...row, chain_id: undefined, exit_group_id: 'b', path: { ...row.path, hops: row.path.hops.slice(0, 1) } };
  assert.match(rulePathSteps(single, '入口组')[1], /候选1.*B1.*候选2.*B2/);
});
