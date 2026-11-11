import assert from 'node:assert/strict';
import { test } from 'node:test';
import { resolveRoute, go } from './src/route.ts';

test('旧链接跳转到新页面，原样保留全部查询参数并解码 focus', () => {
  for (const [old, page] of [['rules', 'ports'], ['nodes', 'servers'], ['entry-groups', 'server-groups'], ['exit-groups', 'endpoint-groups']]) {
    assert.deepEqual(resolveRoute(`#/${old}?focus=a%2Fb&filter=online&filter=ready&note=a%3Fb`), {
      page, focus: 'a/b', hash: `#/${page}?focus=a%2Fb&filter=online&filter=ready&note=a%3Fb`,
    });
  }
});

test('新侧栏的每个页面都能直接打开', () => {
  for (const page of ['overview', 'ports', 'servers', 'endpoints', 'endpoint-groups', 'chains', 'server-groups', 'smart-dns', 'latency', 'dns-watch', 'incidents', 'events', 'telegram', 'security', 'retention']) {
    assert.equal(resolveRoute(`#/${page}`).page, page);
    assert.equal(resolveRoute(`#/${page}`).hash, `#/${page}`);
  }
});

test('空地址和未知页面回到概览，查询参数仍保留', () => {
  assert.equal(resolveRoute('').hash, '#/overview');
  assert.equal(resolveRoute('#/missing?focus=123').hash, '#/overview?focus=123');
});

test('内部旧调用生成新链接并安全编码定位 ID', () => {
  const previous = globalThis.window;
  globalThis.window = { location: { hash: '' } };
  try {
    go('nodes', 'node/a?b&c');
    assert.equal(window.location.hash, '/servers?focus=node%2Fa%3Fb%26c');
    go('rules');
    assert.equal(window.location.hash, '/ports');
  } finally {
    globalThis.window = previous;
  }
});
