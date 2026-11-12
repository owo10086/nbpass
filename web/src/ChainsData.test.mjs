import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  addChainGroup, moveChainGroup, removeChainGroup, chainFormValues, chainPayload,
  chainSaveRequest, chainPathLabel, chainMatchesQuery, chainObservationStatus,
  observationMember,
} from './ChainsData.ts';

const chain = {
  id: 'chain/a?b', name: '沪日路径', exit_group_ids: ['group-b', 'group-c'], rule_count: 3,
  hops: [
    { position: 1, exit_group_id: 'group-b', name: '日本中转', members: [
      { node_id: 'b', node_name: 'B', address_id: 'ix-b', address: 'b.ix.example.com', port: 2443 },
      { node_id: 'backup', node_name: '备用 B', address_id: 'public', address: '203.0.113.2', port: 18443 },
    ], observations: [] },
    { position: 2, exit_group_id: 'group-c', name: '最终出口', members: [
      { node_id: 'c', node_name: 'C', address_id: 'public', address: 'c.example.com', port: 3443 },
    ], observations: [] },
  ],
};

test('按稳定组 ID 添加、上下移动和移除，边界不越界且不改原数组', () => {
  const ids = ['group-b', 'group-c', 'group-d'];
  assert.deepEqual(addChainGroup(ids, 'group-e'), ['group-b', 'group-c', 'group-d', 'group-e']);
  assert.throws(() => addChainGroup(ids, 'group-c'), /重复/);
  assert.deepEqual(moveChainGroup(ids, 'group-c', 'up'), ['group-c', 'group-b', 'group-d']);
  assert.deepEqual(moveChainGroup(ids, 'group-c', 'down'), ['group-b', 'group-d', 'group-c']);
  assert.deepEqual(moveChainGroup(ids, 'group-b', 'up'), ids);
  assert.deepEqual(moveChainGroup(ids, 'group-d', 'down'), ids);
  assert.deepEqual(moveChainGroup(ids, 'missing', 'up'), ids);
  assert.deepEqual(removeChainGroup(ids, 'group-c'), ['group-b', 'group-d']);
  assert.deepEqual(ids, ['group-b', 'group-c', 'group-d']);
});

test('编辑拷贝组 ID，重排后序列化不修改原链或输入', () => {
  const values = chainFormValues(chain);
  values.exit_group_ids.reverse();
  assert.deepEqual(chain.exit_group_ids, ['group-b', 'group-c']);
  assert.deepEqual(chainPayload(values), { name: '沪日路径', exit_group_ids: ['group-c', 'group-b'] });
  assert.deepEqual(values.exit_group_ids, ['group-c', 'group-b']);
  assert.deepEqual(chainFormValues(), { name: '', exit_group_ids: [] });
});

test('请求严格序列化名称与有序组 ID，PUT 保留链 ID 并安全编码路径', () => {
  const request = chainSaveRequest({ ...chain, name: ' 沪日路径 ' }, chain.id);
  assert.deepEqual(request, {
    path: '/forward-chains/chain%2Fa%3Fb', method: 'PUT',
    body: { name: '沪日路径', exit_group_ids: ['group-b', 'group-c'] },
  });
  assert.equal(JSON.stringify(request.body), '{"name":"沪日路径","exit_group_ids":["group-b","group-c"]}');
  assert.equal(chainSaveRequest(chainFormValues(chain)).method, 'POST');
  assert.equal(chainSaveRequest(chainFormValues(chain)).path, '/forward-chains');
});

test('拒绝不足两组、重复组及超过 200 字节名称，不把中文字符数当字节数', () => {
  for (const exit_group_ids of [[], ['group-b']]) {
    assert.throws(() => chainPayload({ name: '链', exit_group_ids }), /至少.*两个/);
  }
  assert.throws(() => chainPayload({ name: '链', exit_group_ids: ['b', 'b'] }), /重复/);
  assert.throws(() => chainPayload({ name: '  ', exit_group_ids: ['b', 'c'] }), /名称/);
  assert.throws(() => chainPayload({ name: '中'.repeat(67), exit_group_ids: ['b', 'c'] }), /200 字节/);
  assert.equal(chainPayload({ name: '中'.repeat(66) + 'ab', exit_group_ids: ['b', 'c'] }).name.length, 68);
});

