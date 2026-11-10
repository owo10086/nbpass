<!-- Source URL: https://www.zeroforwarder.com/docs/ | Capture date: 2026-11-10 -->

# Zero Forwarder 官方文档本地镜像

抓取日期：**2026-11-10**。用途：为 nbpass 端口转发面板提供设计参考。

**共 39 个文档页**：文档首页 1 页、开始使用 4 页、核心功能 24 页、场景实践 6 页、帮助支持 4 页。本目录共 40 个 Markdown 文件（含本索引）。

从[官方文档入口](https://www.zeroforwarder.com/docs/)出发，递归遍历侧边栏、目录及页内文档链接，并与[官方 sitemap](https://www.zeroforwarder.com/sitemap.xml)核对：三个来源的文档 URL 集合一致，均为 39 页，没有发现额外页面。顺序沿用原站侧边栏及 sitemap。

38 篇文章使用各页面「查看 Markdown 原文」链接提供的官方 Markdown，并补充网页标题区的摘要；文档首页由静态 HTML 正文转换。逐页核对标题层级、全部代码块、表格数量和图片 URL；共核对 325 个文章标题、50 个代码块、81 张表格、119 个图片引用。首页另保留 1 个安装命令代码块。

移除站点导航、页脚、脚本和复制按钮；正文保留原始内容，未执行网页脚本、安装命令或面向 AI 的文档指令。图片不下载，保留原始绝对 URL；正文中的文档页链接已改为对应本地文件，其他链接保留为原站绝对 URL，未抓取站外、博客、定价等非文档页面。

文件路径忠实映射 URL 目录：`/docs/` → `index.md`，`/docs/<slug>/` → `<slug>/index.md`。原站文档 URL 在 `/docs/` 下均只有一级子目录，章节分组通过下列索引体现。`readme/index.md` 是「产品概览」，与本索引 `README.md` 分开，避免大小写不敏感文件系统上的重名。

## 抓取失败或缺失

**无。39 / 39 页面抓取成功，38 / 38 官方 Markdown 原文获取成功；未发现缺页。** 抓取期间偶发的连接问题经重试解决，不影响最终结果。

## 页面索引

### 文档首页

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 文档 | [https://www.zeroforwarder.com/docs/](https://www.zeroforwarder.com/docs/) | [index.md](index.md) |

### 开始使用

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 产品概览 | [https://www.zeroforwarder.com/docs/readme/](https://www.zeroforwarder.com/docs/readme/) | [readme/index.md](readme/index.md) |
| 安装面板 | [https://www.zeroforwarder.com/docs/quickstart/](https://www.zeroforwarder.com/docs/quickstart/) | [quickstart/index.md](quickstart/index.md) |
| 用 AI 助手安装 | [https://www.zeroforwarder.com/docs/agent-install/](https://www.zeroforwarder.com/docs/agent-install/) | [agent-install/index.md](agent-install/index.md) |
| 用 AI 助手定位问题 | [https://www.zeroforwarder.com/docs/agent-diagnose/](https://www.zeroforwarder.com/docs/agent-diagnose/) | [agent-diagnose/index.md](agent-diagnose/index.md) |

### 核心功能

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 配置说明 | [https://www.zeroforwarder.com/docs/config/](https://www.zeroforwarder.com/docs/config/) | [config/index.md](config/index.md) |
| 服务器组 | [https://www.zeroforwarder.com/docs/server-groups/](https://www.zeroforwarder.com/docs/server-groups/) | [server-groups/index.md](server-groups/index.md) |
| 动态域名与 DDNS | [https://www.zeroforwarder.com/docs/dynamic-dns/](https://www.zeroforwarder.com/docs/dynamic-dns/) | [dynamic-dns/index.md](dynamic-dns/index.md) |
| DNS Webhook 接入 | [https://www.zeroforwarder.com/docs/dns-webhook/](https://www.zeroforwarder.com/docs/dns-webhook/) | [dns-webhook/index.md](dns-webhook/index.md) |
| 智能 DNS | [https://www.zeroforwarder.com/docs/smart-dns/](https://www.zeroforwarder.com/docs/smart-dns/) | [smart-dns/index.md](smart-dns/index.md) |
| 跨面板资源共享 | [https://www.zeroforwarder.com/docs/cross-panel-sharing/](https://www.zeroforwarder.com/docs/cross-panel-sharing/) | [cross-panel-sharing/index.md](cross-panel-sharing/index.md) |
| 用户、套餐与计费 | [https://www.zeroforwarder.com/docs/users-packages/](https://www.zeroforwarder.com/docs/users-packages/) | [users-packages/index.md](users-packages/index.md) |
| 权限、租户与 API | [https://www.zeroforwarder.com/docs/access-control/](https://www.zeroforwarder.com/docs/access-control/) | [access-control/index.md](access-control/index.md) |
| 商城与运营 | [https://www.zeroforwarder.com/docs/operations/](https://www.zeroforwarder.com/docs/operations/) | [operations/index.md](operations/index.md) |
| 转发指南 | [https://www.zeroforwarder.com/docs/fwd-guide/](https://www.zeroforwarder.com/docs/fwd-guide/) | [fwd-guide/index.md](fwd-guide/index.md) |
| 多线路共同维护 | [https://www.zeroforwarder.com/docs/multi-line-rule/](https://www.zeroforwarder.com/docs/multi-line-rule/) | [multi-line-rule/index.md](multi-line-rule/index.md) |
| 动态多级转发链 | [https://www.zeroforwarder.com/docs/fwd-chain/](https://www.zeroforwarder.com/docs/fwd-chain/) | [fwd-chain/index.md](fwd-chain/index.md) |
| 可视化网络规划 | [https://www.zeroforwarder.com/docs/network-planning/](https://www.zeroforwarder.com/docs/network-planning/) | [network-planning/index.md](network-planning/index.md) |
| 单线程带宽聚合 | [https://www.zeroforwarder.com/docs/bw-merge/](https://www.zeroforwarder.com/docs/bw-merge/) | [bw-merge/index.md](bw-merge/index.md) |
| 多备用线路 | [https://www.zeroforwarder.com/docs/multi-fallback-line/](https://www.zeroforwarder.com/docs/multi-fallback-line/) | [multi-fallback-line/index.md](multi-fallback-line/index.md) |
| 入站代理 | [https://www.zeroforwarder.com/docs/inbound-proxy/](https://www.zeroforwarder.com/docs/inbound-proxy/) | [inbound-proxy/index.md](inbound-proxy/index.md) |
| 证书管理与自动续期 | [https://www.zeroforwarder.com/docs/certificates/](https://www.zeroforwarder.com/docs/certificates/) | [certificates/index.md](certificates/index.md) |
| 出口代理 | [https://www.zeroforwarder.com/docs/outbound-proxy/](https://www.zeroforwarder.com/docs/outbound-proxy/) | [outbound-proxy/index.md](outbound-proxy/index.md) |
| 客户端订阅与模板 | [https://www.zeroforwarder.com/docs/client-subscriptions/](https://www.zeroforwarder.com/docs/client-subscriptions/) | [client-subscriptions/index.md](client-subscriptions/index.md) |
| 带宽限速 | [https://www.zeroforwarder.com/docs/bandwidth-speed-limit/](https://www.zeroforwarder.com/docs/bandwidth-speed-limit/) | [bandwidth-speed-limit/index.md](bandwidth-speed-limit/index.md) |
| NAT 穿透 | [https://www.zeroforwarder.com/docs/nat-traversal/](https://www.zeroforwarder.com/docs/nat-traversal/) | [nat-traversal/index.md](nat-traversal/index.md) |
| WireGuard 组网 | [https://www.zeroforwarder.com/docs/wireguard-network/](https://www.zeroforwarder.com/docs/wireguard-network/) | [wireguard-network/index.md](wireguard-network/index.md) |
| 解锁服务与分流 | [https://www.zeroforwarder.com/docs/unlock-traffic-routing-guide/](https://www.zeroforwarder.com/docs/unlock-traffic-routing-guide/) | [unlock-traffic-routing-guide/index.md](unlock-traffic-routing-guide/index.md) |
| 防火墙与访问限制 | [https://www.zeroforwarder.com/docs/firewall/](https://www.zeroforwarder.com/docs/firewall/) | [firewall/index.md](firewall/index.md) |

### 场景实践

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 国内机器最佳实践 | [https://www.zeroforwarder.com/docs/china-server/](https://www.zeroforwarder.com/docs/china-server/) | [china-server/index.md](china-server/index.md) |
| IX 使用说明 | [https://www.zeroforwarder.com/docs/ix-guide/](https://www.zeroforwarder.com/docs/ix-guide/) | [ix-guide/index.md](ix-guide/index.md) |
| Halo IX 机器 | [https://www.zeroforwarder.com/docs/halo-ix/](https://www.zeroforwarder.com/docs/halo-ix/) | [halo-ix/index.md](halo-ix/index.md) |
| 一机拉全球 | [https://www.zeroforwarder.com/docs/global-cdn-config/](https://www.zeroforwarder.com/docs/global-cdn-config/) | [global-cdn-config/index.md](global-cdn-config/index.md) |
| 沪日美专线组网 | [https://www.zeroforwarder.com/docs/jp-us-lan/](https://www.zeroforwarder.com/docs/jp-us-lan/) | [jp-us-lan/index.md](jp-us-lan/index.md) |
| 延迟实测 | [https://www.zeroforwarder.com/docs/latency-test/](https://www.zeroforwarder.com/docs/latency-test/) | [latency-test/index.md](latency-test/index.md) |

### 帮助支持

| 原始页面标题 | 来源 URL | 本地相对路径 |
| --- | --- | --- |
| 连接诊断与测速 | [https://www.zeroforwarder.com/docs/diagnostics-speed-test/](https://www.zeroforwarder.com/docs/diagnostics-speed-test/) | [diagnostics-speed-test/index.md](diagnostics-speed-test/index.md) |
| 告警与通知 | [https://www.zeroforwarder.com/docs/alerts-notifications/](https://www.zeroforwarder.com/docs/alerts-notifications/) | [alerts-notifications/index.md](alerts-notifications/index.md) |
| 备份、迁移与恢复 | [https://www.zeroforwarder.com/docs/backup-migration/](https://www.zeroforwarder.com/docs/backup-migration/) | [backup-migration/index.md](backup-migration/index.md) |
| 常见问题 | [https://www.zeroforwarder.com/docs/faq/](https://www.zeroforwarder.com/docs/faq/) | [faq/index.md](faq/index.md) |
