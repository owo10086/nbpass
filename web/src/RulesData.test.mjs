import assert from 'node:assert/strict';
import { test } from 'node:test';
import { buildRuleInput, filterRules } from './RulesData.ts';
import * as data from './RulesData.ts';

test('升级提示保留服务器、地址、能力和原因，备用不足使用警示色', () => {
  assert.equal(data.configurationStatusTone('整条阻塞（需升级服务器）'), 'danger');
  assert.equal(data.configurationStatusTone('可运行但备用成员能力不足'), 'warn');
  assert.equal(data.configurationStatusTone('正常'), 'ok');
  assert.equal(data.configurationBlockerText({ node_id: 'b', node_name: '日本备用', address_id: '沪日 IX', capability: 'ix_member_identity', reason: '需升级服务器' }), '日本备用 · 沪日 IX：ix_member_identity；需升级服务器');
  assert.equal(data.configurationBlockerText({ node_id: 'b', node_name: '', address_id: '', capability: 'outbound_ipv4', reason: '未声明能力' }), 'b：outbound_ipv4；未声明能力');
});

test('合并列后仍可按完整 ID 与同步状态双向排序，清除后恢复自定义顺序', () => {
  const ports = [
    { id: 'port10', sync_status: '同步失败' },
    { id: 'port2', sync_status: '正常' },
    { id: 'port1', sync_status: '待确认' },
  ];
  assert.deepEqual(data.sortRuleDetails(ports, 'id-ascend').map(row => row.id), ['port1', 'port2', 'port10']);
  assert.deepEqual(data.sortRuleDetails(ports, 'id-descend').map(row => row.id), ['port10', 'port2', 'port1']);
  assert.deepEqual(data.sortRuleDetails(ports, 'sync-ascend').map(row => row.id), ['port1', 'port10', 'port2']);
  assert.deepEqual(data.sortRuleDetails(ports, 'sync-descend').map(row => row.id), ['port2', 'port10', 'port1']);
  assert.deepEqual(data.sortRuleDetails(ports).map(row => row.id), ['port10', 'port2', 'port1']);
  assert.deepEqual(ports.map(row => row.id), ['port10', 'port2', 'port1']);
});

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

test('三种界面模式回填及往返严格保持 wire kind 和唯一引用', () => {
  for (const [mode, wire] of [
    ['direct', { kind: 'direct' }], ['single', { kind: 'tunnel', exit_group_id: 'exit' }],
    ['chain', { kind: 'tunnel', chain_id: 'chain' }],
  ]) {
    const row = { ...values, ...wire, id: 'r', entry_node_id: 'shared', traffic: { upload_bytes: 99 }, sync_status: '正常' };
    const form = data.ruleFormValues(row);
    assert.equal(form.mode, mode);
    const result = JSON.parse(JSON.stringify(buildRuleInput(form)));
    assert.equal(result.kind, wire.kind);
    assert.equal(result.exit_group_id, wire.exit_group_id);
    assert.equal(result.chain_id, wire.chain_id);
    assert.equal(result.entry_node_id, 'shared');
    assert.equal(result.target_address, 'example.com');
    assert.equal(result.traffic, undefined);
    assert.equal(result.mode, undefined);
  }
});

test('模式切换清理所有隐藏引用，链启停和复制保留定义且不提交累计量', () => {
  const stale = { ...values, kind: 'tunnel', exit_group_id: 'old', chain_id: 'chain' };
  for (const [mode, exit, chain] of [['direct', undefined, undefined], ['single', 'old', undefined], ['chain', undefined, 'chain']]) {
    const input = buildRuleInput({ ...stale, mode });
    assert.equal(input.kind, mode === 'direct' ? 'direct' : 'tunnel');
    assert.equal(input.exit_group_id, exit);
    assert.equal(input.chain_id, chain);
  }
  const row = { ...stale, entry_group_id: 'entries', entry_node_id: undefined, enabled: false, traffic: { upload_bytes: 500 } };
  const input = buildRuleInput({ ...data.ruleFormValues(row), enabled: true });
  assert.equal(input.chain_id, 'chain');
  assert.equal(input.exit_group_id, undefined);
  assert.equal(input.entry_group_id, 'entries');
  assert.equal(input.entry_node_id, undefined);
  assert.equal(input.enabled, true);
  assert.equal(input.traffic, undefined);
  assert.equal(input.listen_port, 32000);
  const copy = buildRuleInput({ ...data.ruleFormValues(row), random_port: true });
  assert.equal(copy.listen_port, 0);
  assert.equal(copy.random_port, true);
  assert.equal(copy.chain_id, 'chain');
});