test('路径与搜索使用真实顺序、备用成员、所选地址和有效拨号端口', () => {
  assert.equal(chainPathLabel(chain), '1. 日本中转 → 2. 最终出口');
  for (const query of ['日本中转', ' GROUP-C ', '备用 B', 'B.IX.EXAMPLE.COM:2443', 'ix-b', 'chain/a?b']) {
    assert.equal(chainMatchesQuery(chain, query), true, query);
  }
  assert.equal(chainMatchesQuery(chain, 'unselected-address'), false);
  assert.equal(chainPathLabel({ ...chain, hops: [] }), '1. group-b → 2. group-c');
});

const sampled = '2026-11-12T00:00:00Z';
const now = Date.parse(sampled);
const observation = {
  from_node_id: 'a', to_node_id: 'b', address_id: 'ix-b', endpoint_config_digest: 'digest-b',
  chain_fingerprint: 'fingerprint', rule_id: 'rule-a', generation: 7,
  state: 'ready', fresh: true, sampled_at: sampled,
};

test('无观测与未知状态不生成健康，逐边就绪明确不包含目标探测', () => {
  assert.deepEqual(chainObservationStatus(undefined, now), { tone: 'muted', label: '待观测' });
  assert.deepEqual(chainObservationStatus({ ...observation, state: 'unknown', sampled_at: '' }, now), { tone: 'muted', label: '待观测' });
  assert.deepEqual(chainObservationStatus(observation, now), { tone: 'ok', label: '本边及后续路径就绪（未探测目标）' });
  assert.equal(chainObservationStatus({ ...observation, state: 'new-state' }, now).tone, 'muted');
});

test('本边未就绪、后续路径未就绪与授权应用失败分别展示', () => {
  assert.deepEqual(chainObservationStatus({ ...observation, state: 'edge_unhealthy' }, now), { tone: 'danger', label: '本边未就绪' });
  assert.deepEqual(chainObservationStatus({ ...observation, state: 'suffix_unknown' }, now), { tone: 'warn', label: '本边可达 · 后续路径未就绪' });
  assert.deepEqual(chainObservationStatus({ ...observation, state: 'grant_failed' }, now), { tone: 'danger', label: '授权应用失败' });
});

test('fresh=false、10 秒到期、未来和非法时间均不能保持当前健康色', () => {
  assert.equal(chainObservationStatus(observation, now + 9999).tone, 'ok');
  for (const [value, at] of [
    [observation, now + 10000], [observation, now - 1],
    [{ ...observation, fresh: false }, now], [{ ...observation, sampled_at: 'invalid' }, now],
  ]) {
    assert.deepEqual(chainObservationStatus(value, at), { tone: 'muted', label: '历史 · 本边及后续路径就绪（未探测目标）' });
  }
  assert.deepEqual(chainObservationStatus({ ...observation, state: 'historical' }, now), { tone: 'muted', label: '历史 · 状态未知' });
});

test('观测目标同时匹配服务器和所选地址，不能借用该服务器其他地址', () => {
  assert.equal(observationMember(chain.hops[0], observation)?.port, 2443);
  assert.equal(observationMember(chain.hops[0], { ...observation, address_id: 'public' }), undefined);
  assert.equal(observationMember(chain.hops[0], { ...observation, to_node_id: 'missing' }), undefined);
  assert.deepEqual(chainObservationStatus({ ...observation, address_id: 'public' }, now, chain.hops[0]), {
    tone: 'muted', label: '历史 · 未匹配当前候选地址',
  });
});
