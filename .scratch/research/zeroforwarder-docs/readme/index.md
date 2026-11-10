<!-- Source URL: https://www.zeroforwarder.com/docs/readme/ | Capture date: 2026-11-10 -->

# 产品概览

Zero Forwarder 是什么、由哪些组件构成、适合什么场景。

Zero Forwarder（ZFC）是一套**自托管**的网络转发面板。数据面由 Rust 编写，控制面统一管理你名下的所有机器：端口转发、隧道、多级转发链、带宽聚合、入站代理协议、多用户计费，都在同一个面板里完成。

面板装在你自己的 VPS 上，业务流量按你配置的入口、中转和出口路径传输；使用共享节点或上游代理时也会经过相应资源。

## 由哪些部分组成

| 组件 | 作用 |
| --- | --- |
| **面板（Web + 控制器）** | 部署在一台 VPS 上，用 Docker 运行。负责配置、下发、监控、计费。 |
| **服务器（worker）** | 流量入口。每条端口转发规则都挂在一台服务器上。 |
| **转发端点（forwarder）** | 中转或落地节点。可组成端点组、转发链，做聚合或备用线路。 |

面板通过控制器域名与各节点保持长连接，配置变更实时下发，节点状态与延迟实时回传。

## 能做什么

- **端口转发**：直连转发、隧道转发、多级转发链，TCP / UDP 都支持。
- **[多线路共同维护](../multi-line-rule/index.md)**：多个入口共用业务配置与监听端口，支持逐线路预检、扩展和批量维护。
- **单线程带宽聚合（TOT）**：把多台小带宽中转并行起来，单条 TCP 连接也可利用多条子流，实际效果受路径和节点瓶颈约束。
- **动态转发链（HammerChain）**：测量一直延伸到最终的目标网站，按入口到目标的**端到端总延迟**选路，而不是逐跳挑最快；节点故障或延迟抖动时自动重新选路。
- **[入口服务器组](../server-groups/index.md)**：多台入口机共用一条组线路，可通过 DNS 服务商接口维护统一域名记录，按健康证据、确认窗口与最少保留数执行切换。
- **[动态域名与 DDNS](../dynamic-dns/index.md)**：把服务器或转发端点上报的公网地址同步到固定域名，支持观察态和写入状态核对。
- **[智能 DNS](../smart-dns/index.md)**：独立权威节点池按业务组健康与权重调整后续解析，支持发布验证、观察点和退役进度管理。
- **多备用线路**：通过可视化卡片配置端口备用路径，按健康状态切换和恢复，需考虑长连接重连。
- **入站代理**：在入口机直接提供 Shadowsocks、VLESS、Reality、AnyTLS、Hysteria2、TUIC、Juicity、Snell、WireGuard、SOCKS5 等协议，落地机无需单独部署。
- **[出口代理](../outbound-proxy/index.md)**：配置 SOCKS5、Hysteria2、TUIC、Juicity 上游，或引用已有端口和共同规则。
- **解锁与分流**：用可视化域名、IP、GeoIP、ASN 和组合条件匹配，按规则选择出口，并通过规则测试核对。
- **多用户与计费**：按用户配置流量额度、限速、到期时间和重置周期。
- **实时监控与[诊断测速](../diagnostics-speed-test/index.md)**：节点在线状态、流量、链路延迟、连接统计，以及按路径执行的速度诊断。
- **AI 助手**：让 Claude Code / Codex 全自动安装面板，或在出问题时只读地远程定位。

## 按任务查阅教程

| 任务 | 教程 |
| --- | --- |
| 接出内网服务、服务器与设备互联 | [NAT 穿透](../nat-traversal/index.md) · [WireGuard 组网](../wireguard-network/index.md) |
| 两个面板合作使用机器 | [跨面板资源共享](../cross-panel-sharing/index.md) |
| 调整拓扑、节点与边 | [可视化网络规划](../network-planning/index.md) |
| 给客户端提供节点配置 | [订阅链接与模板](../client-subscriptions/index.md) |
| 接入自建 DNS 或自定义 DNS API | [DNS Webhook 接入](../dns-webhook/index.md) · [动态域名与 DDNS](../dynamic-dns/index.md) |
| 分配线路与流量、运营套餐 | [用户、套餐与计费](../users-packages/index.md) · [商城与运营](../operations/index.md) |
| 维护 TLS 和访问控制 | [证书管理](../certificates/index.md) · [防火墙](../firewall/index.md) |
| 多人维护、自动化接入 | [权限、租户与 API](../access-control/index.md) |
| 值班、恢复与迁机 | [告警与通知](../alerts-notifications/index.md) · [备份与迁移](../backup-migration/index.md) |

## 授权与价格

Zero Forwarder 不提供免费版。授权 **$6 / 月起**，包含：

- 50 个用户
- 50 台机器
- 10 TB 月流量

需要更大规模，联系我们定制。安装时需要的 `ZFC_INSTANCE_ID` 和 `ZFC_API_KEY` 在购买授权后获得。

授权分为**个人 tier**与**商用 tier**。商城与套餐销售、支付接入、余额与订单、兑换码发行与兑换、推广与佣金等运营功能**仅商用 tier 支持，个人 tier 不支持**。商用授权与报价请通过[首页定价](https://www.zeroforwarder.com/#pricing)联系咨询，操作说明见[商城与运营](../operations/index.md)。

## 下一步

- [安装面板](../quickstart/index.md)：一条命令完成部署
- [用 AI 助手安装](../agent-install/index.md)：把提示词交给 AI，剩下的它来做
- [配置说明](../config/index.md)：理解服务器、转发端点、端口的关系
