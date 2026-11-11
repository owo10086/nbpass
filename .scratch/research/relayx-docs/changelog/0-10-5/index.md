<!-- Source URL: https://docs.relayx.cc/changelog/0.10.5 | Capture date: 2026-11-10 -->

[← 返回首页](../page/4/index.md)

# 0.10.5

2025年7月11日

<a id="nail_care-优化"></a>

### 💅 优化

- license 过期显示优化
- 现在出口机器最多只需要监听 6 个端口，而不是之前的每个隧道一个端口（需要更新节点端）
- 出口机器可以手动指定监听端口，如：`--wss-port 8080`，格式为：`--<协议>-port <端口>`
- 默认开启 Proxy Protocol，不需要手动配置
- 移除单个节点升级，增加一键升级所有节点

<a id="bug-修复"></a>

### 🐛 修复

- 修复多隧道转发同一地址问题
