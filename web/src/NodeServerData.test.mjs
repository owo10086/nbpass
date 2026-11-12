import assert from 'node:assert/strict';
import { test } from 'node:test';
import { matchesServer, serverGroupsFor, serverPortCount } from './NodeServerData.ts';

const groups = [
  { id: 'g1', name: '香港入口', members: ['hk', 'shared'] },
  { id: 'g2', name: '备用入口', members: ['shared', 'jp'] },
];
const server = { id: 'shared', name: 'HK-CN2', public_ipv4: '1.1.1.1', confirmed_ipv4: '8.8.8.8', managed_record: { hostname: 'entry.example.com' }, status: 'online' };

test('服务器同时属于多个组时保留全部成员关系，不把未分组服务器放进其他组', () => {
  assert.deepEqual(serverGroupsFor('shared', groups).map(group => group.id), ['g1', 'g2']);
  assert.deepEqual(serverGroupsFor('new', groups), []);
});

test('端口数合并本机及所属组的启用监听端口，排除停用项、其他服务器并去重', () => {
  const ports = [
    { entry_node_id: 'shared', listen_port: 1000, enabled: true },
    { entry_group_id: 'g1', listen_port: 2000, enabled: true },
    { entry_group_id: 'g2', listen_port: 2000, enabled: true },
    { entry_group_id: 'g2', listen_port: 3000, enabled: true },
    { entry_node_id: 'shared', listen_port: 4000, enabled: false },
    { entry_node_id: 'jp', listen_port: 5000, enabled: true },
    { entry_group_id: 'missing', listen_port: 6000, enabled: true },
  ];
  assert.equal(serverPortCount('shared', groups, ports), 3);
  assert.equal(serverPortCount('new', groups, ports), 0);
});

test('名称和上报地址、确认地址、受管域名都可搜索，忽略首尾空格与大小写', () => {
  for (const query of [' hk-cn2 ', '1.1.1', '8.8.8.8', ' ENTRY.EXAMPLE ']) {
    assert.equal(matchesServer(server, groups, { query, status: 'all', groupID: 'all' }), true);
  }
  assert.equal(matchesServer(server, groups, { query: 'not-found', status: 'all', groupID: 'all' }), false);
});

test('状态和服务器组筛选与搜索同时生效，未分组筛选只匹配无成员关系的服务器', () => {
  assert.equal(matchesServer(server, groups, { query: 'hk', status: 'online', groupID: 'g2' }), true);
  for (const filter of [
    { query: '', status: 'offline', groupID: 'all' },
    { query: '', status: 'all', groupID: 'missing' },
    { query: '', status: 'all', groupID: 'ungrouped' },
    { query: 'jp', status: 'online', groupID: 'g1' },
  ]) assert.equal(matchesServer(server, groups, filter), false);
  assert.equal(matchesServer({ ...server, id: 'new', managed_record: null }, groups, { query: '', status: 'all', groupID: 'ungrouped' }), true);
});

test('名称和安装票据仅发送名称，出站配置独立序列化、清空不丢失', async () => {
  const { serverNamePayload, serverOutboundPayload } = await import('./NodeServerData.ts');
  assert.deepEqual(serverNamePayload({ name: ' A ', outbound_ipv4: '127.0.0.1' }), { name: ' A ' });
  assert.deepEqual(serverOutboundPayload(' 127.0.0.1 '), { outbound_ipv4: '127.0.0.1' });
  assert.deepEqual(serverOutboundPayload(''), { outbound_ipv4: '' });
});
