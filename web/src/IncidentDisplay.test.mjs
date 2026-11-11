import assert from 'node:assert/strict';
import { test } from 'node:test';
import * as display from './IncidentDisplay.ts';

test('故障标题只转换端口故障的固定后缀，保留含旧词和冒号的历史名称', () => {
  const title = '节点：入口组：配置、监听或隧道证据：配置、监听或隧道证据';
  assert.equal(display.displayIncidentTitle({ kind: 'rule', title }), '节点：入口组：配置、监听或隧道证据：配置、监听或转发证据');
  assert.equal(display.displayIncidentTitle({ kind: 'offline', title }), title);
  assert.equal(display.displayIncidentTitle({ kind: 'rule', title: '隧道自定义故障' }), '隧道自定义故障');
});

test('补齐转发证据系统文案，不改写用户名称、原始错误与策略规则', () => {
  assert.equal(display.displaySystemText('配置、监听或隧道'), '配置、监听或转发');
  assert.equal(display.displaySystemText('隧道规则“隧道出口节点”没有获授权出口的新鲜成功握手'), '单跳转发端口“隧道出口节点”没有获授权转发端点的新鲜成功握手');
  assert.equal(display.displaySystemText('转发规则“节点入口组”尚未应用'), '端口“节点入口组”尚未应用');
  assert.equal(display.displaySystemText('节点 n，期望 5，报告 3；出口组 g'), '服务器 n，期望 5，报告 3；端点组 g');
  for (const value of ['隧道错误：节点 abc 规则 xyz', '维护期间沿用现有自动处理抑制规则', '暂停规则与冻结规则', '入口组自定义名称']) {
    assert.equal(display.displaySystemText(value), value);
  }
});

test('嵌入名称及换 IP 通知只转换可识别正文，名称中的旧词原样保留', () => {
  assert.equal(display.displaySystemText('节点入口组：节点维护中', '节点入口组：'), '节点入口组：服务器维护中');
  assert.equal(display.displaySystemText('入口组 入口组旧名称，策略 rotate_ip，决策 waiting', '', true), '服务器组 入口组旧名称，策略 rotate_ip，决策 waiting');
  assert.equal(display.displayRotationNotice('节点：旧名称：节点不支持新 IP 复检，自动许可已取消且未投递，动作 action-1'), '节点：旧名称：服务器不支持新 IP 复检，自动许可已取消且未投递，动作 action-1');
});
