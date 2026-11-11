<!-- Source URL: https://docs.relayx.cc/changelog/0.13.0 | Capture date: 2026-11-10 -->

[← 返回首页](../index.md)

# 0.13.0

2026年3月15日

<a id="rocket-新功能"></a>

### 🚀 新功能

- 增加 `RelayX` 私有隧道协议（Beta 版本，需要更新节点端 0.13.0 版本）
- 增加 `Heleket` 支付方式，支持后台配置商户 UUID、API Key 和收款币种等参数

<a id="nail_care-优化"></a>

### 💅 优化

- `LANDING_PAGE_URL` 首页落地页改为通过 iframe 嵌入展示，并补充后台配置说明
- 节点组部署命令增加 `RelayX` 端口配置

<a id="bug-修复"></a>

### 🐛 修复

- 个人版 license 管理后台不再显示支付方式筛选，避免调用 `/api/pay` 报错
