import assert from 'node:assert/strict';
import { test } from 'node:test';
import { endpointMemberOptions, memberKey } from './EndpointData.ts';

test('每台服务器只提供一个端点选项，使用唯一连接地址和有效端口', () => {
  const options = endpointMemberOptions([
    { node_id: 'b', address: 'exit.test', listen_port: 18443, public_port: 2443 },
    { node_id: 'c', address: '10.0.0.2', listen_port: 8443, public_port: null },
  ], [{ id: 'b', name: 'B' }, { id: 'c', name: 'C' }]);
  assert.deepEqual(options.map(o => [o.node_id, o.address_id, o.name, o.address]), [
    ['b', 'public', 'B', 'exit.test:2443'], ['c', 'public', 'C', '10.0.0.2:8443'],
  ]);
  assert.equal(options[0].id, memberKey({ node_id: 'b', address_id: 'public' }));
});
