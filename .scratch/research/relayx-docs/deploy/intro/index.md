<!-- Source URL: https://docs.relayx.cc/deploy/intro | Capture date: 2026-11-10 -->

# 介绍

<a id="基本功能"></a>

## 基本功能

- 用户管理，支持邮箱、Google、Passkey 等多种登录方式。
- 套餐管理，支持价格、隧道数、连接数、流量、网速等多种配置。
- 支持易支付、BEPUSDT 等多种支付方式。
- 支持多种转发协议，包括 TCP、UDP、WS、TLS 等。
- 支持多级别流量统计，如隧道、节点、用户等。
- 支持实时查看节点状态。
- 一键部署节点端，一键升级，一键卸载。
- 更多功能，等待你来发现。

<a id="demo-环境"></a>

## Demo 环境

我们提供了一个 Demo 环境，你可以在这里测试完整的功能。注意：Demo 环境只能测试各项配置，不能部署节点等。

- 地址：[https://demo.relayx.cc](https://demo.relayx.cc)
- 用户名：`demo@relayx.cc`
- 密码：`hellorelayx`

<a id="界面截图"></a>

## 界面截图

![截图1](https://docs.relayx.cc/assets/images/1-c9531f037df89695ec6d0c2015b5944d.png) ![截图2](https://docs.relayx.cc/assets/images/2-bfb727b37a72643ad84dbc9389f5c693.png) ![截图3](https://docs.relayx.cc/assets/images/3-fe1675686de873703e07a95c6a8bc147.png) ![截图4](https://docs.relayx.cc/assets/images/4-869ac3389d7559480fb83ca84ae1c8a8.png) ![截图5](https://docs.relayx.cc/assets/images/5-4bb8f49afc190cdd336724cb8a7fdcaa.png) ![截图6](https://docs.relayx.cc/assets/images/6-a81c926ffa584a789bfdfa655094f7da.png) ![截图7](https://docs.relayx.cc/assets/images/7-313a120693ec6505b784b549696ac1de.png) ![截图8](https://docs.relayx.cc/assets/images/8-a9b45cb785a51c83929166f813b53bdb.png)

<a id="套餐与支付管理"></a>

## 套餐与支付管理

- 通过不同套餐区分用户权限。
- 支持自动续费和到期不允许续费。
- 收入统计，购买统计。

<a id="转发协议"></a>

## 转发协议

- TCP
- UDP
- WS 加密
- TLS 加密

多种协议支持，满足不同需求。支持负载均衡。

<a id="技术栈"></a>

## 技术栈

<a id="前端"></a>

### 前端

- Next.js
- Shadcn UI

前端使用 Next.js 和 Shadcn UI 构建。

<a id="后端"></a>

### 后端

- Hono
- Prisma
- MySQL
- Redis
- StackAuth

后端使用 Hono 构建，使用 Prisma 进行数据库操作，使用 MySQL 作为数据库，使用 Redis 作为缓存，使用 StackAuth 进行用户管理。

<a id="节点端"></a>

### 节点端

- Go

节点端使用 Go 开发，内存占用低，性能高。
