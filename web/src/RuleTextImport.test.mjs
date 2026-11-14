import assert from 'node:assert/strict';
import { test } from 'node:test';
import { parseRuleText, buildRuleImportPayload, mapRuleImportResults } from './RuleTextImport.ts';

test('解析多行规则，跳过空行、保留原文行号并去除字段两端空白', () => {
  assert.deepEqual(parseRuleText('\r\n zx香港 # 33311 # hka.4441234.xyz # 3400 \r\n \t\r\n备用#65535#8.8.8.8#1\r\n'), {
    rows: [
      { line: 2, name: 'zx香港', listen_port: 33311, target_address: 'hka.4441234.xyz', target_port: 3400 },
      { line: 4, name: '备用', listen_port: 65535, target_address: '8.8.8.8', target_port: 1 },
    ],
    errors: [],
  });
});

test('只认半角分隔符，段数必须为四且不支持注释', () => {
  for (const source of ['香港＃33311＃example.com＃443', '香港#33311#example.com', '香港#33311#example.com#443#多余', '#注释']) {
    const result = parseRuleText(`\n${source}`);
    assert.deepEqual(result.rows, []);
    assert.equal(result.errors.length, 1);
    assert.equal(result.errors[0].line, 2);
    assert.match(result.errors[0].message, /4 段/);
  }
});

test('名称必填且以 UTF-8 字节计算 200 字节上限，允许重名', () => {
  for (const name of ['', ' \t ', 'a'.repeat(201), '港'.repeat(67), '😀'.repeat(51)]) {
    const result = parseRuleText(`${name}#33311#example.com#443`);
    assert.deepEqual(result.rows, []);
    assert.equal(result.errors[0].line, 1);
    assert.match(result.errors[0].message, /名称/);
  }
  for (const name of ['a'.repeat(200), '港'.repeat(66) + 'ab', '😀'.repeat(50)]) {
    const result = parseRuleText(`${name}#1#example.com#443\n${name}#2#example.com#443`);
    assert.equal(result.rows.length, 2);
    assert.deepEqual(result.errors, []);
  }
});

test('监听与目标端口必填，只允许十进制数字且范围为 1–65535', () => {
  for (const value of ['0', '65536', '-1', '1.5', 'abc', '', ' ', '1e3', '0x50', '+80', '４４３']) {
    for (const source of [`香港#${value}#example.com#443`, `香港#33311#example.com#${value}`]) {
      const result = parseRuleText(source);
      assert.deepEqual(result.rows, []);
      assert.equal(result.errors[0].line, 1);
      assert.match(result.errors[0].message, /端口/);
    }
  }
  assert.deepEqual(parseRuleText('香港#00080#example.com#65535').errors, []);
});

test('本地错误保留原文行号，不丢失其余有效行，地址校验交给服务端', () => {
  const result = parseRuleText('第一条#1#example.com#443\n\n错误#0#example.com#443\n第二条#2##443\n第三条#3#::1#443');
  assert.deepEqual(result.rows.map(row => row.line), [1, 4, 5]);
  assert.equal(result.rows[1].target_address, '');
  assert.equal(result.rows[2].target_address, '::1');
  assert.equal(result.errors.length, 1);
  assert.equal(result.errors[0].line, 3);
});

test('空文本不产生规则，整批最多 500 条，超限错误指向原文行号', () => {
  assert.deepEqual(parseRuleText(' \n\r\n\t'), { rows: [], errors: [] });
  const source = Array.from({ length: 500 }, (_, index) => `规则#${index + 1}#example.com#443`).join('\n');
  const allowed = parseRuleText(source);
  assert.equal(allowed.rows.length, 500);
  assert.deepEqual(allowed.errors, []);
  const overflow = parseRuleText(`${source}\n\n第501条#501#example.com#443`);
  assert.equal(overflow.errors.length, 1);
  assert.equal(overflow.errors[0].line, 502);
  assert.match(overflow.errors[0].message, /500/);
});

test('两种入口与三种链路生成格式 3 定义，清除隐藏引用并全部启用', () => {
  const rows = parseRuleText('\n香港#33311#example.com#443\n\n备用#33312#8.8.8.8#53').rows;
  for (const [line, entry] of [['node:entry', { entry_node_id: 'entry' }], ['group:entries', { entry_group_id: 'entries' }]]) {
    for (const [mode, path] of [
      ['direct', { kind: 'direct' }],
      ['single', { kind: 'tunnel', exit_group_id: 'exits' }],
      ['chain', { kind: 'tunnel', chain_id: 'chain' }],
    ]) {
      assert.deepEqual(buildRuleImportPayload(rows, { line, mode, exit_group_id: 'exits', chain_id: 'chain', allow_private: false }), {
        format_version: 3,
        rules: [
          { name: '香港', listen_port: 33311, target_address: 'example.com', target_port: 443, target_family: 'ipv4', allow_private: false, enabled: true, ...entry, ...path },
          { name: '备用', listen_port: 33312, target_address: '8.8.8.8', target_port: 53, target_family: 'ipv4', allow_private: false, enabled: true, ...entry, ...path },
        ],
      });
    }
  }
  assert.deepEqual(rows.map(row => row.line), [2, 4]);
});

test('允许私网目标选项整批透传，不修改解析行', () => {
  const rows = parseRuleText('私网#33311#192.168.1.1#443').rows;
  for (const allow_private of [false, true]) {
    const payload = buildRuleImportPayload(rows, { line: 'node:entry', mode: 'direct', allow_private });
    assert.equal(payload.rules[0].allow_private, allow_private);
    assert.equal(payload.rules[0].target_address, '192.168.1.1');
    assert.equal(payload.rules[0].line, undefined);
    assert.equal(payload.rules[0].id, undefined);
    assert.equal(payload.rules[0].mode, undefined);
  }
  assert.equal(rows[0].line, 1);
});

test('预检逐条结果按数组位置映射原文行号，保留服务端错误与冲突信息', () => {
  const rows = parseRuleText('\n第一条#33311#example.com#443\n\n第二条#33312#example.com#443').rows;
  const results = [
    { index: 1, name: '第一条', errors: [] },
    { index: 2, name: '第二条', errors: ['监听端口冲突'], conflict_count: 2 },
  ];
  assert.deepEqual(mapRuleImportResults(results, rows), [
    { index: 1, name: '第一条', errors: [], line: 2 },
    { index: 2, name: '第二条', errors: ['监听端口冲突'], conflict_count: 2, line: 4 },
  ]);
  assert.equal(results[0].line, undefined);
});
