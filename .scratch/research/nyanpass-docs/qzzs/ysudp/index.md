<!-- Source URL: https://nyanpass.pages.dev/qzzs/ysudp/ | Capture date: 2026-11-10 -->

<a id="udp"></a>
# 原生 UDP 问题

**前置知识：请自行搜索 TCP 与 UDP 的区别。**

<a id="udp_1"></a>
## UDP 代理的两种方式

<a id="udp_2"></a>
### 原生 UDP

主流中转代理协议中， Shadowsocks 采用原生 UDP。

因此**只有 Shadowsocks 协议**才会让 Nyanpass 等中转程序处理 UDP。

![图](https://nyanpass.pages.dev/qzzs/udp1.webp)

<a id="udp-over-tcp"></a>
### UDP Over TCP

将 UDP 数据放入 TCP 隧道中传输。

主流中转代理协议中， VMess / Trojan 采用 UDP Over TCP，即从用户到落地都是采用 TCP 传输。

![图](https://nyanpass.pages.dev/qzzs/udp2.webp)

<a id="nyanpass-udp"></a>
## Nyanpass 如何处理 UDP

默认全程原生 UDP。

开启 UDP Over TCP 后，则在入口处接受 UDP 数据，从入口到出口这一段采用 TCP 传输。(如下图)

![图](https://nyanpass.pages.dev/qzzs/udp3.webp)

<a id="shadowsocks-udp"></a>
## 为什么 Shadowsocks UDP 中转不好用

1. 技术上，原生 UDP 中转会导致 MTU 变小，且中转程序无法控制 UDP 速率，只能照样转发。
2. 技术上，某些 Shadowsocks 后端的 UDP 实现不好，造成延迟提升。
3. 由于 DDoS 等原因，运营商经常严格限制 UDP。无论是用户到中转入口，还是入口到出口，都可能存在 UDP 被限制的情况。

![图](https://nyanpass.pages.dev/qzzs/udp4.webp)

> **一个很常见的问题**
>
> 中国三大运营商 IPv4 国际 UDP 基本就是废的，只要 UDP 交互数据量大/对象多，很容易触发临时的源 IP 阻断（类似反向墙，但一般仅影响 UDP）。如果你遇到这种问题，可以考虑在 nyanpass 隧道中开启 udp\_over\_tcp 选项。

<a id="_1"></a>
## 我就想用机场打游戏

<a id="uot"></a>
### 全程 UOT 方案

1. 使用 VMess / Trojan 等带有 UDP Over TCP 的协议。
2. 确保落地机的 UDP 网络稳定。

<a id="_2"></a>
### 全程原生方案

1. 确保入口、出口和落地的 UDP 网络稳定。（国内段听天由命，过墙段建议走 v6 或者专线？）
2. 使用 SS 等带有原生 UDP 中继的协议，确保后端程序稳定。

<a id="uot_1"></a>
### 仅过墙段 UoT 方案

1. 确保入口、出口和落地的 UDP 网络稳定。（国内段听天由命，过墙段开启 `udp_over_tcp`）
2. 使用 SS 等带有原生 UDP 中继的协议，确保后端程序稳定。
