<!-- Source URL: https://docs.relayx.cc/changelog/0.12.30 | Capture date: 2026-11-10 -->

[← 返回首页](../index.md)

# 0.12.30

2026年3月11日

<a id="rocket-新功能"></a>

### 🚀 新功能

- 后端增加 `/health` 健康检查接口，检测 DB 和 Redis 连接状态
- Dockerfile 增加 HEALTHCHECK 配置
- 节点组支持配置分流白名单/黑名单
- 优化节点组配置界面
- 充值统计增加支付方式筛选

<a id="nail_care-优化"></a>

### 💅 优化

- BEPUSDT 下单逻辑重构：当 `pay_type` 为空时，改用 `create-order` 接口创建订单

<a id="bug-修复"></a>

### 🐛 修复

- 修复 agent `--listen-ip` 参数指定监听 IP 不生效的问题