test('链筛选与单跳互斥，临时缺失链仍保持链模式并保留引用', () => {
  const chain = { ...rows[2], id: 'chain-rule', chain_id: 'missing-chain' };
  assert.deepEqual(filterRules([...rows, chain], { mode: 'chain' }).map(r => r.id), ['chain-rule']);
  assert.deepEqual(filterRules([...rows, chain], { mode: 'single' }).map(r => r.id), ['b', 'c']);
  assert.equal(data.ruleFormValues(chain).mode, 'chain');
  assert.equal(buildRuleInput(data.ruleFormValues(chain)).chain_id, 'missing-chain');
});

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
const settle = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };
function reloadFixture() {
  const reads = [], applied = [], errors = [], loading = [];
  const loader = data.createRuleReloader(() => { const request = deferred(); reads.push(request); return request.promise; }, {
    apply: value => applied.push(value), failed: error => errors.push(error), loading: value => loading.push(value),
  });
  return { loader, reads, applied, errors, loading };
}

test('慢读取跨越多个五秒轮询仍完成初显，轮询不发并行请求也不废弃结果', async () => {
  const f = reloadFixture();
  const first = f.loader.refresh();
  await settle();
  // Each poll is the existing five-second timer firing while HTTP is pending.
  f.loader.poll(); f.loader.poll(); f.loader.poll();
  await settle();
  assert.equal(f.reads.length, 1);
  f.reads[0].resolve('slow initial rows');
  await first;
  assert.deepEqual(f.applied, ['slow initial rows']);
  f.loader.poll();
  await settle();
  assert.equal(f.reads.length, 2);
  f.reads[1].resolve('next rows');
  await settle();
  assert.deepEqual(f.applied, ['slow initial rows', 'next rows']);
  f.loader.dispose();
});

test('慢401和428不会被后续轮询或显式重载吞掉，失败后可继续读取', async () => {
  for (const status of [401, 428]) {
    const f = reloadFixture();
    const first = f.loader.refresh();
    await settle();
    f.loader.poll(); f.loader.poll();
    const explicit = f.loader.refresh();
    const expired = { status, message: 'session expired' };
    f.reads[0].reject(expired);
    await settle();
    assert.deepEqual(f.errors, [expired]);
    assert.equal(f.reads.length, 2);
    f.reads[1].resolve('fresh session rows');
    await Promise.all([first, explicit]);
    assert.deepEqual(f.applied, ['fresh session rows']);
    f.loader.poll();
    await settle();
    assert.equal(f.reads.length, 3);
    f.reads[2].resolve('later rows');
    await settle();
    f.loader.dispose();
  }
});

test('保存批量拖动和手动刷新在途时合并必要重载，所有调用等待最新读取且旧结果不覆盖', async () => {
  const f = reloadFixture();
  const beforeMutation = f.loader.refresh();
  await settle();
  const saveReload = f.loader.refresh();
  const batchReload = f.loader.refresh();
  const dragReload = f.loader.refresh();
  const manualReload = f.loader.refresh();
  await settle();
  assert.equal(f.reads.length, 1);
  f.reads[0].resolve('stale pre-mutation rows');
  await settle();
  assert.equal(f.reads.length, 2);
  assert.deepEqual(f.applied, []);
  let completed = false;
  const all = Promise.all([beforeMutation, saveReload, batchReload, dragReload, manualReload]).then(() => { completed = true; });
  await settle();
  assert.equal(completed, false);
  // Another mutation after the follow-up started requires one further read.
  const laterMutation = f.loader.refresh();
  f.reads[1].resolve('stale intermediate rows');
  await settle();
  assert.equal(f.reads.length, 3);
  assert.deepEqual(f.applied, []);
  assert.equal(completed, false);
  f.reads[2].resolve('latest committed rows');
  await Promise.all([all, laterMutation]);
  assert.deepEqual(f.applied, ['latest committed rows']);
  assert.deepEqual(f.loading, [true, false]);
  f.loader.dispose();
});

test('卸载后迟到成功或错误均无副作用，也不启动已排队重载', async () => {
  for (const fail of [false, true]) {
    const f = reloadFixture();
    const pending = f.loader.refresh();
    await settle();
    const queued = f.loader.refresh();
    f.loader.dispose();
    if (fail) f.reads[0].reject({ status: 401 }); else f.reads[0].resolve('unmounted');
    await settle();
    assert.equal(f.reads.length, 1);
    await Promise.all([pending, queued, f.loader.refresh()]);
    f.loader.poll();
    assert.deepEqual(f.applied, []);
    assert.deepEqual(f.errors, []);
    assert.deepEqual(f.loading, [true]);
  }
});

test('挂载后立即卸载不启动读取，新挂载的加载器独立完成初显', async () => {
  const previous = reloadFixture();
  const unmounted = previous.loader.refresh();
  previous.loader.dispose();
  await settle();
  assert.equal(previous.reads.length, 0);
  await unmounted;
  assert.deepEqual(previous.applied, []);
  const mounted = reloadFixture();
  const initial = mounted.loader.refresh();
  await settle();
  mounted.reads[0].resolve('remounted rows');
  await initial;
  assert.deepEqual(mounted.applied, ['remounted rows']);
  mounted.loader.dispose();
});
