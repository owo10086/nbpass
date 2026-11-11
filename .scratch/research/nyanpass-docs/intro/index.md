<!-- Source URL: https://nyanpass.pages.dev/intro/ | Capture date: 2026-11-10 -->

<a id="_1"></a>
# 介绍

![](https://nyanpass.pages.dev/assets/nyaachoZiii.png) Telegram 频道 @nyanpass\_tz

![](https://nyanpass.pages.dev/assets/nyaachoAaaaaa.png) 演示站点、定价、购买流程 请看 Telegram 频道置顶信息。

![](https://nyanpass.pages.dev/assets/nyaachoQ.png) **有问题不要问作者，请自行参阅文档，或者在群里询问。**（没人回复是很正常的）

![](https://nyanpass.pages.dev/assets/nyaachoSuki.png)

目前由于我方条件限制，托管开户和各种需要人工处理的问题，处理速度会非常缓慢。

因此我们非常推荐您改用其他面板，以免浪费您的宝贵时间。

**不会搭建，不会使用的，请另寻其他产品。请不要骚扰官方人员。**

<a id="_2"></a>
## 基本功能

- 用户管理
- 用户/服务器分组
- 流量/速度/IP数限制
- 规则管理（可以批量操作）
- 规则分组
- 节点服务器管理（实时状态与管理员在线终端）
- 节点服务器分组
- 规则级/用户级/全站流量统计
- 一键对接节点服务器

<a id="_3"></a>
## 商城

- 订单管理
- 套餐购买
- 自动续费
- EPAY/USDT 充值
- 每日/每月全站充值统计

<a id="_4"></a>
## 转发协议

- ws -> http
- tls
- 出口机器无需域名，默认使用公网 IP
- 可指定内网 IP，实现双机专线租网
- 可不使用隧道，直接转发
- 已优化性能和稳定性

<a id="_5"></a>
### 负载均衡

- 基于连接数的出口服务器负载均衡
- 出口服务器自动故障转移
- 目标地址多种负载均衡策略
- 目标地址故障转移

<a id="_6"></a>
## 技术栈

面板采用前后端分离架构，面板本身只提供 API，管理界面（WebUI）是由前端提供的。

<a id="-"></a>
### 面板 - 用户/管理前端

`frontend/public`

`TypeScript + Vite + React + antd`

<a id="-_1"></a>
### 面板 - 后端

`backend`

`Go + Gin + Gorm(SQLite/MySQL/PostgreSQL)`

<a id="_7"></a>
### 节点端

`nodeclient`

`Go + 一键安装脚本`

节点端部分配置采用联网下发，如出口跳板("对端")的连接 IP 等信息都由面板端下发。节点运行中与面板暂时失联不会影响现有规则。
