<!-- Source URL: https://www.zeroforwarder.com/docs/ | Capture date: 2026-11-10 -->

# 从一条命令开始。

先装好面板，再按需要组合服务器、转发端点和转发链。按 `/` 随时搜索。

```bash
bash <(curl -sL https://get.zeroforwarder.com/install.sh)
```

- 01 · [安装面板](quickstart/index.md)：手动安装，几分钟完成
- 02 · [用 AI 助手安装](agent-install/index.md)：交给 Claude Code / Codex
- 03 · [配置说明](config/index.md)：理解服务器与转发端点

## 开始使用

了解产品，装好面板。

- [产品概览](readme/index.md)：Zero Forwarder 是什么、由哪些组件构成、适合什么场景。
- [安装面板](quickstart/index.md)：准备域名与授权，在专用目录安装、更新并检查 Zero Forwarder 面板。
- [用 AI 助手安装](agent-install/index.md)：让 Claude Code / Codex 非交互地安装、更新面板。
- [用 AI 助手定位问题](agent-diagnose/index.md)：按需使用进程内诊断导出或只读命令，让 AI 定位节点问题，核对权限、开关和日志隐私。

## 核心功能

转发、聚合、选路、入站代理与计费。

- [配置说明](config/index.md)：理解服务器、转发端点、端口和分组的关系，查阅传输、监听、权限与流量设置。
- [服务器组](server-groups/index.md)：把多个 worker 组成入口线路，统一部署端口，配置自动加入、入站策略和 DNS 健康切换。
- [动态域名与 DDNS](dynamic-dns/index.md)：配置 DNS API，把服务器或转发端点上报的公网地址同步到域名，并检查观察态、地址族和写入状态。
- [DNS Webhook 接入](dns-webhook/index.md)：下载与面板版本匹配的接收端，验证签名与记录集协议，接入自建 DNS 或自定义 DNS API。
- [智能 DNS](smart-dns/index.md)：创建独立权威调度节点池，把新入口域名绑定到业务服务器组，核对健康、权重、发布进度与缓存等待。
- [跨面板资源共享](cross-panel-sharing/index.md)：区分共享 worker 与共享转发端点的邀请方向，配置额度、安全限制、安装和退出流程。
- [用户、套餐与计费](users-packages/index.md)：配置用户订阅、套餐线路、流量重置、续费和自动补流量，区分各层权限与额度。
- [权限、租户与 API](access-control/index.md)：配置角色、租户隔离和 API 密钥，核对授权、资源范围、操作审计与登录会话。
- [商城与运营](operations/index.md)：商城、支付、余额订单、兑换码和推广佣金仅商用 tier 支持；另介绍工单与公告操作。
- [转发指南](fwd-guide/index.md)：从第一条端口转发开始，理解直连、单跳、转发链与 TOT 聚合，并检查实际连通性。
- [多线路共同维护](multi-line-rule/index.md)：把多条入口线路纳入同一规则，统一端口与业务配置，完成逐线路预检、扩展和批量维护。
- [动态多级转发链](fwd-chain/index.md)：用端点组组织多层路径，配置保存的转发链、智能选择与固定前后跳数。
- [可视化网络规划](network-planning/index.md)：在端口拓扑图中查看路径与测量，编辑节点、边和 TOT 子流，校验后保存并核对实际连接。
- [单线程带宽聚合](bw-merge/index.md)：在 TOT 聚合表单中配置汇聚服务器、多端点或链式子流，并验证单连接吞吐。
- [多备用线路](multi-fallback-line/index.md)：使用可视化表单配置 Hammer、链式和 TOT 备用路径，区分端口冷备与服务器级候选合并。
- [入站代理](inbound-proxy/index.md)：在入口启用 SS、VLESS、Reality、AnyTLS 等协议，配置证书、转发路径与客户端连接。
- [证书管理与自动续期](certificates/index.md)：申请或上传 TLS 证书，绑定入站代理与转发端点，并验证续期、同步和域名匹配。
- [出口代理](outbound-proxy/index.md)：为入站代理配置 SOCKS5、Hysteria2、TUIC、Juicity 上游，导入分享链接或引用已有端口与共同规则。
- [客户端订阅与模板](client-subscriptions/index.md)：把入站代理端口组织为客户端订阅，配置选择器、代理组、路由、拉取策略和 Token 轮换。
- [带宽限速](bandwidth-speed-limit/index.md)：通过带宽设置配置独立、总带宽和动态限速，理解多层限制与流量额度。
- [NAT 穿透](nat-traversal/index.md)：用公网 worker 为内网服务提供入口，区分业务端口、隧道端口和内网目标，并逐段验证。
- [WireGuard 组网](wireguard-network/index.md)：创建虚拟网络，添加服务器成员和设备，配置 P2P、网关回退、中继与出口节点。
- [解锁服务与分流](unlock-traffic-routing-guide/index.md)：用可视化匹配、出口策略和规则集配置分流，结合解锁检测、防火墙与规则测试验证。
- [防火墙与访问限制](firewall/index.md)：创建允许或拒绝规则，绑定入站、出站与订阅拉取，并检查节点下发和协议过滤状态。

## 场景实践

真实线路组合与实测数据。

- [国内机器最佳实践](china-server/index.md)：为入口 worker 配置可达的控制通信路径，并独立选择业务是否走隧道。
- [IX 使用说明](ix-guide/index.md)：区分 IX 的业务中转与控制通信，配置可达地址、端口及必经层。
- [Halo IX 机器](halo-ix/index.md)：为受限网络的 IX 节点准备可达管理路径，并核对离线部署所需实例文件。
- [一机拉全球](global-cdn-config/index.md)：以一台入口接入多个地区落地，用端点组、链式路径和规则分流管理出口。
- [沪日美专线组网](jp-us-lan/index.md)：把 IX、日美内网映射与落地端点组织成可验证的多层路径。
- [延迟实测](latency-test/index.md)：保留历史 300 并发测试数据，说明测量条件和当前配置入口。

## 帮助支持

遇到问题先看这里。

- [连接诊断与测速](diagnostics-speed-test/index.md)：用实时连接、拦截记录、延迟监控和速度诊断定位问题，区分测速范围、限速影响与流量计费。
- [告警与通知](alerts-notifications/index.md)：查看故障和趋势，配置告警阈值、Telegram、邮件与租户通知策略，并区分记录状态和真实恢复。
- [备份、迁移与恢复](backup-migration/index.md)：使用当前安装脚本打包和恢复面板，核对版本、数据库、历史数据、外部依赖与回退。
- [常见问题](faq/index.md)：排查安装、离线、转发与 UDP 问题，核对当前配置路径和迁移方式。
