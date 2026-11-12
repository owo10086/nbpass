import assert from 'node:assert/strict';
import { test } from 'node:test';
import { endpointMemberOptions, memberKey, replaceMemberAddresses, moveMembers } from './EndpointData.ts';

test('成员选项按服务器及稳定地址区分，显示地址名称和有效对外端口', () => {
  const options = endpointMemberOptions([{ node_id: 'b', address: 'public.test', listen_port: 18443, public_port: 2443, dedicated_addresses: [
    { id: 'ix', name: '沪日 IX', address: 'ix.test', public_port: null },
  ] }], [{ id: 'b', name: 'B' }]);
  assert.deepEqual(options.map(o => [o.node_id, o.address_id, o.name, o.address]), [
    ['b', 'public', 'B · 公网', 'public.test:2443'], ['b', 'ix', 'B · 沪日 IX', 'ix.test:18443'],
  ]);
  assert.notEqual(options[0].id, options[1].id);
  assert.equal(options[1].id, memberKey({ node_id: 'b', address_id: 'ix' }));
});

test('选择同服务器另一地址替换原位置，跨服务器追加并保持主备顺序', () => {
  const current = [{ node_id: 'b', address_id: 'public' }, { node_id: 'c', address_id: 'ix-c' }];
  const next = replaceMemberAddresses(current, [{ node_id: 'b', address_id: 'ix-b' }, { node_id: 'd', address_id: 'public' }]);
  assert.deepEqual(next, [{ node_id: 'b', address_id: 'ix-b' }, current[1], { node_id: 'd', address_id: 'public' }]);
  assert.deepEqual(current[0], { node_id: 'b', address_id: 'public' });
  assert.deepEqual(replaceMemberAddresses(next, [next[0]]), next);
  assert.throws(() => replaceMemberAddresses(current, [{ node_id: 'b', address_id: 'ix' }, { node_id: 'b', address_id: 'public' }]), /同一服务器/);
  const ids = next.map(memberKey);
  assert.deepEqual(moveMembers(ids, [ids[1]], 'up'), [ids[1], ids[0], ids[2]]);
});
